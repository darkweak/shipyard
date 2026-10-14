package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFunctionsEndToEnd runs the real Node.js runtime behind the server.
func TestFunctionsEndToEnd(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	fnDir := t.TempDir()
	runtime, err := os.ReadFile("../functions/runtime.mjs")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "background-ran")
	files := map[string]string{
		"runtime.mjs": string(runtime),
		"manifest.json": `{"functions":[{"name":"hello","file":"hello.mjs"},{"name":"items","file":"items.mjs"},
			{"name":"page","file":"page.mjs"},{"name":"job-background","file":"job-background.mjs"},
			{"name":"cron","file":"cron.mjs","schedule":"@daily"},{"name":"legacy","file":"legacy.mjs"}]}`,
		"hello.mjs": `export const handler = async (event) => ({ statusCode: 200, headers: {"content-type": "application/json"},
			body: JSON.stringify({ path: event.path, q: event.queryStringParameters, ip: event.headers["x-nf-client-connection-ip"] }) })`,
		"items.mjs": `export default async (req, context) => Response.json({ id: context.params.id, url: new URL(req.url).pathname, method: req.method })
			export const config = { path: "/api/items/:id", method: ["GET"] }`,
		"page.mjs":           `export default async () => new Response("from function"); export const config = { path: "/page", preferStatic: true }`,
		"job-background.mjs": `import { writeFileSync } from "node:fs"; export const handler = async () => { writeFileSync(` + jsString(marker) + `, "done"); return { statusCode: 200 } }`,
		"cron.mjs":           `export default async () => new Response("cron")`,
		// esbuild's ESM wrapping of a CommonJS module: the handler is on the default export.
		"legacy.mjs": `const require_legacy = { handler: (event, context, callback) => callback(null, { statusCode: 200, body: "cjs:" + context.functionName }) }; export default require_legacy;`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(fnDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := writeSite(t, map[string]string{"index.html": "home", "page.html": "static page"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	fn := newFunctions(node, filepath.Join(fnDir, "runtime.mjs"), port)
	go fn.run(ctx)
	if err := fn.waitReady(ctx, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Redirects: []Redirect{{From: "/api/v1/*", To: "/.netlify/functions/hello/:splat", Status: 200}}}
	for i := range cfg.Redirects {
		cfg.Redirects[i].host, cfg.Redirects[i].re, _ = compilePattern(cfg.Redirects[i].From)
	}
	srv := httptest.NewServer(&site{root: root, cfg: cfg, fn: fn})
	defer srv.Close()

	call := func(method, path string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(""))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	jsonOf := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatalf("not JSON: %q", s)
		}
		return m
	}

	status, body := call("GET", "/.netlify/functions/hello?a=1")
	if m := jsonOf(body); status != 200 || m["path"] != "/.netlify/functions/hello" || m["ip"] != "127.0.0.1" {
		t.Errorf("direct v1: %d %s", status, body)
	}
	// A rewrite to a function: the Lambda event sees the rewritten path.
	status, body = call("GET", "/api/v1/users/7?x=y")
	if m := jsonOf(body); status != 200 || m["path"] != "/.netlify/functions/hello/users/7" || m["q"].(map[string]any)["x"] != "y" {
		t.Errorf("rewrite v1: %d %s", status, body)
	}
	status, body = call("GET", "/api/items/42")
	if m := jsonOf(body); status != 200 || m["id"] != "42" || m["url"] != "/api/items/42" {
		t.Errorf("v2 path: %d %s", status, body)
	}
	if status, _ = call("POST", "/api/items/42"); status != 404 {
		t.Errorf("v2 method filter: got %d", status)
	}
	if status, _ = call("GET", "/.netlify/functions/items"); status != 404 {
		t.Errorf("v2 with config.path must not be reachable on the default path, got %d", status)
	}
	if status, body = call("GET", "/page"); body != "static page" {
		t.Errorf("preferStatic: %d %s", status, body)
	}
	if status, _ = call("GET", "/.netlify/functions/cron"); status != 404 {
		t.Errorf("scheduled functions are not invocable over HTTP, got %d", status)
	}
	if status, body = call("GET", "/.netlify/functions/legacy"); body != "cjs:legacy" {
		t.Errorf("CommonJS handler: %d %s", status, body)
	}
	if status, _ = call("POST", "/.netlify/functions/job-background"); status != 202 {
		t.Errorf("background: got %d", status)
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("background function did not run")
	}
	if status, _ = call("GET", "/.netlify/functions/nope"); status != 404 {
		t.Errorf("unknown function: got %d", status)
	}
	if status, body = call("GET", "/"); body != "home" {
		t.Errorf("static: %d %s", status, body)
	}
}

func jsString(s string) string { b, _ := json.Marshal(s); return string(b) }
