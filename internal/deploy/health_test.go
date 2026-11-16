package deploy

import (
	"strings"
	"testing"

	"shipyard/internal/netlify"
	"shipyard/internal/store"
)

func TestHealthcheck(t *testing.T) {
	m := &Manager{opts: Options{Target: TargetSwarm}}
	p := &store.Project{ID: "p1", Slug: "site", Replicas: 1, BuildType: store.BuildDockerfile, HealthcheckPath: "/healthz"}

	// Off by default: no healthcheck is added to other images.
	if h := m.stackFile(p, "x", "d1", 3000).Services["app"].Healthcheck; h != nil {
		t.Errorf("healthcheck must be opt-in: %+v", h)
	}
	if containerHealth(p, 3000) != nil {
		t.Error("docker run: healthcheck must be opt-in")
	}

	p.Healthcheck = true
	h := m.stackFile(p, "x", "d1", 3000).Services["app"].Healthcheck
	if h == nil || len(h.Test) != 2 || h.Test[0] != "CMD-SHELL" || !strings.Contains(h.Test[1], "http://127.0.0.1:3000/healthz") || h.Retries != 3 {
		t.Fatalf("shell probe: %+v", h)
	}
	if containerHealth(p, 0) != nil {
		t.Error("without a container port there's nothing to probe")
	}

	// Netlify images bake their own HEALTHCHECK: disabled unless opted in.
	p.BuildType = store.BuildNetlify
	svc := m.stackFile(p, "x", "d1", 8080).Services["app"]
	if svc.Healthcheck == nil || len(svc.Healthcheck.Test) != 0 || svc.Healthcheck.Interval == "" {
		t.Errorf("netlify: keep the image's command, set the timings: %+v", svc.Healthcheck)
	}
	if svc.Environment[netlify.HealthPathEnv] != "/healthz" {
		t.Errorf("netlify: the path goes through the env: %+v", svc.Environment)
	}
	p.Healthcheck = false
	svc = m.stackFile(p, "x", "d1", 8080).Services["app"]
	if svc.Healthcheck == nil || !svc.Healthcheck.Disable {
		t.Errorf("netlify: the image's healthcheck must be disabled when off: %+v", svc.Healthcheck)
	}
	if _, ok := svc.Environment[netlify.HealthPathEnv]; ok {
		t.Error("netlify: no healthcheck env when off")
	}
}
