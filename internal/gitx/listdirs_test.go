package gitx

import (
	"context"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// serveRepo publishes a fresh repository over git's smart HTTP protocol.
func serveRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	work, root := t.TempDir(), t.TempDir()
	for name, content := range files {
		p := filepath.Join(work, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(dir string, args ...string) {
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run(work, "init", "-q", "-b", "main")
	run(work, "add", "-A")
	run(work, "commit", "-qm", "init")
	run(work, "clone", "-q", "--bare", ".", filepath.Join(root, "repo.git"))
	run(filepath.Join(root, "repo.git"), "config", "uploadpack.allowFilter", "true")
	srv := httptest.NewServer(&cgi.Handler{Path: git, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(srv.Close)
	return srv.URL + "/repo.git"
}

func TestListDirsAndReadFiles(t *testing.T) {
	url := serveRepo(t, map[string]string{
		"README.md":                  "x",
		"sites/blog/netlify.toml":    "[build]\npublish = \"public\"\n",
		"sites/blog/hugo.toml":       "",
		"sites/blog/content/post.md": "",
		"apps/api/package.json":      "{}",
		"apps/api/Dockerfile":        "FROM scratch",
		"apps/api/src/index.ts":      "",
		"netlify/functions/hello.ts": "",
	})
	c := &Client{KnownHosts: filepath.Join(t.TempDir(), "kh"), TempDir: t.TempDir()}
	dirs, truncated, err := c.ListDirs(context.Background(), url, "main", Auth{Type: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range dirs {
		got[d.Path] = strings.Join(d.Markers, ",")
	}
	want := map[string]string{
		".": "", "apps": "", "apps/api": "Dockerfile,package.json", "apps/api/src": "",
		"netlify": "", "netlify/functions": "", "sites": "", "sites/blog": "hugo.toml,netlify.toml", "sites/blog/content": "",
	}
	if truncated || len(got) != len(want) {
		t.Fatalf("dirs: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: markers %q, want %q", k, got[k], v)
		}
	}
	if dirs[0].Path != "." {
		t.Errorf("root must come first: %v", dirs[0])
	}

	files, err := c.ReadFiles(context.Background(), url, "main", Auth{Type: AuthNone}, []string{"sites/blog/netlify.toml", "missing"})
	if err != nil || !strings.Contains(string(files["sites/blog/netlify.toml"]), "publish") || files["missing"] != nil {
		t.Fatalf("ReadFiles: %v %v", files, err)
	}
	if _, _, err := c.ListDirs(context.Background(), url, "nope", Auth{Type: AuthNone}); err == nil {
		t.Error("unknown branch must fail")
	}
}
