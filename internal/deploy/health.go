package deploy

import (
	"context"
	"fmt"
	"maps"
	"time"

	"shipyard/internal/dockerx"
	"shipyard/internal/netlify"
	"shipyard/internal/store"
)

// Opt-in healthcheck: Docker probes the project's container port and Swarm
// replaces unhealthy tasks. Netlify images ship their own HEALTHCHECK
// (netlify-server -healthcheck, no shell needed), switched off unless the
// project opts in; other images get a shell probe using wget, curl or node.
const (
	healthInterval    = 10 * time.Second
	healthTimeout     = 5 * time.Second
	healthRetries     = 3
	healthStartPeriod = 30 * time.Second
	// healthWait bounds how long a fresh container may take to become healthy.
	healthWait = healthStartPeriod + healthRetries*healthInterval + 30*time.Second
)

// probeScript checks http://127.0.0.1:<port><path> with whichever HTTP client
// the image ships. Paths are validated by the API (no quotes, $ or spaces).
func probeScript(port int, path string) string {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	return fmt.Sprintf(`if command -v wget >/dev/null 2>&1; then wget -q -T 5 -O /dev/null '%[1]s'; `+
		`elif command -v curl >/dev/null 2>&1; then curl -fsS -m 5 -o /dev/null '%[1]s'; `+
		`elif command -v node >/dev/null 2>&1; then node -e "fetch('%[1]s').then(r=>process.exit(r.status<400?0:1),e=>{console.error(e.message);process.exit(1)})"; `+
		`else echo 'healthcheck: no wget, curl or node in the image' >&2; exit 1; fi`, url)
}

func healthPath(p *store.Project) string {
	if p.HealthcheckPath == "" {
		return "/"
	}
	return p.HealthcheckPath
}

// containerHealth returns the healthcheck to run the container with: nil
// keeps the image's own HEALTHCHECK (if any).
func containerHealth(p *store.Project, containerPort int) *dockerx.Health {
	h := &dockerx.Health{Interval: healthInterval, Timeout: healthTimeout, Retries: healthRetries, StartPeriod: healthStartPeriod}
	switch {
	case p.BuildType == store.BuildNetlify && !p.Healthcheck:
		return &dockerx.Health{Disable: true}
	case p.BuildType == store.BuildNetlify:
		return h // the image's netlify-server -healthcheck, reading the path from the env
	case !p.Healthcheck || containerPort == 0:
		return nil
	}
	h.Cmd = probeScript(containerPort, healthPath(p))
	return h
}

// runtimeEnv is the project's environment plus what the healthcheck needs.
func runtimeEnv(p *store.Project) map[string]string {
	if !p.Healthcheck || p.BuildType != store.BuildNetlify {
		return p.Env
	}
	env := maps.Clone(p.Env)
	if env == nil {
		env = map[string]string{}
	}
	env[netlify.HealthPathEnv] = healthPath(p)
	return env
}

func (r *runner) logHealth(p *store.Project, containerPort int) {
	switch {
	case !p.Healthcheck:
	case p.BuildType != store.BuildNetlify && containerPort == 0:
		r.line("⚠ Healthcheck skipped: set the container port so it can be probed")
	default:
		r.logf("Healthcheck: GET %s on port %d every %s (unhealthy after %d failures)", healthPath(p), containerPort, healthInterval, healthRetries)
	}
}

// waitHealthy waits for the container's healthcheck to pass. It returns nil
// right away when the container has no healthcheck.
func (m *Manager) waitHealthy(ctx context.Context, r *runner, name string) error {
	deadline := time.Now().Add(healthWait)
	announced := false
	for {
		st, err := m.docker.Inspect(ctx, name)
		if err != nil {
			return err
		}
		switch {
		case st.Health == "":
			return nil
		case !st.Running || st.Restarting:
			return fmt.Errorf("container stopped while waiting for its healthcheck (state %s, exit code %d)", st.State, st.ExitCode)
		case st.Health == "healthy":
			r.line("Container is healthy")
			return nil
		case st.Health == "unhealthy":
			return fmt.Errorf("container is unhealthy: %s", orNone(st.HealthLog))
		}
		if !announced {
			r.logf("Waiting for the healthcheck to pass (up to %s)…", healthWait)
			announced = true
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("container did not become healthy within %s: %s", healthWait, orNone(st.HealthLog))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// stackHealth is the compose healthcheck for a project (nil: the image's).
func stackHealth(p *store.Project, containerPort int) *stackHealthcheck {
	h := containerHealth(p, containerPort)
	if h == nil {
		return nil
	}
	if h.Disable {
		return &stackHealthcheck{Disable: true}
	}
	s := &stackHealthcheck{Interval: h.Interval.String(), Timeout: h.Timeout.String(), Retries: h.Retries, StartPeriod: h.StartPeriod.String()}
	if h.Cmd != "" {
		s.Test = []string{"CMD-SHELL", escape(h.Cmd)}
	}
	return s
}

type stackHealthcheck struct {
	Test        []string `json:"test,omitempty"`
	Disable     bool     `json:"disable,omitempty"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	Retries     int      `json:"retries,omitempty"`
	StartPeriod string   `json:"start_period,omitempty"`
}
