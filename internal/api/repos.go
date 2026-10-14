package api

import (
	"context"
	"net/http"

	"strings"
	"time"

	"shipyard/internal/gitx"
)

// repoRequest identifies a repository and its credentials. When AuthSecret is
// empty, the secret stored on ProjectID is used (the UI never sees it).
type repoRequest struct {
	RepoURL      string `json:"repoUrl"`
	Branch       string `json:"branch"`
	AuthType     string `json:"authType"`
	AuthUsername string `json:"authUsername"`
	AuthSecret   string `json:"authSecret"`
	ProjectID    string `json:"projectId"`
}

func (s *Server) storedSecret(ctx context.Context, projectID, authType string) string {
	if projectID == "" {
		return ""
	}
	if p, err := s.Store.GetProject(ctx, projectID); err == nil && p.AuthType == authType {
		return p.AuthSecret
	}
	return ""
}

func (s *Server) auth(ctx context.Context, in repoRequest) gitx.Auth {
	secret := in.AuthSecret
	if secret == "" {
		secret = s.storedSecret(ctx, in.ProjectID, in.AuthType)
	}
	return gitx.Auth{Type: in.AuthType, Username: in.AuthUsername, Secret: secret}
}

// githubRepos lists the repositories reachable with a GitHub token.
func (s *Server) githubRepos(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token     string `json:"token"`
		ProjectID string `json:"projectId"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	token := strings.TrimSpace(in.Token)
	if token == "" {
		token = s.storedSecret(r.Context(), in.ProjectID, gitx.AuthToken)
	}
	if token == "" {
		writeError(w, http.StatusBadRequest, "enter a GitHub personal access token")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	login, repos, truncated, err := s.GitHub.Repos(ctx, token)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "login": login, "repos": repos, "truncated": truncated})
}

// gitDirs lists the directories of a branch, for the base directory picker.
func (s *Server) gitDirs(w http.ResponseWriter, r *http.Request) {
	var in repoRequest
	if !readJSON(w, r, &in) {
		return
	}
	in.RepoURL = strings.TrimSpace(in.RepoURL)
	if err := gitx.ValidateURL(in.RepoURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := gitx.ValidateBranch(in.Branch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	dirs, truncated, err := s.Git.ListDirs(ctx, in.RepoURL, in.Branch, s.auth(r.Context(), in))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dirs": dirs, "truncated": truncated})
}
