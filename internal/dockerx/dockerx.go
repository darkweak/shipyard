// Package dockerx drives the docker CLI. Using the CLI rather than the Engine
// API keeps BuildKit features (heredocs, cache mounts) and the user's docker
// context/config working without extra plumbing.
package dockerx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Docker struct {
	Bin string
}

func New(bin string) *Docker {
	if bin == "" {
		bin = "docker"
	}
	return &Docker{Bin: bin}
}

func (d *Docker) cmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, d.Bin, args...)
	c.Env = append(os.Environ(), "DOCKER_BUILDKIT=1", "BUILDKIT_PROGRESS=plain")
	return c
}

// output runs a command and returns its trimmed stdout, or stderr as the error.
func (d *Docker) output(ctx context.Context, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	c := d.cmd(ctx, args...)
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("docker %s: %s", args[0], msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// ServerVersion doubles as a daemon reachability check.
func (d *Docker) ServerVersion(ctx context.Context) (string, error) {
	return d.output(ctx, "version", "--format", "{{.Server.Version}}")
}

// Build runs docker build. secretFile, when set, is exposed to RUN steps as the
// BuildKit secret "buildenv" (RUN --mount=type=secret,id=buildenv), so build
// time variables never end up in image layers.
func (d *Docker) Build(ctx context.Context, contextDir, dockerfile, tag, secretFile string, labels map[string]string, out io.Writer) error {
	args := []string{"build", "--progress=plain", "-f", dockerfile, "-t", tag}
	if secretFile != "" {
		args = append(args, "--secret", "id=buildenv,src="+secretFile)
	}
	for _, k := range sortedKeys(labels) {
		args = append(args, "--label", k+"="+labels[k])
	}
	args = append(args, contextDir)
	c := d.cmd(ctx, args...)
	c.Stdout, c.Stderr = out, out
	if err := c.Run(); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}
	return nil
}

type RunOptions struct {
	Name          string
	Image         string
	HostPort      int
	ContainerPort int
	Network       string
	Env           map[string]string
	Labels        map[string]string
	// Volumes are "name:/path" mounts (named volumes are created on first use).
	Volumes []string
	// WorkDir receives the temporary env file (kept out of argv).
	WorkDir string
}

func (d *Docker) Run(ctx context.Context, o RunOptions) (string, error) {
	args := []string{"run", "-d", "--name", o.Name, "--restart", "unless-stopped"}
	for _, k := range sortedKeys(o.Labels) {
		args = append(args, "--label", k+"="+o.Labels[k])
	}
	if o.HostPort > 0 && o.ContainerPort > 0 {
		args = append(args, "-p", fmt.Sprintf("%d:%d", o.HostPort, o.ContainerPort))
	}
	if o.Network != "" {
		args = append(args, "--network", o.Network)
	}
	for _, v := range o.Volumes {
		args = append(args, "-v", v)
	}
	if len(o.Env) > 0 {
		f := filepath.Join(o.WorkDir, "container.env")
		var b strings.Builder
		for _, k := range sortedKeys(o.Env) {
			b.WriteString(k + "=" + o.Env[k] + "\n")
		}
		if err := os.WriteFile(f, []byte(b.String()), 0o600); err != nil {
			return "", err
		}
		defer os.Remove(f)
		args = append(args, "--env-file", f)
	}
	args = append(args, o.Image)
	return d.output(ctx, args...)
}

func (d *Docker) Remove(ctx context.Context, name string) error {
	_, err := d.output(ctx, "rm", "-f", name)
	if err != nil && strings.Contains(err.Error(), "No such container") {
		return nil
	}
	return err
}

// RemoveVolume deletes a named volume; a missing volume is not an error.
func (d *Docker) RemoveVolume(ctx context.Context, name string) error {
	_, err := d.output(ctx, "volume", "rm", "-f", name)
	return err
}

func (d *Docker) Rename(ctx context.Context, from, to string) error {
	_, err := d.output(ctx, "rename", from, to)
	return err
}

// Action runs start, stop or restart on a container.
func (d *Docker) Action(ctx context.Context, name, action string) error {
	switch action {
	case "start", "stop", "restart":
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	_, err := d.output(ctx, action, name)
	return err
}

func (d *Docker) Logs(ctx context.Context, name string, tail int) (string, error) {
	var buf bytes.Buffer
	c := d.cmd(ctx, "logs", "--timestamps", "--tail", strconv.Itoa(tail), name)
	c.Stdout, c.Stderr = &buf, &buf
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("docker logs: %s", strings.TrimSpace(buf.String()))
	}
	return buf.String(), nil
}

func (d *Docker) ImageExists(ctx context.Context, image string) bool {
	_, err := d.output(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	return err == nil
}

// RemoveImages deletes every tag of repo. Images still used by a container are kept.
func (d *Docker) RemoveImages(ctx context.Context, repo string) {
	ids, err := d.output(ctx, "image", "ls", "-q", repo)
	if err != nil || ids == "" {
		return
	}
	_, _ = d.output(ctx, append([]string{"rmi", "-f"}, strings.Fields(ids)...)...)
}

type Port struct {
	ContainerPort string `json:"containerPort"`
	HostIP        string `json:"hostIp"`
	HostPort      string `json:"hostPort"`
}

type ContainerState struct {
	Exists       bool       `json:"exists"`
	ID           string     `json:"id,omitempty"`
	Image        string     `json:"image,omitempty"`
	State        string     `json:"state,omitempty"` // created, running, restarting, exited…
	Running      bool       `json:"running"`
	Restarting   bool       `json:"restarting"`
	ExitCode     int        `json:"exitCode"`
	RestartCount int        `json:"restartCount"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	Ports        []Port     `json:"ports"`
}

func (d *Docker) Inspect(ctx context.Context, name string) (*ContainerState, error) {
	out, err := d.output(ctx, "container", "inspect", name)
	if err != nil {
		if strings.Contains(err.Error(), "No such") {
			return &ContainerState{Ports: []Port{}}, nil
		}
		return nil, err
	}
	var raw []struct {
		ID           string `json:"Id"`
		RestartCount int
		Config       struct{ Image string }
		State        struct {
			Status     string
			Running    bool
			Restarting bool
			ExitCode   int
			StartedAt  time.Time
		}
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("docker inspect: unexpected output")
	}
	r := raw[0]
	st := &ContainerState{
		Exists: true, ID: r.ID, Image: r.Config.Image, State: r.State.Status,
		Running: r.State.Running, Restarting: r.State.Restarting, ExitCode: r.State.ExitCode,
		RestartCount: r.RestartCount, Ports: []Port{},
	}
	if !r.State.StartedAt.IsZero() {
		t := r.State.StartedAt
		st.StartedAt = &t
	}
	for cp, bindings := range r.NetworkSettings.Ports {
		for _, b := range bindings {
			st.Ports = append(st.Ports, Port{ContainerPort: cp, HostIP: b.HostIP, HostPort: b.HostPort})
		}
	}
	sort.Slice(st.Ports, func(i, j int) bool { return st.Ports[i].ContainerPort < st.Ports[j].ContainerPort })
	return st, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
