// Package deploy runs the clone → build → swap-container pipeline.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"shipyard/internal/dockerx"
	"shipyard/internal/gitx"
	"shipyard/internal/logstream"
	"shipyard/internal/netlify"
	"shipyard/internal/store"
	"shipyard/internal/templates"
)

// healthWindow is how long a fresh container must stay up before the
// previous one is discarded.
const healthWindow = 5 * time.Second

// secretKey matches env var names whose values are masked in deployment logs.
var secretKey = regexp.MustCompile(`(?i)secret|token|passw|private|api_?key|credential`)

type Manager struct {
	store   *store.Store
	docker  *dockerx.Docker
	git     *gitx.Client
	hub     *logstream.Hub
	log     *slog.Logger
	workDir string
	opts    Options

	sem chan struct{} // bounds concurrent builds

	mu       sync.Mutex
	projLock map[string]*sync.Mutex
	cancels  map[string]context.CancelFunc
	wg       sync.WaitGroup
}

// Deployment targets.
const (
	TargetDocker = "docker" // one container per project on the local daemon
	TargetSwarm  = "swarm"  // one stack per project, deployed from a manager node
)

type Options struct {
	WorkDir       string
	MaxConcurrent int
	Target        string
	// Registry prefixes image names (e.g. 127.0.0.1:5000) and images are
	// pushed there, so every Swarm node can pull them.
	Registry string
	// NodeID pins services to this node when there's no registry.
	NodeID string
}

func NewManager(s *store.Store, d *dockerx.Docker, g *gitx.Client, hub *logstream.Hub, log *slog.Logger, o Options) *Manager {
	if o.MaxConcurrent < 1 {
		o.MaxConcurrent = 1
	}
	if o.Target == "" {
		o.Target = TargetDocker
	}
	return &Manager{
		store: s, docker: d, git: g, hub: hub, log: log, workDir: o.WorkDir, opts: o,
		sem:      make(chan struct{}, o.MaxConcurrent),
		projLock: map[string]*sync.Mutex{},
		cancels:  map[string]context.CancelFunc{},
	}
}

func (m *Manager) Target() string   { return m.opts.Target }
func (m *Manager) Registry() string { return m.opts.Registry }

func ContainerName(p *store.Project) string { return "shipyard-" + p.Slug }
func StackName(p *store.Project) string     { return "shipyard-" + p.Slug }
func ServiceName(p *store.Project) string   { return StackName(p) + "_app" }

// BlobsVolume holds a Netlify project's Netlify Blobs, kept across deployments.
func BlobsVolume(p *store.Project) string { return "shipyard-" + p.Slug + "-blobs" }

// volumes are the "name:/path" mounts of a project's container.
func volumes(p *store.Project) []string {
	if p.BuildType == store.BuildNetlify {
		return []string{BlobsVolume(p) + ":" + netlify.BlobsDir}
	}
	return nil
}

func (m *Manager) ImageRepo(p *store.Project) string {
	if m.opts.Registry != "" {
		return strings.TrimSuffix(m.opts.Registry, "/") + "/shipyard-" + p.Slug
	}
	return "shipyard/" + p.Slug
}

// Trigger queues a build of the project's branch.
func (m *Manager) Trigger(ctx context.Context, projectID, trigger string) (*store.Deployment, error) {
	if _, err := m.store.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	d := &store.Deployment{ProjectID: projectID, Status: store.StatusQueued, Trigger: trigger}
	if err := m.store.CreateDeployment(ctx, d); err != nil {
		return nil, err
	}
	m.start(d)
	return d, nil
}

// Rollback redeploys the image built by a previous successful deployment.
func (m *Manager) Rollback(ctx context.Context, sourceID string) (*store.Deployment, error) {
	src, err := m.store.GetDeployment(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if src.Status != store.StatusSucceeded || src.Image == "" {
		return nil, fmt.Errorf("%w: only successful deployments can be redeployed", store.ErrConflict)
	}
	if !m.docker.ImageExists(ctx, src.Image) {
		return nil, fmt.Errorf("%w: image %s no longer exists", store.ErrConflict, src.Image)
	}
	d := &store.Deployment{
		ProjectID: src.ProjectID, Status: store.StatusQueued, Trigger: "rollback",
		CommitSHA: src.CommitSHA, CommitMessage: src.CommitMessage, Image: src.Image,
		SourceDeploymentID: src.ID,
	}
	if err := m.store.CreateDeployment(ctx, d); err != nil {
		return nil, err
	}
	m.start(d)
	return d, nil
}

// Cancel stops a queued or running deployment.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.cancels[id]; ok {
		c()
		return true
	}
	return false
}

// Wait blocks until running deployments finish (used on shutdown).
func (m *Manager) Wait() { m.wg.Wait() }

func (m *Manager) start(d *store.Deployment) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancels[d.ID] = cancel
	lock := m.projLock[d.ProjectID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.projLock[d.ProjectID] = lock
	}
	m.mu.Unlock()

	stream := m.hub.Open(d.ID)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.cancels, d.ID)
			m.mu.Unlock()
			cancel()
		}()
		m.execute(ctx, lock, d, stream)
	}()
}

type runner struct {
	m      *Manager
	d      *store.Deployment
	stream *logstream.Stream
	// secrets are masked in every log line.
	secrets []string
}

func (r *runner) logf(format string, args ...any) {
	r.line(fmt.Sprintf(format, args...))
}

func (r *runner) line(s string) {
	for _, sec := range r.secrets {
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, "****")
		}
	}
	r.stream.Write(s)
}

func (r *runner) step(status, title string) {
	r.d.Status = status
	r.save()
	r.line("")
	r.line("==> " + title)
}

func (r *runner) save() {
	// Persisting must work even when the deployment context is cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.m.store.SaveDeployment(ctx, r.d); err != nil {
		r.m.log.Error("save deployment", "id", r.d.ID, "err", err)
	}
	if err := r.m.store.SetDeploymentLog(ctx, r.d.ID, r.stream.String()); err != nil {
		r.m.log.Error("save deployment log", "id", r.d.ID, "err", err)
	}
}

func (m *Manager) execute(ctx context.Context, lock *sync.Mutex, d *store.Deployment, stream *logstream.Stream) {
	r := &runner{m: m, d: d, stream: stream}
	defer m.hub.Close(d.ID)

	r.logf("Deployment %s queued (%s)", d.ID, d.Trigger)

	// One deployment per project at a time; others wait their turn.
	locked := make(chan struct{})
	go func() { lock.Lock(); close(locked) }()
	select {
	case <-locked:
	case <-ctx.Done():
		go func() { <-locked; lock.Unlock() }()
		r.finish(ctx.Err())
		return
	}
	defer lock.Unlock()

	select {
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		r.finish(ctx.Err())
		return
	}
	defer func() { <-m.sem }()

	now := time.Now().UTC()
	d.StartedAt = &now
	err := m.pipeline(ctx, r)
	if err != nil && ctx.Err() != nil {
		err = ctx.Err() // a killed git/docker process reports "signal: killed"
	}
	r.finish(err)
}

func (r *runner) finish(err error) {
	now := time.Now().UTC()
	r.d.FinishedAt = &now
	switch {
	case err == nil:
		r.d.Status = store.StatusSucceeded
		r.line("")
		r.logf("==> Deployment succeeded in %s", r.elapsed())
	case errors.Is(err, context.Canceled):
		r.d.Status = store.StatusCancelled
		r.d.Error = "cancelled"
		r.line("")
		r.line("==> Deployment cancelled")
	default:
		r.d.Status = store.StatusFailed
		r.d.Error = err.Error()
		r.line("")
		r.logf("==> Deployment failed: %v", err)
	}
	r.save()
	r.m.log.Info("deployment finished", "id", r.d.ID, "project", r.d.ProjectID, "status", r.d.Status)
}

func (r *runner) elapsed() string {
	if r.d.StartedAt == nil {
		return "0s"
	}
	return time.Since(*r.d.StartedAt).Round(time.Second).String()
}

func (m *Manager) pipeline(ctx context.Context, r *runner) error {
	d := r.d
	p, err := m.store.GetProject(ctx, d.ProjectID)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	if p.AuthType == gitx.AuthToken {
		r.secrets = append(r.secrets, p.AuthSecret)
	}
	for k, v := range p.Env {
		if secretKey.MatchString(k) {
			r.secrets = append(r.secrets, v)
		}
	}

	dir := filepath.Join(m.workDir, d.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	containerPort := p.ContainerPort

	if d.SourceDeploymentID == "" {
		repoDir := filepath.Join(dir, "repo")
		r.step(store.StatusCloning, fmt.Sprintf("Cloning %s (branch %s)", p.RepoURL, p.Branch))
		lw := logstream.NewLineWriter(r.line)
		err := m.git.Clone(ctx, p.RepoURL, p.Branch, repoDir,
			gitx.Auth{Type: p.AuthType, Username: p.AuthUsername, Secret: p.AuthSecret}, lw)
		lw.Flush()
		if err != nil {
			return err
		}
		if d.CommitSHA, d.CommitMessage, err = gitx.Head(ctx, repoDir); err != nil {
			return fmt.Errorf("read commit: %w", err)
		}
		r.logf("Checked out %.12s — %s", d.CommitSHA, d.CommitMessage)
		r.save()

		buildCtx, err := gitx.SafeJoin(repoDir, p.BuildContext)
		if err != nil {
			return err
		}
		var dockerfile string
		var port int
		if p.BuildType == store.BuildNetlify {
			dockerfile, err = m.prepareNetlify(r, p, dir, repoDir)
			port, buildCtx = netlify.ServerPort, repoDir
		} else {
			dockerfile, port, err = m.prepareDockerfile(ctx, r, p, dir, repoDir, buildCtx)
		}
		if err != nil {
			return err
		}
		if containerPort == 0 {
			containerPort = port
		}

		// Project variables are also available while building, as a BuildKit
		// secret: RUN --mount=type=secret,id=buildenv sh -c '. /run/secrets/buildenv; …'
		secretFile := filepath.Join(dir, "build.env")
		if err := os.WriteFile(secretFile, []byte(netlify.BuildEnvFile(p.Env)), 0o600); err != nil {
			return err
		}

		d.Image = fmt.Sprintf("%s:%s", m.ImageRepo(p), d.ID)
		r.step(store.StatusBuilding, "Building image "+d.Image)
		lw = logstream.NewLineWriter(r.line)
		err = m.docker.Build(ctx, buildCtx, dockerfile, d.Image, secretFile, map[string]string{
			"shipyard.project":    p.ID,
			"shipyard.deployment": d.ID,
			"shipyard.commit":     d.CommitSHA,
		}, lw)
		lw.Flush()
		if err != nil {
			return err
		}
	} else {
		r.logf("Redeploying image %s from deployment %s", d.Image, d.SourceDeploymentID)
		if containerPort == 0 && p.BuildType == store.BuildNetlify {
			containerPort = netlify.ServerPort
		}
		if containerPort == 0 && p.BuildType == store.BuildTemplate {
			if t, err := m.store.GetTemplate(ctx, p.TemplateID); err == nil {
				containerPort = t.DefaultPort
			}
		}
	}

	var id string
	if m.opts.Target == TargetSwarm {
		if m.opts.Registry != "" && d.SourceDeploymentID == "" {
			r.line("")
			r.line("==> Pushing " + d.Image)
			lw := logstream.NewLineWriter(r.line)
			err := m.docker.Push(ctx, d.Image, lw)
			lw.Flush()
			if err != nil {
				return err
			}
		}
		r.step(store.StatusDeploying, "Deploying stack "+StackName(p))
		id, err = m.deployStack(ctx, r, p, containerPort, dir)
	} else {
		r.step(store.StatusDeploying, "Starting container "+ContainerName(p))
		id, err = m.swap(ctx, r, p, containerPort, dir)
	}
	if err != nil {
		return err
	}
	d.ContainerID = id
	if p.HostPort > 0 && containerPort > 0 {
		where := "host port"
		if m.opts.Target == TargetSwarm {
			where = "port (routing mesh, every node)"
		}
		r.logf("Listening on %s %d → container port %d", where, p.HostPort, containerPort)
	}
	return nil
}

// prepareNetlify generates the build from netlify.toml and returns the Dockerfile path.
func (m *Manager) prepareNetlify(r *runner, p *store.Project, dir, repoDir string) (string, error) {
	plan, err := netlify.Load(repoDir, p.BuildContext, p.Env)
	if err != nil {
		return "", err
	}
	r.line("Using netlify.toml (production context):")
	r.logf("  base directory    %s", plan.Base)
	r.logf("  publish directory %s", plan.Publish)
	r.logf("  build command     %s", orNone(plan.Command))
	tools := "Node.js " + plan.NodeVersion
	if plan.HugoVersion != "" {
		tools += ", Hugo " + plan.HugoVersion + " extended"
	}
	if plan.GoVersion != "" {
		tools += ", Go " + plan.GoVersion
	}
	if plan.Ruby {
		tools += ", Ruby"
	}
	if plan.Python {
		tools += ", Python"
	}
	r.logf("  tools             %s", tools)
	r.logf("  rules             %d redirect(s), %d header block(s) (+ _redirects / _headers if published)", len(plan.Redirects), len(plan.Headers))
	if len(plan.Functions) > 0 {
		r.logf("  functions         %d in %s (esbuild %s, Node.js runtime)", len(plan.Functions), plan.FunctionsDir, netlify.EsbuildVersion)
		for _, fn := range plan.Functions {
			line := "                    · " + fn.Name + "  ←  " + fn.Entry
			if fn.Schedule != "" {
				line += "  (scheduled: " + fn.Schedule + ")"
			}
			r.line(line)
		}
	}
	for _, w := range plan.Warnings {
		r.line("  ⚠ " + w)
	}

	content, err := netlify.Write(plan, repoDir, netlify.Meta{
		Branch: p.Branch, Commit: r.d.CommitSHA, DeployID: r.d.ID, SiteName: p.Slug,
	})
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	r.line("Generated Dockerfile:")
	for _, l := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		r.line("  │ " + l)
	}
	return path, nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// prepareDockerfile returns the Dockerfile path to build with and the
// template's default port (0 when the repository Dockerfile is used).
func (m *Manager) prepareDockerfile(ctx context.Context, r *runner, p *store.Project, dir, repoDir, buildCtx string) (string, int, error) {
	if p.BuildType != store.BuildTemplate {
		path, err := gitx.SafeJoin(repoDir, p.DockerfilePath)
		if err != nil {
			return "", 0, err
		}
		if _, err := os.Stat(path); err != nil {
			return "", 0, fmt.Errorf("no Dockerfile at %s in the repository; pick a template instead", p.DockerfilePath)
		}
		r.logf("Using repository Dockerfile %s", p.DockerfilePath)
		return path, 0, nil
	}

	t, err := m.store.GetTemplate(ctx, p.TemplateID)
	if err != nil {
		return "", 0, fmt.Errorf("load template %q: %w", p.TemplateID, err)
	}
	content, err := templates.Render(t, p.TemplateVars)
	if err != nil {
		return "", 0, err
	}
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", 0, err
	}
	r.logf("Using template %q, rendered Dockerfile:", t.Name)
	for _, l := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		r.line("  │ " + l)
	}

	// The template's .dockerignore only applies when the repository has none.
	ignore := filepath.Join(buildCtx, ".dockerignore")
	if t.Dockerignore != "" {
		if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(ignore, []byte(t.Dockerignore), 0o644); err != nil {
				return "", 0, err
			}
			r.line("Added the template's .dockerignore (the repository has none)")
		}
	}
	return path, t.DefaultPort, nil
}

// swap replaces the running container. The previous container is kept aside
// (stopped, renamed) until the new one has survived healthWindow, and is
// restored if anything goes wrong.
func (m *Manager) swap(ctx context.Context, r *runner, p *store.Project, containerPort int, dir string) (string, error) {
	name := ContainerName(p)
	prev := name + "-previous"

	_ = m.docker.Remove(ctx, prev)
	old, err := m.docker.Inspect(ctx, name)
	if err != nil {
		return "", err
	}
	if old.Exists {
		r.line("Stopping the current container")
		if err := m.docker.Action(ctx, name, "stop"); err != nil {
			return "", err
		}
		if err := m.docker.Rename(ctx, name, prev); err != nil {
			_ = m.docker.Action(ctx, name, "start")
			return "", err
		}
	}
	restore := func(reason error) error {
		// ctx may be cancelled: clean up with a fresh one.
		bg, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = m.docker.Remove(bg, name)
		if old.Exists {
			r.line("Restoring the previous container")
			if err := m.docker.Rename(bg, prev, name); err == nil && old.Running {
				_ = m.docker.Action(bg, name, "start")
			}
		}
		return reason
	}

	labels := map[string]string{
		"shipyard.project":    p.ID,
		"shipyard.deployment": r.d.ID,
	}
	for k, v := range p.Labels {
		labels[k] = v
	}
	r.logHealth(p, containerPort)
	id, err := m.docker.Run(ctx, dockerx.RunOptions{
		Name: name, Image: r.d.Image, HostPort: p.HostPort, ContainerPort: containerPort,
		Network: p.Network, Env: runtimeEnv(p), Labels: labels, Volumes: volumes(p), WorkDir: dir,
		Health: containerHealth(p, containerPort),
	})
	if err != nil {
		return "", restore(err)
	}
	r.logf("Container %.12s started, checking it stays up for %s…", id, healthWindow)

	select {
	case <-time.After(healthWindow):
	case <-ctx.Done():
		return "", restore(ctx.Err())
	}
	st, err := m.docker.Inspect(ctx, name)
	if err != nil {
		return "", restore(err)
	}
	containerLogs := func() {
		if logs, err := m.docker.Logs(ctx, name, 50); err == nil && logs != "" {
			r.line("Container output:")
			for _, l := range strings.Split(strings.TrimRight(logs, "\n"), "\n") {
				r.line("  │ " + l)
			}
		}
	}
	if !st.Running || st.Restarting || st.RestartCount > 0 {
		containerLogs()
		return "", restore(fmt.Errorf("container is not running (state %s, exit code %d)", st.State, st.ExitCode))
	}
	if p.Healthcheck && st.Health == "" {
		r.line("⚠ The image has no healthcheck to run (built before healthchecks were supported?): redeploy from a new build")
	}
	if err := m.waitHealthy(ctx, r, name); err != nil {
		containerLogs()
		return "", restore(err)
	}
	if old.Exists {
		_ = m.docker.Remove(ctx, prev)
	}
	return id, nil
}
