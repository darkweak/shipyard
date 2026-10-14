package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shipyard/internal/dockerx"
	"shipyard/internal/logstream"
	"shipyard/internal/store"
)

const (
	// convergeTimeout bounds how long a Swarm rollout may take (image pulls included).
	convergeTimeout = 10 * time.Minute
	// stableFor is how long all tasks must stay up before a rollout counts as done.
	stableFor = 5 * time.Second
)

// Compose file (version 3.8) for one project, written as JSON (valid YAML).
type stackFile struct {
	Version  string                     `json:"version"`
	Services map[string]stackService    `json:"services"`
	Networks map[string]stackExtNetwork `json:"networks,omitempty"`
}

type stackService struct {
	Image       string            `json:"image"`
	Environment map[string]string `json:"environment,omitempty"`
	Ports       []stackPort       `json:"ports,omitempty"`
	Networks    []string          `json:"networks,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Deploy      stackDeploy       `json:"deploy"`
}

type stackPort struct {
	Target    int    `json:"target"`
	Published int    `json:"published"`
	Protocol  string `json:"protocol"`
	Mode      string `json:"mode"`
}

type stackDeploy struct {
	Mode           string            `json:"mode"`
	Replicas       int               `json:"replicas"`
	Labels         map[string]string `json:"labels,omitempty"`
	Placement      *stackPlacement   `json:"placement,omitempty"`
	UpdateConfig   stackUpdate       `json:"update_config"`
	RollbackConfig stackUpdate       `json:"rollback_config"`
	RestartPolicy  stackRestart      `json:"restart_policy"`
}

type stackPlacement struct {
	Constraints []string `json:"constraints"`
}

type stackUpdate struct {
	Parallelism   int    `json:"parallelism"`
	Order         string `json:"order"`
	FailureAction string `json:"failure_action,omitempty"`
	Monitor       string `json:"monitor,omitempty"`
	Delay         string `json:"delay,omitempty"`
}

type stackRestart struct {
	Condition string `json:"condition"`
	Delay     string `json:"delay"`
}

type stackExtNetwork struct {
	External bool `json:"external"`
}

// escape protects literal "$" from compose variable interpolation.
func escape(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func escapeMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = escape(v)
	}
	return out
}

func (m *Manager) stackFile(p *store.Project, image, deploymentID string, containerPort int) stackFile {
	svc := stackService{
		Image:       image,
		Environment: escapeMap(p.Env),
		Deploy: stackDeploy{
			Mode:     "replicated",
			Replicas: p.Replicas,
			// Zero-downtime rolling updates: start the new task first, and let
			// Swarm roll back by itself if it doesn't stay up.
			UpdateConfig:   stackUpdate{Parallelism: 1, Order: "start-first", FailureAction: "rollback", Monitor: "15s", Delay: "5s"},
			RollbackConfig: stackUpdate{Parallelism: 1, Order: "start-first"},
			RestartPolicy:  stackRestart{Condition: "any", Delay: "5s"},
		},
	}
	// Proxies such as Traefik or caddy-docker-proxy read service labels in
	// Swarm mode; containers get them too.
	labels := map[string]string{"shipyard.project": p.ID}
	for k, v := range p.Labels {
		labels[k] = v
	}
	svc.Deploy.Labels = escapeMap(labels)
	// The deployment label changes on every deploy, which rolls the tasks even
	// when the image is the same (e.g. a redeploy).
	containerLabels := map[string]string{"shipyard.project": p.ID, "shipyard.deployment": deploymentID}
	for k, v := range p.Labels {
		containerLabels[k] = v
	}
	svc.Labels = escapeMap(containerLabels)

	constraints := append([]string{}, p.Placement...)
	if m.opts.Registry == "" && m.opts.NodeID != "" {
		// Without a registry the image only exists on this node.
		constraints = append(constraints, "node.id == "+m.opts.NodeID)
	}
	if len(constraints) > 0 {
		svc.Deploy.Placement = &stackPlacement{Constraints: constraints}
	}
	if p.HostPort > 0 && containerPort > 0 {
		svc.Ports = []stackPort{{Target: containerPort, Published: p.HostPort, Protocol: "tcp", Mode: "ingress"}}
	}
	f := stackFile{Version: "3.8", Services: map[string]stackService{"app": svc}}
	if p.Network != "" {
		svc.Networks = []string{p.Network}
		f.Services["app"] = svc
		f.Networks = map[string]stackExtNetwork{p.Network: {External: true}}
	}
	return f
}

// deployStack runs `docker stack deploy` and waits for the rollout.
func (m *Manager) deployStack(ctx context.Context, r *runner, p *store.Project, containerPort int, dir string) (string, error) {
	name := ServiceName(p)
	before, err := m.docker.ServiceInspect(ctx, name)
	if err != nil {
		return "", err
	}
	if m.opts.Registry == "" {
		r.line("No registry configured: the service is pinned to this node (set SHIPYARD_REGISTRY to spread it across the cluster)")
	}
	f := m.stackFile(p, r.d.Image, r.d.ID, containerPort)
	b, _ := json.MarshalIndent(f, "", "  ")
	file := filepath.Join(dir, "stack.json")
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return "", err
	}
	r.logf("Service %s: %d replica(s), rolling update (start-first, automatic rollback)", name, p.Replicas)
	lw := logstream.NewLineWriter(r.line)
	err = m.docker.StackDeploy(ctx, StackName(p), file, lw)
	lw.Flush()
	if err == nil {
		err = m.waitConverged(ctx, r, name, r.d.Image, p.Replicas)
	}
	if err != nil {
		bg, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if logs, lerr := m.docker.ServiceLogs(bg, name, 30); lerr == nil && strings.TrimSpace(logs) != "" {
			r.line("Service output:")
			for _, l := range strings.Split(strings.TrimRight(logs, "\n"), "\n") {
				r.line("  │ " + l)
			}
		}
		switch {
		case before == nil:
			r.line("Removing the stack (first deployment)")
			_ = m.docker.StackRemove(bg, StackName(p))
		case errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "did not converge") || strings.Contains(err.Error(), "keep failing"):
			r.line("Rolling the service back to the previous version")
			_ = m.docker.ServiceRollback(bg, name)
		}
		return "", err
	}
	svc, err := m.docker.ServiceInspect(ctx, name)
	if err != nil || svc == nil {
		return "", err
	}
	return svc.ID, nil
}

// waitConverged polls the service until `replicas` tasks of image run and
// stay up, Swarm rolls the update back, or tasks keep failing.
func (m *Manager) waitConverged(ctx context.Context, r *runner, name, image string, replicas int) error {
	deadline := time.Now().Add(convergeTimeout)
	var stableSince time.Time
	last := ""
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		svc, err := m.docker.ServiceInspect(ctx, name)
		if err != nil {
			return err
		}
		if svc == nil {
			continue
		}
		tasks, err := m.docker.ServiceTasks(ctx, name)
		if err != nil {
			return err
		}
		running, failed, lastErr := 0, 0, ""
		for _, t := range tasks {
			if imageOf(t.Image) != image {
				continue
			}
			if t.DesiredState == "Running" && t.Running() {
				running++
			}
			if t.Failed() {
				failed++
				if lastErr == "" {
					lastErr = t.Error
				}
			}
		}
		status := fmt.Sprintf("%d/%d task(s) running", running, replicas)
		if svc.UpdateState != "" {
			status += " · update " + svc.UpdateState
		}
		if failed > 0 {
			status += fmt.Sprintf(" · %d failed (%s)", failed, orNone(lastErr))
		}
		if status != last {
			r.line("  " + status)
			last = status
		}
		switch svc.UpdateState {
		case "rollback_started", "rollback_completed", "rollback_paused", "paused":
			return fmt.Errorf("update failed, Swarm rolled back to the previous version: %s", svc.UpdateMessage)
		}
		if failed >= 3 && running < replicas {
			return fmt.Errorf("tasks keep failing: %s", orNone(lastErr))
		}
		if running >= replicas && (svc.UpdateState == "" || svc.UpdateState == "completed") {
			if stableSince.IsZero() {
				stableSince = time.Now()
			} else if time.Since(stableSince) >= stableFor {
				return nil
			}
		} else {
			stableSince = time.Time{}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("service did not converge within %s (%s)", convergeTimeout, status)
		}
	}
}

// imageOf strips the digest Swarm appends to task images.
func imageOf(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	return ref
}

// ---- runtime operations, for the API ----

// Status describes what runs for a project, whatever the target.
type Status struct {
	Mode string `json:"mode"`
	// Docker target.
	*dockerx.ContainerState
	// Swarm target.
	Service *dockerx.Service `json:"service,omitempty"`
	Running int              `json:"runningTasks"`
	Tasks   []dockerx.Task   `json:"tasks,omitempty"`
}

func (m *Manager) Status(ctx context.Context, p *store.Project) (*Status, error) {
	if m.opts.Target != TargetSwarm {
		st, err := m.docker.Inspect(ctx, ContainerName(p))
		if err != nil {
			return nil, err
		}
		return &Status{Mode: TargetDocker, ContainerState: st}, nil
	}
	st := &Status{Mode: TargetSwarm, ContainerState: &dockerx.ContainerState{Ports: []dockerx.Port{}}}
	svc, err := m.docker.ServiceInspect(ctx, ServiceName(p))
	if err != nil || svc == nil {
		return st, err
	}
	st.Service, st.Exists = svc, true
	tasks, err := m.docker.ServiceTasks(ctx, ServiceName(p))
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.DesiredState == "Running" && t.Running() {
			st.Running++
		}
	}
	if len(tasks) > 12 {
		tasks = tasks[:12]
	}
	st.Tasks = tasks
	st.ContainerState.Running = st.Running > 0
	st.ContainerState.Image = svc.Image
	return st, nil
}

// Action starts, stops or restarts what runs for a project.
func (m *Manager) Action(ctx context.Context, p *store.Project, action string) error {
	if m.opts.Target != TargetSwarm {
		return m.docker.Action(ctx, ContainerName(p), action)
	}
	switch action {
	case "start":
		return m.docker.ServiceScale(ctx, ServiceName(p), max(p.Replicas, 1))
	case "stop":
		return m.docker.ServiceScale(ctx, ServiceName(p), 0)
	case "restart":
		return m.docker.ServiceRestart(ctx, ServiceName(p))
	}
	return fmt.Errorf("unknown action %q", action)
}

func (m *Manager) Logs(ctx context.Context, p *store.Project, tail int) (string, error) {
	if m.opts.Target == TargetSwarm {
		return m.docker.ServiceLogs(ctx, ServiceName(p), tail)
	}
	return m.docker.Logs(ctx, ContainerName(p), tail)
}

// Remove deletes what runs for a project (both targets, in case it moved) and its local images.
func (m *Manager) Remove(ctx context.Context, p *store.Project) error {
	if err := m.docker.StackRemove(ctx, StackName(p)); err != nil && m.opts.Target == TargetSwarm {
		return err
	}
	if err := m.docker.Remove(ctx, ContainerName(p)); err != nil && m.opts.Target != TargetSwarm {
		return err
	}
	_ = m.docker.Remove(ctx, ContainerName(p)+"-previous")
	m.docker.RemoveImages(ctx, m.ImageRepo(p))
	return nil
}
