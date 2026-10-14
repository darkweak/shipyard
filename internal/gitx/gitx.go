// Package gitx wraps the git CLI for cloning repositories with per-project
// credentials (HTTPS token or SSH deploy key).
package gitx

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	AuthNone  = "none"
	AuthToken = "token"
	AuthSSH   = "ssh"
)

type Auth struct {
	Type     string
	Username string
	Secret   string
}

type Client struct {
	// KnownHosts is the known_hosts file used for SSH remotes. New hosts are
	// trusted on first use and pinned afterwards.
	KnownHosts string
	// TempDir holds short-lived SSH key files.
	TempDir string
}

var (
	scpLike   = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^-].*$`)
	branchRef = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// ValidateURL accepts https://, http://, ssh:// and scp-like (git@host:path) remotes.
func ValidateURL(u string) error {
	switch {
	case u == "":
		return fmt.Errorf("repository URL is required")
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"), strings.HasPrefix(u, "ssh://"):
		return nil
	case scpLike.MatchString(u):
		return nil
	}
	return fmt.Errorf("unsupported repository URL %q (use https://, ssh:// or git@host:owner/repo.git)", u)
}

func IsSSHURL(u string) bool {
	return strings.HasPrefix(u, "ssh://") || scpLike.MatchString(u)
}

func ValidateBranch(b string) error {
	if b == "" || strings.HasPrefix(b, "-") || strings.Contains(b, "..") || !branchRef.MatchString(b) {
		return fmt.Errorf("invalid branch name %q", b)
	}
	return nil
}

// env builds the environment for a git command. Credentials go through
// environment variables rather than argv or the URL so they never show up in
// `ps` output or in clone logs.
func (c *Client) env(auth Auth) ([]string, func(), error) {
	env := append(os.Environ(),
		// Ignore the host user's git config (credential helpers, insteadOf
		// rewrites…): a project's access must only depend on its own settings.
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=http:https:ssh",
	)
	cleanup := func() {}
	switch auth.Type {
	case AuthToken:
		user := auth.Username
		if user == "" {
			user = "x-access-token"
		}
		basic := base64.StdEncoding.EncodeToString([]byte(user + ":" + auth.Secret))
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic,
		)
	case AuthSSH:
		if err := os.MkdirAll(c.TempDir, 0o700); err != nil {
			return nil, nil, err
		}
		f, err := os.CreateTemp(c.TempDir, "key-*")
		if err != nil {
			return nil, nil, err
		}
		key := strings.ReplaceAll(auth.Secret, "\r\n", "\n")
		if !strings.HasSuffix(key, "\n") {
			key += "\n" // ssh refuses keys without a trailing newline
		}
		_, werr := f.WriteString(key)
		f.Close()
		if werr != nil {
			os.Remove(f.Name())
			return nil, nil, werr
		}
		cleanup = func() { os.Remove(f.Name()) }
		env = append(env, "GIT_SSH_COMMAND="+c.sshCommand(f.Name()))
	default:
		env = append(env, "GIT_SSH_COMMAND="+c.sshCommand(""))
	}
	return env, cleanup, nil
}

func (c *Client) sshCommand(keyFile string) string {
	cmd := fmt.Sprintf("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s",
		shellQuote(c.KnownHosts))
	if keyFile != "" {
		cmd += " -o IdentitiesOnly=yes -i " + shellQuote(keyFile)
	}
	return cmd
}

// Branches lists the remote branches, which doubles as an access check.
func (c *Client) Branches(ctx context.Context, url string, auth Auth) ([]string, error) {
	env, cleanup, err := c.env(auth)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", "--", url)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s", gitError(stderr.String(), err.Error()))
	}
	var branches []string
	sc := bufio.NewScanner(&stdout)
	for sc.Scan() {
		if _, ref, ok := strings.Cut(sc.Text(), "\t"); ok {
			branches = append(branches, strings.TrimPrefix(ref, "refs/heads/"))
		}
	}
	sort.Strings(branches)
	return branches, nil
}

// Clone makes a shallow clone of branch into dir, submodules included (Hugo
// themes are commonly vendored that way).
func (c *Client) Clone(ctx context.Context, url, branch, dir string, auth Auth, out io.Writer) error {
	env, cleanup, err := c.env(auth)
	if err != nil {
		return err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, "git", "clone", "--progress", "--depth", "1", "--single-branch",
		"--branch", branch, "--recurse-submodules", "--shallow-submodules", "--", url, dir)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}
	return nil
}

// partialClone makes a blobless, checkout-less shallow clone of branch: only
// commit and tree objects are downloaded. run executes git commands in it
// with the same credentials (missing blobs are fetched lazily).
func (c *Client) partialClone(ctx context.Context, url, branch string, auth Auth, fn func(run func(args ...string) ([]byte, error)) error) error {
	env, cleanup, err := c.env(auth)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := os.MkdirAll(c.TempDir, 0o700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(c.TempDir, "inspect-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	var stderr bytes.Buffer
	clone := exec.CommandContext(ctx, "git", "clone", "--quiet", "--depth", "1", "--filter=blob:none", "--no-checkout",
		"--single-branch", "--branch", branch, "--", url, dir)
	clone.Env, clone.Stderr = env, &stderr
	if err := clone.Run(); err != nil {
		return fmt.Errorf("%s", gitError(stderr.String(), err.Error()))
	}
	return fn(func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		return cmd.Output()
	})
}

// ReadFiles fetches a few files from the tip of branch without checking out
// the repository. Missing files are absent from the map.
func (c *Client) ReadFiles(ctx context.Context, url, branch string, auth Auth, paths []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := c.partialClone(ctx, url, branch, auth, func(run func(args ...string) ([]byte, error)) error {
		for _, p := range paths {
			if b, err := run("show", "HEAD:"+p); err == nil {
				out[p] = b
			}
		}
		return nil
	})
	return out, err
}

// Dir is a directory of the repository with the build files it contains.
type Dir struct {
	Path    string   `json:"path"` // "." for the root
	Markers []string `json:"markers"`
}

// markerFiles hint at what a directory is (shown next to it in the UI).
var markerFiles = map[string]bool{
	"netlify.toml": true, "package.json": true, "Dockerfile": true, "go.mod": true,
	"hugo.toml": true, "hugo.yaml": true, "hugo.json": true, "config.toml": true,
	"next.config.js": true, "next.config.mjs": true, "next.config.ts": true,
	"vite.config.ts": true, "vite.config.js": true, "astro.config.mjs": true,
	"Gemfile": true, "requirements.txt": true, "index.html": true,
}

// MaxDirs bounds the directory listing of very large repositories.
const MaxDirs = 5000

// ListDirs lists every directory on branch (no file contents downloaded).
func (c *Client) ListDirs(ctx context.Context, url, branch string, auth Auth) ([]Dir, bool, error) {
	var dirs []Dir
	truncated := false
	err := c.partialClone(ctx, url, branch, auth, func(run func(args ...string) ([]byte, error)) error {
		out, err := run("ls-tree", "-r", "-t", "-z", "HEAD")
		if err != nil {
			return fmt.Errorf("list tree: %w", err)
		}
		markers := map[string][]string{}
		var paths []string
		for _, entry := range strings.Split(string(out), "\x00") {
			meta, p, ok := strings.Cut(entry, "\t")
			if !ok {
				continue
			}
			fields := strings.Fields(meta) // <mode> <type> <object>
			if len(fields) < 2 {
				continue
			}
			switch fields[1] {
			case "tree":
				paths = append(paths, p)
			case "blob":
				parent, name := path.Split(p)
				if markerFiles[name] {
					parent = strings.TrimSuffix(parent, "/")
					if parent == "" {
						parent = "."
					}
					markers[parent] = append(markers[parent], name)
				}
			}
		}
		sort.Strings(paths)
		paths = append([]string{"."}, paths...)
		if len(paths) > MaxDirs {
			paths, truncated = paths[:MaxDirs], true
		}
		for _, p := range paths {
			m := markers[p]
			if m == nil {
				m = []string{}
			}
			sort.Strings(m)
			dirs = append(dirs, Dir{Path: p, Markers: m})
		}
		return nil
	})
	return dirs, truncated, err
}

// Head returns the checked out commit hash and subject.
func Head(ctx context.Context, dir string) (sha, subject string, err error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "log", "-1", "--format=%H%n%s")
	b, err := cmd.Output()
	if err != nil {
		return "", "", err
	}
	sha, subject, _ = strings.Cut(strings.TrimSpace(string(b)), "\n")
	return sha, subject, nil
}

// SafeJoin joins rel onto root and refuses paths escaping root.
func SafeJoin(root, rel string) (string, error) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	r, err := filepath.Rel(root, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the repository", rel)
	}
	return p, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// gitError condenses git's stderr, dropping ssh's known_hosts notices.
func gitError(stderr, fallback string) string {
	var parts []string
	for l := range strings.SplitSeq(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "Warning: Permanently added") {
			parts = append(parts, l)
		}
	}
	msg := strings.Join(parts, " ")
	switch {
	case msg == "":
		return fallback
	case strings.Contains(msg, "could not read Username"), strings.Contains(msg, "Authentication failed"):
		return "authentication failed: the repository is private or the token is invalid / lacks read access"
	case strings.Contains(msg, "Permission denied (publickey)"):
		return "SSH authentication failed: the key is not authorized for this repository"
	}
	return msg
}
