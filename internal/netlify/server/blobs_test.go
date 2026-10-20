package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBlobs speaks the edge protocol @netlify/blobs uses.
func TestBlobs(t *testing.T) {
	b, err := newBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(b)
	defer srv.Close()
	do := func(method, path, body string, h map[string]string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+b.token)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	read := func(res *http.Response) string { s, _ := io.ReadAll(res.Body); return string(s) }

	req, _ := http.NewRequest("GET", srv.URL+"/shipyard/site:s/k", nil)
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 401 {
		t.Fatalf("no token: got %d, want 401", res.StatusCode)
	}
	if res := do("GET", "/shipyard/site:s/k", "", nil); res.StatusCode != 404 {
		t.Fatalf("missing blob: got %d, want 404", res.StatusCode)
	}
	put := do("PUT", "/shipyard/site:s/dir/k", "hello", map[string]string{blobsMetaHeader: "b64;e30="})
	etag := put.Header.Get("ETag")
	if put.StatusCode != 200 || etag == "" {
		t.Fatalf("put: %d etag %q", put.StatusCode, etag)
	}
	// deploy stores are addressed with a region prefix
	get := do("GET", "/region:local/shipyard/site:s/dir/k", "", nil)
	if got := read(get); got != "hello" || get.Header.Get(blobsMetaHeader) != "b64;e30=" || get.Header.Get("ETag") != etag {
		t.Fatalf("get: %q %v", got, get.Header)
	}
	if res := do("GET", "/shipyard/site:s/dir/k", "", map[string]string{"If-None-Match": etag}); res.StatusCode != 304 {
		t.Fatalf("if-none-match: got %d, want 304", res.StatusCode)
	}
	if res := do("PUT", "/shipyard/site:s/dir/k", "x", map[string]string{"If-None-Match": "*"}); res.StatusCode != 412 {
		t.Fatalf("only-if-new on existing: got %d, want 412", res.StatusCode)
	}
	if res := do("PUT", "/shipyard/site:s/dir/k", "x", map[string]string{"If-Match": `"stale"`}); res.StatusCode != 412 {
		t.Fatalf("stale if-match: got %d, want 412", res.StatusCode)
	}
	if res := do("PUT", "/shipyard/site:s/dir/k", "x", map[string]string{"If-Match": etag}); res.StatusCode != 200 {
		t.Fatalf("matching if-match: got %d, want 200", res.StatusCode)
	}
	do("PUT", "/shipyard/site:s/top", "t", nil)

	var list struct {
		Blobs []struct {
			Key string `json:"key"`
		} `json:"blobs"`
		Directories []string `json:"directories"`
	}
	if err := json.Unmarshal([]byte(read(do("GET", "/shipyard/site:s?directories=true", "", nil))), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Blobs) != 1 || list.Blobs[0].Key != "top" || len(list.Directories) != 1 || list.Directories[0] != "dir" {
		t.Fatalf("list with directories: %+v", list)
	}
	var stores struct{ Stores []string }
	_ = json.Unmarshal([]byte(read(do("GET", "/shipyard?prefix=site:", "", nil))), &stores)
	if len(stores.Stores) != 1 || stores.Stores[0] != "site:s" {
		t.Fatalf("stores: %+v", stores)
	}
	if res := do("DELETE", "/shipyard/site:s/top", "", nil); res.StatusCode != 204 {
		t.Fatalf("delete: got %d", res.StatusCode)
	}
	if res := do("GET", "/shipyard/site:s/top", "", nil); res.StatusCode != 404 {
		t.Fatalf("after delete: got %d, want 404", res.StatusCode)
	}
}
