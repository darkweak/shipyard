// Package api exposes the REST API, the webhook endpoint and the web UI.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"shipyard/internal/deploy"
	"shipyard/internal/dockerx"
	"shipyard/internal/github"
	"shipyard/internal/gitx"
	"shipyard/internal/logstream"
	"shipyard/internal/store"
)

const sessionCookie = "shipyard_session"

type Server struct {
	Store  *store.Store
	Deploy *deploy.Manager
	Docker *dockerx.Docker
	Git    *gitx.Client
	GitHub *github.Client
	Hub    *logstream.Hub
	Log    *slog.Logger
	// Token protects the API. Required.
	Token string
	// UI is the built web app (may be nil in development).
	UI fs.FS
	// SecureCookies sets the Secure flag (enable behind HTTPS).
	SecureCookies bool
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("POST /hooks/{project}/{secret}", s.webhook)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	api.HandleFunc("GET /api/system", s.system)

	api.HandleFunc("GET /api/templates", s.listTemplates)
	api.HandleFunc("POST /api/templates", s.createTemplate)
	api.HandleFunc("POST /api/templates/preview", s.previewTemplate)
	api.HandleFunc("GET /api/templates/{id}", s.getTemplate)
	api.HandleFunc("PUT /api/templates/{id}", s.updateTemplate)
	api.HandleFunc("DELETE /api/templates/{id}", s.deleteTemplate)

	api.HandleFunc("POST /api/git/branches", s.gitBranches)
	api.HandleFunc("POST /api/git/inspect", s.inspectRepo)
	api.HandleFunc("POST /api/git/dirs", s.gitDirs)
	api.HandleFunc("POST /api/github/repos", s.githubRepos)

	api.HandleFunc("GET /api/projects", s.listProjects)
	api.HandleFunc("POST /api/projects", s.createProject)
	api.HandleFunc("GET /api/projects/{id}", s.getProject)
	api.HandleFunc("PUT /api/projects/{id}", s.updateProject)
	api.HandleFunc("DELETE /api/projects/{id}", s.deleteProject)
	api.HandleFunc("POST /api/projects/{id}/deploy", s.deployProject)
	api.HandleFunc("POST /api/projects/{id}/webhook/rotate", s.rotateWebhook)
	api.HandleFunc("GET /api/projects/{id}/deployments", s.listDeployments)
	api.HandleFunc("GET /api/projects/{id}/container", s.containerState)
	api.HandleFunc("POST /api/projects/{id}/container/{action}", s.containerAction)
	api.HandleFunc("GET /api/projects/{id}/container/logs", s.containerLogs)

	api.HandleFunc("GET /api/deployments/{id}", s.getDeployment)
	api.HandleFunc("GET /api/deployments/{id}/logs", s.streamDeploymentLogs)
	api.HandleFunc("POST /api/deployments/{id}/cancel", s.cancelDeployment)
	api.HandleFunc("POST /api/deployments/{id}/redeploy", s.redeploy)

	mux.Handle("/api/", s.requireAuth(api))
	mux.Handle("/", s.ui())

	return s.logRequests(mux)
}

// ---- auth ----

func (s *Server) authorized(r *http.Request) bool {
	tok := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok = strings.TrimPrefix(h, "Bearer ")
	} else if c, err := r.Cookie(sessionCookie); err == nil {
		tok = c.Value
	}
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) == 1
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Token), []byte(s.Token)) != 1 {
		time.Sleep(500 * time.Millisecond) // slow down guessing
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.Token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: s.SecureCookies, MaxAge: 30 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- system ----

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"docker": map[string]any{"ok": false}, "target": s.Deploy.Target(), "registry": s.Deploy.Registry()}
	if s.Deploy.Target() == deploy.TargetSwarm {
		if info, err := s.Docker.SwarmInfo(r.Context()); err == nil {
			out["swarm"] = info
		}
	}
	if v, err := s.Docker.ServerVersion(r.Context()); err != nil {
		out["docker"] = map[string]any{"ok": false, "error": err.Error()}
	} else {
		out["docker"] = map[string]any{"ok": true, "version": v}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- UI ----

// ui serves the single-page app, falling back to index.html for client routes.
func (s *Server) ui() http.Handler {
	if s.UI == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "UI not built: run `make ui` or use the Vite dev server", http.StatusNotFound)
		})
	}
	files := http.FileServerFS(s.UI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(s.UI, p); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

// ---- helpers ----

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush keeps SSE working through the recorder.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/hooks/") {
			// Never log the webhook secret.
			p := r.URL.Path
			if strings.HasPrefix(p, "/hooks/") {
				p = "/hooks/…"
			}
			s.Log.Debug("http", "method", r.Method, "path", p, "status", rec.status, "dur", time.Since(start))
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeStoreError maps store errors to HTTP statuses.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}
