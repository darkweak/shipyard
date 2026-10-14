package netlify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The netlify.toml of github.com/gohugoio/hugoDocs (abridged).
const hugoDocs = `
[build]
  publish = "public"
  command = "npm ls && hugo --gc --minify"
  [build.environment]
    HUGO_VERSION = "0.168.0"

[context.production.environment]
  HUGO_ENV           = "production"
  HUGO_ENABLEGITINFO = "true"

[context.deploy-preview]
  command = "hugo --buildFuture -b $DEPLOY_PRIME_URL"

[[headers]]
  for = "/*.jpg"
  [headers.values]
    Cache-Control = "public, max-age=31536000"

[[redirects]]
  from = "/old/*"
  to = "/new/:splat"

[[redirects]]
  from = "/api/*"
  to = "https://api.example.com/:splat"
  status = 200
  force = true
  conditions = {Country = ["US"]}
`

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadHugoDocs(t *testing.T) {
	dir := repo(t, map[string]string{"netlify.toml": hugoDocs, "go.mod": "module x", "package.json": "{}"})
	p, err := Load(dir, ".", map[string]string{"NODE_VERSION": "20", "HUGO_VERSION": "0.100.0"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Base != "." || p.Publish != "public" || p.Command != "npm ls && hugo --gc --minify" {
		t.Errorf("settings: %+v", p)
	}
	if p.Environment["HUGO_ENV"] != "production" || p.Environment["HUGO_VERSION"] != "0.168.0" {
		t.Errorf("environment: %v", p.Environment)
	}
	// netlify.toml wins over project (Netlify UI) variables; NODE_VERSION comes from the project.
	if p.HugoVersion != "0.168.0" || p.NodeVersion != "20" || p.GoVersion != DefaultGoVersion {
		t.Errorf("versions: node=%s hugo=%s go=%s", p.NodeVersion, p.HugoVersion, p.GoVersion)
	}
	if len(p.Redirects) != 2 || p.Redirects[0].Status != 301 || p.Redirects[1].Conditions["Country"][0] != "US" {
		t.Errorf("redirects: %+v", p.Redirects)
	}
	if len(p.Headers) != 1 || p.Headers[0].Values["Cache-Control"] != "public, max-age=31536000" {
		t.Errorf("headers: %+v", p.Headers)
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "Country") {
		t.Errorf("expected a warning about the Country condition: %v", p.Warnings)
	}

	df, err := Write(p, dir, Meta{Branch: "master", Commit: "abc", DeployID: "d1", SiteName: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FROM node:20-bookworm-slim AS node", "FROM debian:bookworm-slim AS build", "COPY --from=node /usr/local/ /usr/local/", "v=0.168.0", "COPY --from=golang:1.26-bookworm", "/netlify-server"} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile lacks %q:\n%s", want, df)
		}
	}
	script, _ := os.ReadFile(filepath.Join(dir, Dir, "build.sh"))
	for _, want := range []string{"export HUGO_ENV='production'", "export COMMIT_REF='abc'", "npm ls && hugo --gc --minify", "cd '/repo'"} {
		if !strings.Contains(string(script), want) {
			t.Errorf("build.sh lacks %q:\n%s", want, script)
		}
	}
	for _, f := range []string{"server/main.go", "server/rules.go", "server/go.mod", "netlify.json"} {
		if _, err := os.Stat(filepath.Join(dir, Dir, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
}

func TestLoadBaseDirectoryAndNvmrc(t *testing.T) {
	dir := repo(t, map[string]string{
		"netlify.toml":     "[build]\nbase = \"site\"\npublish = \"site/dist\"\ncommand = \"npm run build\"\n[[plugins]]\npackage = \"netlify-plugin-cache\"\n",
		"site/.nvmrc":      "lts/iron\n",
		"site/Gemfile":     "",
		"site/netlify.txt": "",
	})
	p, err := Load(dir, ".", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Base != "site" || p.Publish != "dist" || p.NodeVersion != "lts/iron" || !p.Ruby || p.HugoVersion != "" {
		t.Errorf("plan: %+v", p)
	}
	if nodeTag(p.NodeVersion) != "iron-bookworm-slim" {
		t.Errorf("tag %q", nodeTag(p.NodeVersion))
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "netlify-plugin-cache") {
		t.Errorf("warnings: %v", p.Warnings)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(repo(t, nil), ".", nil); err == nil {
		t.Error("missing netlify.toml must fail")
	}
	if _, err := Load(repo(t, map[string]string{"netlify.toml": "[build]\npublish = \"../../etc\"\n"}), ".", nil); err == nil {
		t.Error("escaping publish must fail")
	}
	if _, err := Load(repo(t, map[string]string{"netlify.toml": "[build\n"}), ".", nil); err == nil {
		t.Error("invalid TOML must fail")
	}
}

func TestNodeTag(t *testing.T) {
	for in, want := range map[string]string{"v18.17.0": "18.17.0-bookworm-slim", "20": "20-bookworm-slim", "lts/*": "lts-bookworm-slim", "node": "current-bookworm-slim", "14": "14-bullseye-slim", "v12.22.1": "12.22.1-bullseye-slim", "lts/fermium": "fermium-bullseye-slim", "18; rm -rf /": ""} {
		if got := nodeTag(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestBuildEnvFileQuoting(t *testing.T) {
	got := BuildEnvFile(map[string]string{"A": "it's", "B": "$HOME", "bad-key": "x"})
	if got != "A='it'\\''s'\nB='$HOME'\n" {
		t.Errorf("got %q", got)
	}
}

func TestFunctionsDiscoveryAndConfig(t *testing.T) {
	dir := repo(t, map[string]string{
		"netlify.toml": `[build]
command = "npm run build"
publish = "dist"
[functions]
external_node_modules = ["sharp"]
included_files = ["data/**"]
[functions."report"]
schedule = "@daily"
[functions."api-*"]
external_node_modules = ["pg-native"]
`,
		"netlify/functions/hello.ts":            "export default async () => new Response('hi')",
		"netlify/functions/api-users/index.mjs": "",
		"netlify/functions/report/report.js":    "",
		"netlify/functions/_shared.ts":          "",
		"netlify/functions/README.md":           "",
		"netlify/functions/gofn/main.go":        "",
		"netlify/functions/bad name.js":         "",
	})
	p, err := Load(dir, ".", map[string]string{"NODE_VERSION": "20.11.0"})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, f := range p.Functions {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "api-users,hello,report" {
		t.Fatalf("functions: %v", names)
	}
	api, hello, report := p.Functions[0], p.Functions[1], p.Functions[2]
	if api.Entry != "netlify/functions/api-users/index.mjs" || strings.Join(api.External, ",") != "sharp,pg-native" {
		t.Errorf("api-users: %+v", api)
	}
	if report.Schedule != "@daily" || hello.Schedule != "" || hello.Included[0] != "data/**" {
		t.Errorf("config: %+v %+v", report, hello)
	}
	warnings := strings.Join(p.Warnings, "\n")
	if !strings.Contains(warnings, "gofn") || !strings.Contains(warnings, "bad name") {
		t.Errorf("warnings: %v", p.Warnings)
	}

	df, err := Write(p, dir, Meta{Branch: "main", Commit: "c0ffee", DeployID: "d1", SiteName: "site"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"linux-${arch}-" + EsbuildVersion, "FROM node AS runtime", `"-functions", "/app/.netlify-functions/runtime.mjs"`, `COMMIT_REF="c0ffee"`} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile lacks %q:\n%s", want, df)
		}
	}
	script, _ := os.ReadFile(filepath.Join(dir, Dir, "build.sh"))
	for _, want := range []string{"esbuild 'netlify/functions/hello.ts' --bundle", "--target=node20", "'--external:pg-native'", "cp -a node_modules /out-app/", "for f in data/**; do"} {
		if !strings.Contains(string(script), want) {
			t.Errorf("build.sh lacks %q:\n%s", want, script)
		}
	}
	manifest, _ := os.ReadFile(filepath.Join(dir, Dir, "functions", "manifest.json"))
	if !strings.Contains(string(manifest), `"schedule": "@daily"`) {
		t.Errorf("manifest: %s", manifest)
	}
	if _, err := os.Stat(filepath.Join(dir, Dir, "functions", "runtime.mjs")); err != nil {
		t.Error("runtime.mjs not written")
	}
}
