package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"shipyard/internal/gitx"
	"shipyard/internal/netlify"
	"shipyard/internal/store"
)

var (
	envKey      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	networkName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	nonSlug     = regexp.MustCompile(`[^a-z0-9]+`)
)

type projectInput struct {
	Name         string `json:"name"`
	RepoURL      string `json:"repoUrl"`
	Branch       string `json:"branch"`
	AuthType     string `json:"authType"`
	AuthUsername string `json:"authUsername"`
	// AuthSecret left empty on update keeps the stored secret.
	AuthSecret      string            `json:"authSecret"`
	BuildType       string            `json:"buildType"`
	TemplateID      string            `json:"templateId"`
	TemplateVars    map[string]string `json:"templateVars"`
	DockerfilePath  string            `json:"dockerfilePath"`
	BuildContext    string            `json:"buildContext"`
	ContainerPort   int               `json:"containerPort"`
	HostPort        int               `json:"hostPort"`
	Env             map[string]string `json:"env"`
	Network         string            `json:"network"`
	Labels          map[string]string `json:"labels"`
	Replicas        *int              `json:"replicas"`
	Placement       []string          `json:"placement"`
	Healthcheck     bool              `json:"healthcheck"`
	HealthcheckPath string            `json:"healthcheckPath"`
}

// healthPath keeps healthcheck paths shell- and compose-safe (no quotes, $, spaces).
var healthPath = regexp.MustCompile(`^/[A-Za-z0-9._~%/?=&+,:@-]*$`)

// apply validates the input and copies it onto p.
func (s *Server) apply(ctx context.Context, in projectInput, p *store.Project) error {
	in.Name = strings.TrimSpace(in.Name)
	in.RepoURL = strings.TrimSpace(in.RepoURL)
	in.Branch = strings.TrimSpace(in.Branch)
	if in.Name == "" {
		return fmt.Errorf("name is required")
	}
	if err := gitx.ValidateURL(in.RepoURL); err != nil {
		return err
	}
	if in.Branch == "" {
		in.Branch = "main"
	}
	if err := gitx.ValidateBranch(in.Branch); err != nil {
		return err
	}

	secret := in.AuthSecret
	if secret == "" && in.AuthType == p.AuthType {
		secret = p.AuthSecret
	}
	switch in.AuthType {
	case "", gitx.AuthNone:
		in.AuthType, secret, in.AuthUsername = gitx.AuthNone, "", ""
	case gitx.AuthToken:
		if gitx.IsSSHURL(in.RepoURL) {
			return fmt.Errorf("token authentication needs an https:// repository URL")
		}
		if secret == "" {
			return fmt.Errorf("an access token is required")
		}
	case gitx.AuthSSH:
		if !gitx.IsSSHURL(in.RepoURL) {
			return fmt.Errorf("SSH key authentication needs an SSH repository URL (git@host:owner/repo.git)")
		}
		if !strings.Contains(secret, "PRIVATE KEY") {
			return fmt.Errorf("a PEM/OpenSSH private key is required")
		}
	default:
		return fmt.Errorf("unknown auth type %q", in.AuthType)
	}

	if in.BuildType == "" {
		in.BuildType = store.BuildDockerfile
		if in.TemplateID != "" {
			in.BuildType = store.BuildTemplate
		}
	}
	switch in.BuildType {
	case store.BuildTemplate:
		if in.TemplateID == "" {
			return fmt.Errorf("pick a template")
		}
		if _, err := s.Store.GetTemplate(ctx, in.TemplateID); err != nil {
			return fmt.Errorf("unknown template %q", in.TemplateID)
		}
	case store.BuildDockerfile, store.BuildNetlify:
		in.TemplateID = ""
	default:
		return fmt.Errorf("unknown build type %q", in.BuildType)
	}
	if in.DockerfilePath == "" {
		in.DockerfilePath = "Dockerfile"
	}
	if in.BuildContext == "" {
		in.BuildContext = "."
	}
	for _, rel := range []string{in.DockerfilePath, in.BuildContext} {
		if _, err := gitx.SafeJoin("/repo", rel); err != nil {
			return err
		}
	}
	if in.ContainerPort < 0 || in.ContainerPort > 65535 || in.HostPort < 0 || in.HostPort > 65535 {
		return fmt.Errorf("ports must be between 1 and 65535")
	}
	for k, v := range in.Env {
		if !envKey.MatchString(k) {
			return fmt.Errorf("invalid environment variable name %q", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("environment variable %s: multi-line values are not supported", k)
		}
	}
	for k := range in.Labels {
		if k == "" || strings.ContainsAny(k, "= \t\n") {
			return fmt.Errorf("invalid label name %q", k)
		}
	}
	if in.Network != "" && !networkName.MatchString(in.Network) {
		return fmt.Errorf("invalid network name %q", in.Network)
	}
	replicas := 1
	if in.Replicas != nil {
		replicas = *in.Replicas
	}
	if replicas < 0 || replicas > 100 {
		return fmt.Errorf("replicas must be between 0 and 100")
	}
	placement := []string{}
	for _, c := range in.Placement {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "==") && !strings.Contains(c, "!=") || strings.ContainsAny(c, "\n\r") {
			return fmt.Errorf("invalid placement constraint %q (e.g. node.labels.web == true)", c)
		}
		placement = append(placement, c)
	}
	in.HealthcheckPath = strings.TrimSpace(in.HealthcheckPath)
	if in.HealthcheckPath == "" {
		in.HealthcheckPath = "/"
	}
	if !healthPath.MatchString(in.HealthcheckPath) || len(in.HealthcheckPath) > 512 {
		return fmt.Errorf("invalid healthcheck path %q (e.g. /healthz)", in.HealthcheckPath)
	}
	if in.HostPort > 0 {
		all, err := s.Store.ListProjects(ctx)
		if err != nil {
			return err
		}
		for _, o := range all {
			if o.ID != p.ID && o.HostPort == in.HostPort {
				return fmt.Errorf("%w: host port %d is already used by project %q", store.ErrConflict, in.HostPort, o.Name)
			}
		}
	}

	p.Name, p.RepoURL, p.Branch = in.Name, in.RepoURL, in.Branch
	p.AuthType, p.AuthUsername, p.AuthSecret = in.AuthType, strings.TrimSpace(in.AuthUsername), secret
	p.BuildType, p.TemplateID, p.TemplateVars = in.BuildType, in.TemplateID, in.TemplateVars
	p.DockerfilePath, p.BuildContext = in.DockerfilePath, in.BuildContext
	p.ContainerPort, p.HostPort = in.ContainerPort, in.HostPort
	p.Env, p.Network, p.Labels = in.Env, in.Network, in.Labels
	p.Replicas, p.Placement = replicas, placement
	p.Healthcheck, p.HealthcheckPath = in.Healthcheck, in.HealthcheckPath
	return nil
}

func (s *Server) uniqueSlug(ctx context.Context, name string) (string, error) {
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "app"
	}
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	slug := base
	for i := 2; ; i++ {
		exists, err := s.Store.SlugExists(ctx, slug)
		if err != nil || !exists {
			return slug, err
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
}

type projectView struct {
	*store.Project
	LatestDeployment *store.Deployment `json:"latestDeployment"`
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Store.ListProjects(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	latest, err := s.Store.LatestDeployments(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]projectView, 0, len(ps))
	for _, p := range ps {
		out = append(out, projectView{Project: p, LatestDeployment: latest[p.ID]})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var in projectInput
	if !readJSON(w, r, &in) {
		return
	}
	p := &store.Project{}
	if err := s.apply(r.Context(), in, p); err != nil {
		writeValidationError(w, err)
		return
	}
	slug, err := s.uniqueSlug(r.Context(), p.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	p.Slug = slug
	if err := s.Store.CreateProject(r.Context(), p); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var in projectInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.apply(r.Context(), in, p); err != nil {
		writeValidationError(w, err)
		return
	}
	if err := s.Store.UpdateProject(r.Context(), p); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.Deploy.Remove(ctx, p); err != nil {
		writeError(w, http.StatusBadGateway, "remove: "+err.Error())
		return
	}
	if err := s.Store.DeleteProject(ctx, p.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deployProject(w http.ResponseWriter, r *http.Request) {
	d, err := s.Deploy.Trigger(r.Context(), r.PathValue("id"), "manual")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

func (s *Server) rotateWebhook(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	p.WebhookSecret = store.NewSecret()
	if err := s.Store.UpdateProject(r.Context(), p); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// gitBranches checks repository access with the given (or stored) credentials.
func (s *Server) gitBranches(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RepoURL      string `json:"repoUrl"`
		AuthType     string `json:"authType"`
		AuthUsername string `json:"authUsername"`
		AuthSecret   string `json:"authSecret"`
		ProjectID    string `json:"projectId"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.RepoURL = strings.TrimSpace(in.RepoURL)
	if err := gitx.ValidateURL(in.RepoURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.AuthSecret == "" {
		in.AuthSecret = s.storedSecret(r.Context(), in.ProjectID, in.AuthType)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	branches, err := s.Git.Branches(ctx, in.RepoURL, gitx.Auth{Type: in.AuthType, Username: in.AuthUsername, Secret: in.AuthSecret})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "branches": branches})
}

// inspectRepo looks for build configuration on a branch (netlify.toml,
// Dockerfile) so the UI can suggest a build type.
func (s *Server) inspectRepo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RepoURL      string `json:"repoUrl"`
		Branch       string `json:"branch"`
		Dir          string `json:"dir"`
		AuthType     string `json:"authType"`
		AuthUsername string `json:"authUsername"`
		AuthSecret   string `json:"authSecret"`
		ProjectID    string `json:"projectId"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := gitx.ValidateURL(strings.TrimSpace(in.RepoURL)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := gitx.ValidateBranch(in.Branch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dir := strings.Trim(path.Clean("/"+in.Dir), "/")
	if in.AuthSecret == "" {
		in.AuthSecret = s.storedSecret(r.Context(), in.ProjectID, in.AuthType)
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	files, err := s.Git.ReadFiles(ctx, strings.TrimSpace(in.RepoURL), in.Branch,
		gitx.Auth{Type: in.AuthType, Username: in.AuthUsername, Secret: in.AuthSecret},
		[]string{path.Join(dir, "netlify.toml"), path.Join(dir, "Dockerfile")})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := map[string]any{"ok": true, "hasDockerfile": files[path.Join(dir, "Dockerfile")] != nil}
	if data := files[path.Join(dir, "netlify.toml")]; data != nil {
		plan, err := netlify.Parse(data)
		if err != nil {
			out["netlify"] = map[string]any{"error": err.Error()}
		} else {
			out["netlify"] = plan
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- container ----

func (s *Server) containerState(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	st, err := s.Deploy.Status(r.Context(), p)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) containerAction(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.Deploy.Action(r.Context(), p, r.PathValue("action")); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.containerState(w, r)
}

func (s *Server) containerLogs(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	if tail <= 0 || tail > 5000 {
		tail = 300
	}
	logs, err := s.Deploy.Logs(r.Context(), p, tail)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

// ---- webhook ----

// webhook triggers a deployment from a push event. It works with GitHub,
// GitLab, Gitea/Forgejo and Bitbucket: the secret is part of the URL, and
// pushes to other branches are ignored when the payload has a "ref".
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("project"))
	if err != nil || subtle.ConstantTimeCompare([]byte(r.PathValue("secret")), []byte(p.WebhookSecret)) != 1 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Header.Get("X-GitHub-Event") == "ping" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	}
	var payload struct {
		Ref string `json:"ref"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	_ = json.NewDecoder(r.Body).Decode(&payload)
	if payload.Ref != "" && payload.Ref != "refs/heads/"+p.Branch {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "push to " + payload.Ref})
		return
	}
	d, err := s.Deploy.Trigger(r.Context(), p.ID, "webhook")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "deployment": d.ID})
}

func writeValidationError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrConflict) {
		writeStoreError(w, err)
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}
