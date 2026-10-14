// Package netlify turns a repository's netlify.toml into a container build:
// a build stage mimicking Netlify's build image (Node, Hugo, Go, Ruby, Python,
// automatic dependency install) and a runtime stage serving the publish
// directory with Netlify's redirect and header semantics (see ./server).
package netlify

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed server/main.go server/rules.go server/functions.go functions/runtime.mjs
var embedded embed.FS

const (
	DefaultNodeVersion = "22"
	DefaultHugoVersion = "0.168.0"
	DefaultGoVersion   = "1.26"
	// EsbuildVersion bundles Netlify Functions (standalone binary, any Node.js).
	EsbuildVersion = "0.28.2"
	// DefaultFunctionsDir is where Netlify looks for functions by default.
	DefaultFunctionsDir = "netlify/functions"
	// ServerPort is where the runtime server listens inside the container.
	ServerPort = 8080
	// Dir is the directory Shipyard writes its generated files to, inside the build context.
	Dir = ".shipyard"
)

// ---- netlify.toml ----

type file struct {
	Build         settings            `toml:"build"`
	Context       map[string]settings `toml:"context"`
	Redirects     []tomlRedirect      `toml:"redirects"`
	Headers       []tomlHeader        `toml:"headers"`
	Functions     map[string]any      `toml:"functions"`
	EdgeFunctions []map[string]any    `toml:"edge_functions"`
	Plugins       []struct {
		Package string `toml:"package"`
	} `toml:"plugins"`
}

type settings struct {
	Base          string         `toml:"base"`
	Publish       string         `toml:"publish"`
	Command       string         `toml:"command"`
	Functions     string         `toml:"functions"`
	EdgeFunctions string         `toml:"edge_functions"`
	Environment   map[string]any `toml:"environment"`
}

type tomlRedirect struct {
	From       string            `toml:"from"`
	To         string            `toml:"to"`
	Status     int               `toml:"status"`
	Force      bool              `toml:"force"`
	Query      map[string]string `toml:"query"`
	Conditions map[string]any    `toml:"conditions"`
	Headers    map[string]string `toml:"headers"`
	Signed     string            `toml:"signed"`
}

type tomlHeader struct {
	For    string         `toml:"for"`
	Values map[string]any `toml:"values"`
}

// Redirect and Header are the JSON rules read by the runtime server.
type Redirect struct {
	From       string              `json:"from"`
	To         string              `json:"to"`
	Status     int                 `json:"status"`
	Force      bool                `json:"force"`
	Query      map[string]string   `json:"query,omitempty"`
	Conditions map[string][]string `json:"conditions,omitempty"`
	Headers    map[string]string   `json:"headers,omitempty"`
}

type Header struct {
	For    string            `json:"for"`
	Values map[string]string `json:"values"`
}

// Plan is everything needed to build a Netlify site, resolved for the
// production context.
type Plan struct {
	Base        string            `json:"base"`    // relative to the repository root
	Publish     string            `json:"publish"` // relative to Base
	Command     string            `json:"command"`
	Environment map[string]string `json:"environment"` // from netlify.toml
	NodeVersion string            `json:"nodeVersion"`
	HugoVersion string            `json:"hugoVersion,omitempty"`
	GoVersion   string            `json:"goVersion,omitempty"`
	Ruby        bool              `json:"ruby"`
	Python      bool              `json:"python"`
	Redirects   []Redirect        `json:"redirects"`
	Headers     []Header          `json:"headers"`
	// FunctionsDir is the configured functions directory (relative to Base).
	FunctionsDir string     `json:"functionsDir,omitempty"`
	Functions    []Function `json:"functions"`
	Warnings     []string   `json:"warnings"`

	fnConfig functionsConfig
}

// Function is a Netlify Function found in the functions directory.
type Function struct {
	Name     string   `json:"name"`
	Entry    string   `json:"entry"` // relative to Base
	Schedule string   `json:"schedule,omitempty"`
	External []string `json:"external,omitempty"`
	Included []string `json:"included,omitempty"`
}

// functionsConfig is the [functions] table: global settings plus
// per-function blocks keyed by name or glob ([functions."api-*"]).
type functionsConfig struct {
	Directory string
	External  []string
	Included  []string
	ByPattern map[string]functionsConfig
	Schedule  string
}

// Parse decodes netlify.toml content, resolving the production context. Tool
// versions are only resolved by Load, which sees the repository files.
func Parse(data []byte) (*Plan, error) {
	p, _, err := parse(data)
	return p, err
}

func parse(data []byte) (*Plan, *file, error) {
	var f file
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, nil, fmt.Errorf("netlify.toml: %w", err)
	}
	s := f.Build
	if prod, ok := f.Context["production"]; ok {
		if prod.Base != "" {
			s.Base = prod.Base
		}
		if prod.Publish != "" {
			s.Publish = prod.Publish
		}
		if prod.Command != "" {
			s.Command = prod.Command
		}
		if prod.Functions != "" {
			s.Functions = prod.Functions
		}
		env := map[string]any{}
		for k, v := range s.Environment {
			env[k] = v
		}
		for k, v := range prod.Environment {
			env[k] = v
		}
		s.Environment = env
	}

	p := &Plan{
		Base:        s.Base,
		Publish:     s.Publish,
		Command:     strings.TrimSpace(s.Command),
		Environment: map[string]string{},
		Redirects:   []Redirect{},
		Headers:     []Header{},
		Functions:   []Function{},
		Warnings:    []string{},
	}
	p.fnConfig = parseFunctionsConfig(f.Functions)
	p.FunctionsDir = p.fnConfig.Directory
	if p.FunctionsDir == "" {
		p.FunctionsDir = s.Functions
	}
	for k, v := range s.Environment {
		p.Environment[k] = fmt.Sprint(v)
	}

	for _, r := range f.Redirects {
		if r.From == "" || r.To == "" {
			continue
		}
		out := Redirect{From: r.From, To: r.To, Status: r.Status, Force: r.Force, Query: r.Query, Headers: r.Headers}
		if out.Status == 0 {
			out.Status = 301
		}
		if r.Signed != "" {
			p.warn("Redirect %s → %s: signed proxy requests are not supported (sent unsigned)", r.From, r.To)
		}
		if len(r.Conditions) > 0 {
			out.Conditions = map[string][]string{}
			for k, v := range r.Conditions {
				out.Conditions[k] = toStrings(v)
				switch strings.ToLower(k) {
				case "language", "cookie":
				default:
					p.warn("Redirect %s → %s uses the %s condition, which only Netlify's edge can evaluate: the rule is ignored", r.From, r.To, k)
				}
			}
		}
		p.Redirects = append(p.Redirects, out)
	}
	for _, h := range f.Headers {
		if h.For == "" {
			continue
		}
		vals := map[string]string{}
		for k, v := range h.Values {
			vals[k] = headerValue(v)
		}
		p.Headers = append(p.Headers, Header{For: h.For, Values: vals})
	}

	if len(f.EdgeFunctions) > 0 || s.EdgeFunctions != "" {
		p.warn("Netlify Edge Functions are not supported and are ignored")
	}
	for _, pl := range f.Plugins {
		if pl.Package == "@netlify/plugin-nextjs" {
			p.warn("@netlify/plugin-nextjs: this is a server-rendered Next.js app; use the Next.js template instead of Netlify mode")
		} else {
			p.warn("Build plugin %s is not run", pl.Package)
		}
	}
	return p, &f, nil
}

// Load reads netlify.toml from dir (relative to repoDir, Netlify's "base
// directory" setting) and resolves tool versions from the repository files.
// extraEnv holds the project's environment variables (Netlify UI variables
// such as NODE_VERSION or HUGO_VERSION live there after a migration).
func Load(repoDir, dir string, extraEnv map[string]string) (*Plan, error) {
	if dir == "" {
		dir = "."
	}
	if err := relative(dir); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(dir), "netlify.toml"))
	if err != nil {
		return nil, fmt.Errorf("no netlify.toml found in %s", dir)
	}
	p, f, err := parse(data)
	if err != nil {
		return nil, err
	}
	if p.Base == "" {
		p.Base = dir
	}
	p.Base = path.Clean(strings.TrimPrefix(p.Base, "/"))
	if p.Publish == "" {
		p.Publish = "."
	}
	p.Publish = path.Clean(strings.TrimPrefix(p.Publish, "/"))
	// Netlify accepts publish paths that already include the base directory.
	if p.Base != "." && strings.HasPrefix(p.Publish, p.Base+"/") {
		p.Publish = strings.TrimPrefix(p.Publish, p.Base+"/")
	}
	for _, rel := range []string{p.Base, p.Publish} {
		if err := relative(rel); err != nil {
			return nil, err
		}
	}
	baseDir := filepath.Join(repoDir, filepath.FromSlash(p.Base))
	if st, err := os.Stat(baseDir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("base directory %q does not exist", p.Base)
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(baseDir, name))
		return err == nil
	}

	env := map[string]string{}
	for k, v := range extraEnv {
		env[k] = v
	}
	for k, v := range p.Environment {
		env[k] = v // netlify.toml wins over UI variables, as on Netlify
	}

	// Node.js is always present, like in Netlify's build image.
	p.NodeVersion = env["NODE_VERSION"]
	for _, name := range []string{".nvmrc", ".node-version"} {
		if p.NodeVersion != "" {
			break
		}
		for _, d := range []string{baseDir, repoDir} {
			if b, err := os.ReadFile(filepath.Join(d, name)); err == nil {
				p.NodeVersion = strings.TrimSpace(string(b))
				break
			}
		}
	}
	if p.NodeVersion == "" {
		p.NodeVersion = DefaultNodeVersion
	}
	if tag := nodeTag(p.NodeVersion); tag == "" {
		p.warn("Unsupported Node.js version %q, using %s", p.NodeVersion, DefaultNodeVersion)
		p.NodeVersion = DefaultNodeVersion
	}

	usesHugo := regexp.MustCompile(`(^|[\s;&|(])hugo(\s|$|;|&)`).MatchString(p.Command)
	if v := env["HUGO_VERSION"]; v != "" {
		p.HugoVersion = strings.TrimPrefix(v, "v")
	} else if usesHugo {
		p.HugoVersion = DefaultHugoVersion
		p.warn("HUGO_VERSION is not set, using Hugo %s (Netlify's default is much older: set it to the version you use)", DefaultHugoVersion)
	}
	if p.HugoVersion != "" && !versionRe.MatchString(p.HugoVersion) {
		return nil, fmt.Errorf("invalid HUGO_VERSION %q", p.HugoVersion)
	}

	usesGo := regexp.MustCompile(`(^|[\s;&|(])go\s`).MatchString(p.Command)
	if v := env["GO_VERSION"]; v != "" {
		p.GoVersion = strings.TrimPrefix(v, "go")
	} else if usesGo || (p.HugoVersion != "" && exists("go.mod")) { // Hugo modules need Go
		p.GoVersion = DefaultGoVersion
	}
	if p.GoVersion != "" && !versionRe.MatchString(p.GoVersion) {
		return nil, fmt.Errorf("invalid GO_VERSION %q", p.GoVersion)
	}

	p.Ruby = exists("Gemfile") || env["RUBY_VERSION"] != ""
	p.Python = exists("requirements.txt") || exists("Pipfile") || env["PYTHON_VERSION"] != ""
	if env["RUBY_VERSION"] != "" || env["PYTHON_VERSION"] != "" {
		p.warn("RUBY_VERSION / PYTHON_VERSION are ignored: Debian's Ruby and Python packages are used")
	}
	if p.Command == "" {
		p.warn("No build command: the %q directory is published as-is", p.Publish)
	}
	_ = f
	if err := p.discoverFunctions(baseDir); err != nil {
		return nil, err
	}
	p.Warnings = dedupe(p.Warnings)
	return p, nil
}

func parseFunctionsConfig(raw map[string]any) functionsConfig {
	c := functionsConfig{ByPattern: map[string]functionsConfig{}}
	for k, v := range raw {
		switch k {
		case "directory":
			c.Directory = fmt.Sprint(v)
		case "external_node_modules":
			c.External = toStrings(v)
		case "included_files":
			c.Included = toStrings(v)
		case "schedule":
			c.Schedule = fmt.Sprint(v)
		default:
			if sub, ok := v.(map[string]any); ok {
				c.ByPattern[k] = parseFunctionsConfig(sub)
			}
		}
	}
	return c
}

var (
	functionExts = []string{".mjs", ".js", ".cjs", ".mts", ".ts", ".cts", ".tsx", ".jsx"}
	functionName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// discoverFunctions lists functions with Netlify's rules: <dir>/<name>.<ext>,
// <dir>/<name>/<name>.<ext> or <dir>/<name>/index.<ext>.
func (p *Plan) discoverFunctions(baseDir string) error {
	dir := p.FunctionsDir
	explicit := dir != ""
	if !explicit {
		dir = DefaultFunctionsDir
	}
	dir = path.Clean(strings.TrimPrefix(dir, "/"))
	if err := relative(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(baseDir, filepath.FromSlash(dir)))
	if err != nil {
		if explicit {
			p.warn("Functions directory %q not found: no functions deployed", dir)
		}
		return nil
	}
	p.FunctionsDir = dir
	seen := map[string]string{}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") {
			continue
		}
		var name, entry string
		if e.IsDir() {
			name = n
			sub := filepath.Join(baseDir, filepath.FromSlash(dir), n)
			for _, cand := range []string{n, "index"} {
				for _, ext := range functionExts {
					if entry == "" && isFile(filepath.Join(sub, cand+ext)) {
						entry = path.Join(dir, n, cand+ext)
					}
				}
			}
			if entry == "" {
				if isFile(filepath.Join(sub, "main.go")) || isFile(filepath.Join(sub, "Cargo.toml")) {
					p.warn("Function %q is written in Go/Rust: only JavaScript and TypeScript functions are supported", n)
				}
				continue
			}
		} else {
			ext := path.Ext(n)
			if !slices.Contains(functionExts, ext) {
				continue
			}
			name, entry = strings.TrimSuffix(n, ext), path.Join(dir, n)
		}
		if !functionName.MatchString(name) {
			p.warn("Function %q skipped: names may only contain letters, digits, - and _", name)
			continue
		}
		if prev, dup := seen[name]; dup {
			p.warn("Function %q is defined twice (%s, %s): keeping the first", name, prev, entry)
			continue
		}
		seen[name] = entry
		fn := Function{Name: name, Entry: entry, Schedule: p.fnConfig.Schedule,
			External: append([]string{}, p.fnConfig.External...), Included: append([]string{}, p.fnConfig.Included...)}
		for pattern, c := range p.fnConfig.ByPattern {
			if ok, _ := path.Match(pattern, name); ok {
				if c.Schedule != "" {
					fn.Schedule = c.Schedule
				}
				fn.External = append(fn.External, c.External...)
				fn.Included = append(fn.Included, c.Included...)
			}
		}
		fn.External, fn.Included = dedupe(fn.External), dedupe(fn.Included)
		p.Functions = append(p.Functions, fn)
	}
	sort.Slice(p.Functions, func(i, j int) bool { return p.Functions[i].Name < p.Functions[j].Name })
	return nil
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Meta describes the deployment, exposed to the build like Netlify's
// read-only variables (CONTEXT, BRANCH, COMMIT_REF…).
type Meta struct {
	Branch, Commit, DeployID, SiteName string
}

// Write generates the build files under <contextDir>/.shipyard and returns the Dockerfile.
func Write(p *Plan, contextDir string, meta Meta) (string, error) {
	out := filepath.Join(contextDir, Dir)
	if err := os.MkdirAll(filepath.Join(out, "server"), 0o755); err != nil {
		return "", err
	}
	for _, name := range []string{"main.go", "rules.go", "functions.go"} {
		src, err := embedded.ReadFile("server/" + name)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(out, "server", name), src, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(out, "server", "go.mod"), []byte("module netlifyserver\n\ngo 1.22\n"), 0o644); err != nil {
		return "", err
	}
	if len(p.Functions) > 0 {
		if err := os.MkdirAll(filepath.Join(out, "functions"), 0o755); err != nil {
			return "", err
		}
		runtime, err := embedded.ReadFile("functions/runtime.mjs")
		if err != nil {
			return "", err
		}
		type entry struct {
			Name     string `json:"name"`
			File     string `json:"file"`
			Schedule string `json:"schedule,omitempty"`
		}
		manifest := struct {
			Functions []entry `json:"functions"`
		}{}
		for _, fn := range p.Functions {
			manifest.Functions = append(manifest.Functions, entry{fn.Name, fn.Name + ".mjs", fn.Schedule})
		}
		mb, _ := json.MarshalIndent(manifest, "", "  ")
		for name, data := range map[string][]byte{"runtime.mjs": runtime, "manifest.json": mb} {
			if err := os.WriteFile(filepath.Join(out, "functions", name), data, 0o644); err != nil {
				return "", err
			}
		}
	}
	rules, _ := json.MarshalIndent(map[string]any{"redirects": p.Redirects, "headers": p.Headers}, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "netlify.json"), rules, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(out, "build.sh"), []byte(buildScript(p, meta)), 0o755); err != nil {
		return "", err
	}
	// Without a .dockerignore, keep local dependency folders out of the context
	// (but keep .git: Hugo's enableGitInfo and many build scripts read it).
	ignore := filepath.Join(contextDir, ".dockerignore")
	if _, err := os.Stat(ignore); os.IsNotExist(err) {
		_ = os.WriteFile(ignore, []byte("**/node_modules\n"), 0o644)
	}
	return dockerfile(p, meta), nil
}

func buildScript(p *Plan, m Meta) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n# Generated by Shipyard from netlify.toml.\nset -eo pipefail\n\n")
	b.WriteString("# Netlify's read-only variables.\n")
	for _, kv := range [][2]string{
		{"NETLIFY", "true"}, {"CONTEXT", "production"}, {"BRANCH", m.Branch}, {"HEAD", m.Branch},
		{"COMMIT_REF", m.Commit}, {"BUILD_ID", m.DeployID}, {"DEPLOY_ID", m.DeployID}, {"SITE_NAME", m.SiteName},
	} {
		fmt.Fprintf(&b, "export %s=%s\n", kv[0], shellQuote(kv[1]))
	}
	b.WriteString("\n# Project environment variables (BuildKit secret, never stored in the image).\n")
	b.WriteString("if [ -f /run/secrets/buildenv ]; then set -a; . /run/secrets/buildenv; set +a; fi\n\n")
	if len(p.Environment) > 0 {
		b.WriteString("# [build.environment] from netlify.toml.\n")
		for _, k := range sortedKeys(p.Environment) {
			if envName.MatchString(k) {
				fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(p.Environment[k]))
			}
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "cd %s\n\n", shellQuote(path.Join("/repo", p.Base)))
	b.WriteString(`if [ -f package.json ]; then
  echo "==> Installing Node.js dependencies"
  # Same commands and *_FLAGS variables as Netlify's build image.
  if [ -f pnpm-lock.yaml ]; then (corepack enable pnpm || npm i -g pnpm) && pnpm install $PNPM_FLAGS
  elif [ -f yarn.lock ]; then (corepack enable yarn || npm i -g yarn) && yarn install $YARN_FLAGS
  elif [ -f bun.lockb ] || [ -f bun.lock ]; then npm i -g bun && bun install $BUN_FLAGS
  else npm install $NPM_FLAGS; fi
fi
if [ -f Gemfile ]; then echo "==> Installing Ruby gems"; bundle install; fi
if [ -f requirements.txt ]; then echo "==> Installing Python packages"; pip3 install --break-system-packages -r requirements.txt 2>/dev/null || pip3 install -r requirements.txt; fi

`)
	if p.Command != "" {
		fmt.Fprintf(&b, "echo %s\n%s\n\n", shellQuote("==> Running build command: "+p.Command), p.Command)
	}
	if len(p.Functions) > 0 {
		fmt.Fprintf(&b, "echo %s\nmkdir -p /out-app/.netlify-functions\n", shellQuote(fmt.Sprintf("==> Bundling %d Netlify Function(s)", len(p.Functions))))
		var external, included []string
		for _, fn := range p.Functions {
			args := []string{"esbuild", shellQuote(fn.Entry), "--bundle", "--platform=node", "--format=esm",
				"--target=" + esbuildTarget(p.NodeVersion), "--log-level=warning",
				"--outfile=" + shellQuote("/out-app/.netlify-functions/"+fn.Name+".mjs")}
			for _, m := range fn.External {
				args = append(args, shellQuote("--external:"+m))
			}
			fmt.Fprintf(&b, "echo '  %s'\n%s\n", fn.Name, strings.Join(args, " "))
			external = append(external, fn.External...)
			included = append(included, fn.Included...)
		}
		if len(dedupe(external)) > 0 {
			b.WriteString("# external_node_modules are loaded at runtime from node_modules.\nif [ -d node_modules ]; then cp -a node_modules /out-app/; fi\n")
		}
		if inc := dedupe(included); len(inc) > 0 {
			b.WriteString("# included_files, relative to the base directory.\nshopt -s globstar nullglob dotglob\n")
			for _, g := range inc {
				if strings.HasPrefix(g, "!") {
					continue
				}
				fmt.Fprintf(&b, "for f in %s; do cp -a --parents \"$f\" /out-app/; done\n", globWords(g))
			}
		}
		b.WriteString("\n")
	}
	pub := shellQuote(p.Publish)
	fmt.Fprintf(&b, `if [ ! -d %[1]s ]; then echo "Publish directory %[2]s not found after the build" >&2; exit 1; fi
mkdir -p /out
(cd %[1]s && tar --exclude=./node_modules --exclude=./.git --exclude=./%[3]s -cf - .) | tar -xf - -C /out
echo "==> Published $(find /out -type f | wc -l) files from %[2]s"
`, pub, p.Publish, Dir)
	return b.String()
}

func dockerfile(p *Plan, meta Meta) string {
	var b strings.Builder
	b.WriteString("# syntax=docker/dockerfile:1\n# Generated by Shipyard from netlify.toml.\n\n")
	b.WriteString(`FROM golang:1.26-alpine AS server
WORKDIR /src
COPY ` + Dir + `/server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /netlify-server .

`)
	// Like Netlify's build image, one current OS with the requested Node.js
	// installed into it: old node:* images sit on Debian releases whose
	// package mirrors have been retired.
	fmt.Fprintf(&b, "FROM node:%s AS node\n\n", nodeTag(p.NodeVersion))
	b.WriteString("FROM debian:bookworm-slim AS build\nARG TARGETARCH\n")
	pkgs := []string{"ca-certificates", "curl", "git", "xz-utils"}
	if p.Ruby {
		pkgs = append(pkgs, "ruby-full", "build-essential", "zlib1g-dev")
	}
	if p.Python {
		pkgs = append(pkgs, "python3", "python3-pip", "python3-venv")
	}
	fmt.Fprintf(&b, "RUN apt-get update && apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*\n", strings.Join(pkgs, " "))
	b.WriteString("COPY --from=node /usr/local/ /usr/local/\nCOPY --from=node /opt/ /opt/\nRUN node --version && npm --version\n")
	if p.Python {
		b.WriteString("RUN ln -sf /usr/bin/python3 /usr/local/bin/python && ln -sf /usr/bin/pip3 /usr/local/bin/pip\n")
	}
	if p.GoVersion != "" {
		fmt.Fprintf(&b, "COPY --from=golang:%s-bookworm /usr/local/go /usr/local/go\nENV PATH=/usr/local/go/bin:/root/go/bin:$PATH\n", p.GoVersion)
	}
	if p.HugoVersion != "" {
		fmt.Fprintf(&b, `RUN set -eux; v=%s; arch="${TARGETARCH:-amd64}"; \
    base="https://github.com/gohugoio/hugo/releases/download/v${v}"; \
    if ! curl -fsSL "${base}/hugo_extended_${v}_linux-${arch}.tar.gz" -o /tmp/hugo.tgz; then \
      legacy=$([ "$arch" = arm64 ] && echo ARM64 || echo 64bit); \
      curl -fsSL "${base}/hugo_extended_${v}_Linux-${legacy}.tar.gz" -o /tmp/hugo.tgz; \
    fi; \
    tar -xzf /tmp/hugo.tgz -C /usr/local/bin hugo; rm /tmp/hugo.tgz; hugo version
`, p.HugoVersion)
	}
	if len(p.Functions) > 0 {
		fmt.Fprintf(&b, `RUN arch=$([ "${TARGETARCH:-amd64}" = arm64 ] && echo arm64 || echo x64); \
    curl -fsSL "https://registry.npmjs.org/@esbuild/linux-${arch}/-/linux-${arch}-%[1]s.tgz" | tar -xz -C /tmp; \
    mv /tmp/package/bin/esbuild /usr/local/bin/esbuild && esbuild --version
`, EsbuildVersion)
	}
	b.WriteString(`WORKDIR /repo
COPY . .
RUN --mount=type=secret,id=buildenv \
    --mount=type=cache,target=/root/.npm \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/usr/local/share/.cache \
    --mount=type=cache,target=/root/.local/share/pnpm \
    bash /repo/` + Dir + `/build.sh

`)
	if len(p.Functions) == 0 {
		b.WriteString(`FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /netlify-server /netlify-server
COPY --from=build /out /site
COPY ` + Dir + `/netlify.json /netlify.json
`)
		fmt.Fprintf(&b, "EXPOSE %d\nENTRYPOINT [\"/netlify-server\", \"-addr\", \":%d\", \"-root\", \"/site\", \"-config\", \"/netlify.json\"]\n", ServerPort, ServerPort)
		return b.String()
	}
	// With functions, the runtime image is Node.js (same version as the build).
	b.WriteString(`FROM node AS runtime
COPY --from=server /netlify-server /usr/local/bin/netlify-server
COPY --from=build /out /site
COPY --from=build /out-app /app
COPY ` + Dir + `/functions/ /app/.netlify-functions/
COPY ` + Dir + `/netlify.json /netlify.json
WORKDIR /app
`)
	fmt.Fprintf(&b, "ENV NODE_ENV=production NETLIFY=true CONTEXT=production SITE_NAME=%s BRANCH=%s COMMIT_REF=%s DEPLOY_ID=%s BUILD_ID=%s\n",
		strconv.Quote(meta.SiteName), strconv.Quote(meta.Branch), strconv.Quote(meta.Commit), strconv.Quote(meta.DeployID), strconv.Quote(meta.DeployID))
	fmt.Fprintf(&b, "USER node\nEXPOSE %d\nENTRYPOINT [\"netlify-server\", \"-addr\", \":%d\", \"-root\", \"/site\", \"-config\", \"/netlify.json\", \"-functions\", \"/app/.netlify-functions/runtime.mjs\"]\n", ServerPort, ServerPort)
	return b.String()
}

// esbuildTarget maps the site's Node.js version to an esbuild target.
func esbuildTarget(v string) string {
	major, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if n, err := strconv.Atoi(major); err == nil && n >= 12 {
		return "node" + major
	}
	return "node18"
}

// globWords quotes a glob for bash while keeping its wildcards active.
func globWords(g string) string {
	var b strings.Builder
	for _, r := range g {
		switch {
		case strings.ContainsRune("*?[]", r):
			b.WriteRune(r)
		case r == '/' || r == '.' || r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteString("\\" + string(r))
		}
	}
	return b.String()
}

var (
	versionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]*$`)
	nodeRe    = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)
	envName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// nodeTag maps nvm-style versions ("v20.11.1", "lts/*", "lts/iron", "node")
// to a node Docker image tag; "" means unsupported.
func nodeTag(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	switch {
	case v == "lts/*" || v == "lts":
		v = "lts"
	case strings.HasPrefix(v, "lts/"):
		v = strings.ToLower(strings.TrimPrefix(v, "lts/"))
		if !regexp.MustCompile(`^[a-z]+$`).MatchString(v) {
			return ""
		}
	case v == "node" || v == "latest" || v == "current" || v == "stable":
		v = "current"
	case nodeRe.MatchString(v):
	default:
		return ""
	}
	// Node.js 14 and older predate Debian bookworm images.
	major, _, _ := strings.Cut(v, ".")
	if n, err := strconv.Atoi(major); (err == nil && n < 16) || v == "fermium" || v == "erbium" || v == "dubnium" {
		return v + "-bullseye-slim"
	}
	return v + "-bookworm-slim"
}

func (p *Plan) warn(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

func relative(p string) error {
	c := path.Clean(strings.TrimPrefix(p, "/"))
	if c == ".." || strings.HasPrefix(c, "../") {
		return fmt.Errorf("path %q escapes the repository", p)
	}
	return nil
}

func toStrings(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, fmt.Sprint(x))
		}
		return out
	case string:
		return strings.Split(t, ",")
	default:
		return []string{fmt.Sprint(t)}
	}
}

// headerValue flattens multi-line TOML header values ("\" continuations).
func headerValue(v any) string {
	s := strings.ReplaceAll(fmt.Sprint(v), "\\\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// BuildEnvFile renders environment variables for `. /run/secrets/buildenv`.
func BuildEnvFile(env map[string]string) string {
	var b strings.Builder
	for _, k := range sortedKeys(env) {
		if envName.MatchString(k) {
			fmt.Fprintf(&b, "%s=%s\n", k, shellQuote(env[k]))
		}
	}
	return b.String()
}
