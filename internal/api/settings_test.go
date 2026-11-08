package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"shipyard/internal/store"
)

func TestSettings(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := (&Server{Store: st, Token: "t", Log: slog.New(slog.DiscardHandler)}).Handler()
	do := func(method, body string) (int, string) {
		req := httptest.NewRequest(method, "/api/settings", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer t")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}

	if code, body := do("GET", ""); code != http.StatusOK || body != `{"defaultEnv":{},"defaultLabels":{}}` {
		t.Fatalf("empty settings: %d %s", code, body)
	}
	if code, _ := do("PUT", `{"defaultEnv":{"bad key":"x"}}`); code != http.StatusBadRequest {
		t.Fatalf("invalid env name: got %d", code)
	}
	if code, _ := do("PUT", `{"defaultLabels":{"a=b":"x"}}`); code != http.StatusBadRequest {
		t.Fatalf("invalid label name: got %d", code)
	}
	if code, body := do("PUT", `{"defaultEnv":{"TZ":"Europe/Paris"},"defaultLabels":{"traefik.enable":"true"}}`); code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	if _, body := do("GET", ""); body != `{"defaultEnv":{"TZ":"Europe/Paris"},"defaultLabels":{"traefik.enable":"true"}}` {
		t.Fatalf("saved settings: %s", body)
	}
}
