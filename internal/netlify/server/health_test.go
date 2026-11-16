package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != healthUA {
			t.Errorf("user agent: %q", r.UserAgent())
		}
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
		case "/moved":
			http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	addr := ":" + srv.URL[strings.LastIndex(srv.URL, ":")+1:]

	for path, want := range map[string]int{"": 0, "/": 0, "/moved": 0, "/down": 1} {
		if got := probe(addr, path); got != want {
			t.Errorf("probe(%q) = %d, want %d", path, got, want)
		}
	}
	if probe(":1", "/") != 1 {
		t.Error("nothing listening must be unhealthy")
	}
}
