package deploy

import (
	"encoding/json"
	"strings"
	"testing"

	"shipyard/internal/store"
)

func TestStackFile(t *testing.T) {
	p := &store.Project{ID: "p1", Slug: "site", Replicas: 3, HostPort: 8081, Network: "proxy",
		Env:       map[string]string{"PASSWORD": "pa$$word"},
		Labels:    map[string]string{"traefik.http.routers.site.rule": "Host(`example.com`)"},
		Placement: []string{"node.labels.web == true"}}

	m := &Manager{opts: Options{Target: TargetSwarm, NodeID: "node-abc"}}
	f := m.stackFile(p, "shipyard/site:d1", "d1", 8080)
	svc := f.Services["app"]
	if svc.Environment["PASSWORD"] != "pa$$$$word" {
		t.Errorf("$ must be escaped for compose interpolation: %q", svc.Environment["PASSWORD"])
	}
	if got := strings.Join(svc.Deploy.Placement.Constraints, ","); got != "node.labels.web == true,node.id == node-abc" {
		t.Errorf("without a registry the service must be pinned to this node: %s", got)
	}
	if svc.Deploy.Replicas != 3 || svc.Deploy.UpdateConfig.Order != "start-first" || svc.Deploy.UpdateConfig.FailureAction != "rollback" {
		t.Errorf("deploy: %+v", svc.Deploy)
	}
	if len(svc.Ports) != 1 || svc.Ports[0].Published != 8081 || svc.Ports[0].Target != 8080 || svc.Ports[0].Mode != "ingress" {
		t.Errorf("ports: %+v", svc.Ports)
	}
	if svc.Deploy.Labels["traefik.http.routers.site.rule"] == "" || svc.Labels["shipyard.deployment"] != "d1" {
		t.Errorf("labels: %+v / %+v", svc.Deploy.Labels, svc.Labels)
	}
	if !f.Networks["proxy"].External || svc.Networks[0] != "proxy" {
		t.Errorf("network: %+v", f.Networks)
	}
	if b, _ := json.Marshal(f); !strings.Contains(string(b), `"version":"3.8"`) {
		t.Errorf("compose: %s", b)
	}

	if len(svc.Volumes) != 0 || f.Volumes != nil {
		t.Errorf("only Netlify projects get a blobs volume: %+v", svc.Volumes)
	}
	p.BuildType = store.BuildNetlify
	f = m.stackFile(p, "x", "d1", 8080)
	if v := f.Services["app"].Volumes; len(v) != 1 || v[0] != "data0:/blobs" || f.Volumes["data0"].Name != "shipyard-site-blobs" {
		t.Errorf("blobs volume: %+v %+v", v, f.Volumes)
	}

	m.opts.Registry = "127.0.0.1:5000"
	if got := m.stackFile(p, "x", "d1", 8080).Services["app"].Deploy.Placement.Constraints; len(got) != 1 {
		t.Errorf("with a registry, no pinning: %v", got)
	}
	if m.ImageRepo(p) != "127.0.0.1:5000/shipyard-site" {
		t.Errorf("image repo: %s", m.ImageRepo(p))
	}
}
