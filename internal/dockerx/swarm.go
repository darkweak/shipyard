package dockerx

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// SwarmInfo describes the node Shipyard talks to.
type SwarmInfo struct {
	State    string `json:"state"` // inactive, active, pending…
	Manager  bool   `json:"manager"`
	NodeID   string `json:"nodeId"`
	Hostname string `json:"hostname"` // this node's name (docker info .Name)
	Nodes    int    `json:"nodes"`
	Managers int    `json:"managers"`
}

func (d *Docker) SwarmInfo(ctx context.Context) (*SwarmInfo, error) {
	out, err := d.output(ctx, "info", "--format", `{"Swarm":{{json .Swarm}},"Name":{{json .Name}}}`)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Name  string
		Swarm struct {
			LocalNodeState   string
			ControlAvailable bool
			NodeID           string
			Nodes            int
			Managers         int
		}
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("docker info: %w", err)
	}
	sw := raw.Swarm
	return &SwarmInfo{State: sw.LocalNodeState, Manager: sw.ControlAvailable, NodeID: sw.NodeID, Hostname: raw.Name, Nodes: sw.Nodes, Managers: sw.Managers}, nil
}

// Login authenticates the daemon against a registry (password via stdin).
func (d *Docker) Login(ctx context.Context, registry, user, password string) error {
	c := d.cmd(ctx, "login", "--username", user, "--password-stdin", registry)
	c.Stdin = strings.NewReader(password)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("docker login %s: %s", registry, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *Docker) Push(ctx context.Context, ref string, out io.Writer) error {
	c := d.cmd(ctx, "push", ref)
	c.Stdout, c.Stderr = out, out
	if err := c.Run(); err != nil {
		return fmt.Errorf("docker push failed: %w", err)
	}
	return nil
}

// StackDeploy creates or updates a stack from a compose file.
func (d *Docker) StackDeploy(ctx context.Context, name, file string, out io.Writer) error {
	c := d.cmd(ctx, "stack", "deploy", "--compose-file", file, "--with-registry-auth", "--prune", "--detach=true", name)
	c.Stdout, c.Stderr = out, out
	if err := c.Run(); err != nil {
		return fmt.Errorf("docker stack deploy failed: %w", err)
	}
	return nil
}

func (d *Docker) StackRemove(ctx context.Context, name string) error {
	_, err := d.output(ctx, "stack", "rm", name)
	if err != nil && strings.Contains(err.Error(), "Nothing found in stack") {
		return nil
	}
	return err
}

type ServicePort struct {
	TargetPort    int    `json:"targetPort"`
	PublishedPort int    `json:"publishedPort"`
	PublishMode   string `json:"publishMode"`
	Protocol      string `json:"protocol"`
}

type Service struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Image         string        `json:"image"`
	Replicas      int           `json:"replicas"`
	UpdateState   string        `json:"updateState,omitempty"` // updating, completed, rollback_*, paused
	UpdateMessage string        `json:"updateMessage,omitempty"`
	Ports         []ServicePort `json:"ports"`
	UpdatedAt     *time.Time    `json:"updatedAt,omitempty"`
}

// ServiceInspect returns nil (no error) when the service doesn't exist.
func (d *Docker) ServiceInspect(ctx context.Context, name string) (*Service, error) {
	out, err := d.output(ctx, "service", "inspect", name)
	if err != nil {
		if strings.Contains(err.Error(), "no such service") || strings.Contains(err.Error(), "No such") {
			return nil, nil
		}
		return nil, err
	}
	var raw []struct {
		ID        string
		UpdatedAt time.Time
		Spec      struct {
			Name         string
			TaskTemplate struct {
				ContainerSpec struct{ Image string }
			}
			Mode struct {
				Replicated *struct{ Replicas int }
			}
		}
		UpdateStatus *struct {
			State   string
			Message string
		}
		Endpoint struct {
			Ports []struct {
				Protocol      string
				TargetPort    int
				PublishedPort int
				PublishMode   string
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("docker service inspect: unexpected output")
	}
	r := raw[0]
	s := &Service{ID: r.ID, Name: r.Spec.Name, Image: r.Spec.TaskTemplate.ContainerSpec.Image, Ports: []ServicePort{}}
	if r.Spec.Mode.Replicated != nil {
		s.Replicas = r.Spec.Mode.Replicated.Replicas
	}
	if r.UpdateStatus != nil {
		s.UpdateState, s.UpdateMessage = r.UpdateStatus.State, r.UpdateStatus.Message
	}
	if !r.UpdatedAt.IsZero() {
		t := r.UpdatedAt
		s.UpdatedAt = &t
	}
	for _, p := range r.Endpoint.Ports {
		s.Ports = append(s.Ports, ServicePort{TargetPort: p.TargetPort, PublishedPort: p.PublishedPort, PublishMode: p.PublishMode, Protocol: p.Protocol})
	}
	return s, nil
}

type Task struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Image        string `json:"image"`
	Node         string `json:"node"`
	DesiredState string `json:"desiredState"`
	CurrentState string `json:"currentState"`
	Error        string `json:"error,omitempty"`
}

// Running reports whether the task's current state is running.
func (t Task) Running() bool { return strings.HasPrefix(t.CurrentState, "Running") }

// Failed reports whether the task ended in error.
func (t Task) Failed() bool {
	return strings.HasPrefix(t.CurrentState, "Failed") || strings.HasPrefix(t.CurrentState, "Rejected")
}

// ServiceTasks lists the service's tasks, most recent first.
func (d *Docker) ServiceTasks(ctx context.Context, name string) ([]Task, error) {
	out, err := d.output(ctx, "service", "ps", "--no-trunc", "--format", "{{json .}}", name)
	if err != nil {
		return nil, err
	}
	tasks := []Task{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var t Task
		var raw struct{ ID, Name, Image, Node, DesiredState, CurrentState, Error string }
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			continue
		}
		t = Task{ID: raw.ID, Name: strings.TrimLeft(raw.Name, " \\_"), Image: raw.Image, Node: raw.Node,
			DesiredState: raw.DesiredState, CurrentState: raw.CurrentState, Error: raw.Error}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (d *Docker) ServiceScale(ctx context.Context, name string, replicas int) error {
	_, err := d.output(ctx, "service", "scale", "--detach", name+"="+strconv.Itoa(replicas))
	return err
}

// ServiceRestart replaces every task (rolling, following update_config).
func (d *Docker) ServiceRestart(ctx context.Context, name string) error {
	_, err := d.output(ctx, "service", "update", "--force", "--detach", name)
	return err
}

func (d *Docker) ServiceRollback(ctx context.Context, name string) error {
	_, err := d.output(ctx, "service", "rollback", "--detach", name)
	return err
}

func (d *Docker) ServiceLogs(ctx context.Context, name string, tail int) (string, error) {
	var buf strings.Builder
	c := d.cmd(ctx, "service", "logs", "--timestamps", "--no-task-ids", "--tail", strconv.Itoa(tail), name)
	c.Stdout, c.Stderr = &buf, &buf
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("docker service logs: %s", strings.TrimSpace(buf.String()))
	}
	return buf.String(), nil
}
