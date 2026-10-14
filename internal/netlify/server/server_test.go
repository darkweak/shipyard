package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSite(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func newTestSite(t *testing.T, files map[string]string, cfg Config) http.Handler {
	t.Helper()
	root := writeSite(t, files)
	b, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(t.TempDir(), "netlify.json")
	_ = os.WriteFile(cfgPath, b, 0o644)
	c, err := loadConfig(root, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return &site{root: root, cfg: c}
}

type resp struct {
	status   int
	body     string
	location string
	header   http.Header
}

func get(h http.Handler, target string, mod ...func(*http.Request)) resp {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, m := range mod {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return resp{rec.Code, string(b), rec.Header().Get("Location"), rec.Header()}
}

func TestPrettyURLsAnd404(t *testing.T) {
	h := newTestSite(t, map[string]string{
		"index.html":       "home",
		"about.html":       "about",
		"docs/index.html":  "docs",
		"404.html":         "custom 404",
		"assets/style.css": "body{}",
		"_redirects":       "/x /y",
	}, Config{})

	cases := []struct {
		path, body, location string
		status               int
	}{
		{"/", "home", "", 200},
		{"/about", "about", "", 200},
		{"/docs/", "docs", "", 200},
		{"/docs", "", "/docs/", 301},
		{"/assets/style.css", "body{}", "", 200},
		{"/nope", "custom 404", "", 404},
		{"/../../etc/passwd", "custom 404", "", 404},
		{"/_redirects", "custom 404", "", 404},
	}
	for _, c := range cases {
		r := get(h, c.path)
		if r.status != c.status || (c.body != "" && r.body != c.body) || r.location != c.location {
			t.Errorf("%s: got %d %q loc=%q, want %d %q loc=%q", c.path, r.status, r.body, r.location, c.status, c.body, c.location)
		}
	}
}

func TestRedirectRules(t *testing.T) {
	h := newTestSite(t, map[string]string{
		"index.html":  "spa",
		"real.html":   "real file",
		"_redirects":  "# comment\n/old-blog/*  /blog/:splat  301\n/real  /elsewhere  302\n/forced  /index.html  200!\n/store id=:id  /products/:id  301\n/temp  /x  302 Cookie=beta\n",
		"blog/a.html": "post a",
	}, Config{Redirects: []Redirect{
		{From: "/posts/:year/:slug", To: "/blog/:year-:slug", Status: 301},
		{From: "/real", To: "/never", Status: 301, Force: true},
		{From: "/geo", To: "/x", Status: 302, Conditions: map[string][]string{"Country": {"fr"}}},
		{From: "/*", To: "/index.html", Status: 200},
	}})

	cases := []struct {
		path, body, location string
		status               int
		mod                  func(*http.Request)
	}{
		{"/old-blog/2024/hello", "", "/blog/2024/hello", 301, nil},
		{"/old-blog/x?ref=tw", "", "/blog/x?ref=tw", 301, nil},
		{"/posts/2024/go", "", "/blog/2024-go", 301, nil},
		// /real exists: the non-forced _redirects rule is shadowed, the forced toml rule applies.
		{"/real", "", "/never", 301, nil},
		{"/forced", "spa", "", 200, nil},
		{"/store?id=42", "", "/products/42", 301, nil},
		{"/store", "spa", "", 200, nil}, // query param missing → falls through to the SPA rule
		{"/temp", "spa", "", 200, nil},
		{"/temp", "", "/x", 302, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "beta", Value: "1"}) }},
		{"/geo", "spa", "", 200, nil},               // Country can't be evaluated: rule skipped
		{"/some/client/route", "spa", "", 200, nil}, // SPA fallback
		{"/blog/a", "post a", "", 200, nil},         // existing file shadows the SPA rule
	}
	for _, c := range cases {
		var r resp
		if c.mod != nil {
			r = get(h, c.path, c.mod)
		} else {
			r = get(h, c.path)
		}
		if r.status != c.status || (c.body != "" && r.body != c.body) || r.location != c.location {
			t.Errorf("%s: got %d %q loc=%q, want %d %q loc=%q", c.path, r.status, r.body, r.location, c.status, c.body, c.location)
		}
	}
}

func TestCustom404Rewrite(t *testing.T) {
	h := newTestSite(t, map[string]string{"en/404.html": "en 404"}, Config{Redirects: []Redirect{
		{From: "/en/*", To: "/en/404.html", Status: 404},
	}})
	if r := get(h, "/en/missing"); r.status != 404 || r.body != "en 404" {
		t.Fatalf("got %d %q", r.status, r.body)
	}
}

func TestHostRule(t *testing.T) {
	h := newTestSite(t, map[string]string{"index.html": "x"}, Config{Redirects: []Redirect{
		{From: "https://old.example.com/*", To: "https://new.example.com/:splat", Status: 301, Force: true},
	}})
	r := get(h, "/a/b", func(r *http.Request) { r.Host = "old.example.com" })
	if r.status != 301 || r.location != "https://new.example.com/a/b" {
		t.Fatalf("got %d %q", r.status, r.location)
	}
	if r := get(h, "/"); r.status != 200 {
		t.Fatalf("other hosts must not match, got %d", r.status)
	}
}

func TestHeaders(t *testing.T) {
	h := newTestSite(t, map[string]string{
		"index.html": "x",
		"img/a.jpg":  "jpg",
		"_headers":   "/*\n  X-Frame-Options: DENY\n  Link: </a.css>; rel=preload\n  Link: </b.js>; rel=preload\n# comment\n/img/*\n  X-Img: yes\n",
	}, Config{Headers: []HeaderRule{{For: "/*.jpg", Values: map[string]string{"Cache-Control": "public, max-age=31536000"}}}})

	r := get(h, "/img/a.jpg")
	if r.header.Get("Cache-Control") != "public, max-age=31536000" || r.header.Get("X-Frame-Options") != "DENY" || r.header.Get("X-Img") != "yes" {
		t.Fatalf("headers: %v", r.header)
	}
	if got := r.header.Get("Link"); got != "</a.css>; rel=preload, </b.js>; rel=preload" {
		t.Fatalf("Link: %q", got)
	}
	r = get(h, "/")
	if r.header.Get("Cache-Control") != "public, max-age=0, must-revalidate" || r.header.Get("X-Img") != "" {
		t.Fatalf("headers: %v", r.header)
	}
}

func TestProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.URL.RequestURI() + " " + r.Header.Get("X-From")))
	}))
	defer upstream.Close()
	h := newTestSite(t, map[string]string{}, Config{Redirects: []Redirect{
		{From: "/api/*", To: upstream.URL + "/v1/:splat", Status: 200, Headers: map[string]string{"X-From": "shipyard"}},
	}})
	r := get(h, "/api/users/1?x=2")
	if r.status != 200 || !strings.HasPrefix(r.body, "/v1/users/1?x=2 shipyard") {
		t.Fatalf("got %d %q", r.status, r.body)
	}
}
