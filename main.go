// Shipyard builds git repositories into Docker images using Dockerfile
// templates and runs them as containers, driven from a web UI.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"shipyard/internal/api"
	"shipyard/internal/deploy"
	"shipyard/internal/dockerx"
	"shipyard/internal/github"
	"shipyard/internal/gitx"
	"shipyard/internal/logstream"
	"shipyard/internal/store"
	"shipyard/internal/templates"
	"shipyard/web"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	level := slog.LevelInfo
	if os.Getenv("SHIPYARD_DEBUG") != "" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// targetOptions picks where projects run: SHIPYARD_TARGET=docker|swarm, or
// auto (swarm when this daemon is a Swarm manager).
func targetOptions(ctx context.Context, docker *dockerx.Docker, log *slog.Logger) (deploy.Options, error) {
	o := deploy.Options{Target: env("SHIPYARD_TARGET", "auto"), Registry: strings.TrimSuffix(os.Getenv("SHIPYARD_REGISTRY"), "/")}
	info, infoErr := docker.SwarmInfo(ctx)
	switch o.Target {
	case "auto":
		o.Target = deploy.TargetDocker
		if infoErr == nil && info.State == "active" && info.Manager {
			o.Target = deploy.TargetSwarm
		}
	case deploy.TargetDocker, deploy.TargetSwarm:
	default:
		return o, fmt.Errorf("SHIPYARD_TARGET must be auto, docker or swarm (got %q)", o.Target)
	}
	if o.Target == deploy.TargetSwarm {
		if infoErr != nil || info.State != "active" || !info.Manager {
			return o, fmt.Errorf("SHIPYARD_TARGET=swarm needs a Swarm manager node (docker swarm init / run Shipyard on a manager)")
		}
		o.NodeID = info.NodeID
		log.Info("deploying to Docker Swarm", "nodes", info.Nodes, "managers", info.Managers, "registry", o.Registry)
		if o.Registry == "" && info.Nodes > 1 {
			log.Warn("SHIPYARD_REGISTRY is not set: services are pinned to this node because other nodes can't pull locally built images")
		}
	} else {
		log.Info("deploying to the local Docker daemon")
	}
	if user := os.Getenv("SHIPYARD_REGISTRY_USERNAME"); user != "" && o.Registry != "" {
		password := os.Getenv("SHIPYARD_REGISTRY_PASSWORD")
		if f := os.Getenv("SHIPYARD_REGISTRY_PASSWORD_FILE"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return o, err
			}
			password = strings.TrimSpace(string(b))
		}
		host, _, _ := strings.Cut(o.Registry, "/")
		if err := docker.Login(ctx, host, user, password); err != nil {
			return o, err
		}
		log.Info("logged in to the registry", "registry", host)
	}
	return o, nil
}

func run(log *slog.Logger) error {
	addr := env("SHIPYARD_ADDR", ":8080")
	dataDir := env("SHIPYARD_DATA_DIR", "./data")
	maxBuilds, _ := strconv.Atoi(env("SHIPYARD_MAX_CONCURRENT_BUILDS", "2"))

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	dataDir, _ = filepath.Abs(dataDir)

	token := os.Getenv("SHIPYARD_TOKEN")
	if f := os.Getenv("SHIPYARD_TOKEN_FILE"); token == "" && f != "" { // Docker/Swarm secrets
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("SHIPYARD_TOKEN_FILE: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		token = hex.EncodeToString(b)
		log.Warn("SHIPYARD_TOKEN is not set; generated a one-time admin token (set SHIPYARD_TOKEN to keep it across restarts)",
			"token", token)
	}

	st, err := store.Open(filepath.Join(dataDir, "shipyard.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if n, err := st.FailInterrupted(ctx); err != nil {
		return err
	} else if n > 0 {
		log.Warn("marked deployments interrupted by the previous shutdown as failed", "count", n)
	}
	if err := templates.SeedBuiltins(ctx, st, log); err != nil {
		return err
	}

	docker := dockerx.New(os.Getenv("SHIPYARD_DOCKER_BIN"))
	if v, err := docker.ServerVersion(ctx); err != nil {
		log.Warn("docker is not reachable; deployments will fail until it is", "err", err)
	} else {
		log.Info("connected to docker", "version", v)
	}
	opts, err := targetOptions(ctx, docker, log)
	if err != nil {
		return err
	}

	workDir := filepath.Join(dataDir, "builds")
	_ = os.RemoveAll(workDir) // leftovers from a crash
	git := &gitx.Client{KnownHosts: filepath.Join(dataDir, "known_hosts"), TempDir: filepath.Join(dataDir, "tmp")}
	hub := logstream.NewHub()
	opts.WorkDir, opts.MaxConcurrent = workDir, maxBuilds
	mgr := deploy.NewManager(st, docker, git, hub, log, opts)

	var ui fs.FS
	if sub, err := fs.Sub(web.Dist, "dist"); err == nil {
		if _, err := fs.Stat(sub, "index.html"); err == nil {
			ui = sub
		}
	}
	if ui == nil {
		log.Warn("web UI is not embedded in this binary; run `make build`")
	}

	srv := &http.Server{
		Addr: addr,
		Handler: (&api.Server{
			Store: st, Deploy: mgr, Docker: docker, Git: git, Hub: hub, Log: log,
			GitHub: github.New(os.Getenv("SHIPYARD_GITHUB_API_URL")),
			Token: token, UI: ui, SecureCookies: os.Getenv("SHIPYARD_SECURE_COOKIES") == "true",
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("shipyard listening", "addr", addr, "data", dataDir)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down; waiting for running deployments")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	mgr.Wait()
	return nil
}
