# Shipyard

Deploy git repositories as Docker containers from a web UI. Each project points
at a repository (public, HTTPS token or SSH deploy key) and is built in one of
three ways:

- with the repository's own **Dockerfile**;
- with a **Dockerfile template** you manage in the UI (Next.js, Hugo, Vite SPA, Go, Node, static files… or your own);
- from the repository's **`netlify.toml`**, to move sites off Netlify without changing them (see below).

- **Backend**: Go (stdlib `net/http`, SQLite via the pure-Go `modernc.org/sqlite`), drives the `git` and `docker` CLIs.
- **Frontend**: React 19 + TypeScript + Tailwind v4 (Vite), embedded in the Go binary.

## Quick start

```sh
# Development: API on :8080 + Vite with hot reload on http://localhost:5173
make dev                       # admin token defaults to "dev-token"

# Single binary with the UI embedded
make build
SHIPYARD_TOKEN=change-me ./bin/shipyard   # http://localhost:8080

# Docker (uses the host's Docker daemon through the socket)
SHIPYARD_TOKEN=change-me docker compose up -d --build
```

Requirements: Go 1.26+, Node 22+, `git`, and a reachable Docker daemon with BuildKit (Docker 26+ for Swarm stacks).

## How a deployment works

1. **Clone** – shallow clone of the branch (submodules included) into `$SHIPYARD_DATA_DIR/builds/<id>`.
2. **Dockerfile** – the repository's file (`dockerfilePath`), the project's template rendered with its
   variable values, or one generated from `netlify.toml`. Generated Dockerfiles are printed in the deployment
   log. If the repository has no `.dockerignore`, the template's one is added.
3. **Build** – `docker build` tagged `shipyard/<slug>:<deployment-id>`, logs streamed live to the UI (SSE).
4. **Swap** – the running `shipyard-<slug>` container is stopped and kept aside, the new one starts
   (`--restart unless-stopped`, ports, env, network, labels). If the new container isn't still running after
   5 s, or anything fails, the previous container is restored. Otherwise it is removed.

One deployment runs per project at a time (others queue); `SHIPYARD_MAX_CONCURRENT_BUILDS` bounds builds
globally. Every successful deployment can be **redeployed** later (rollback) from its image, and running
deployments can be cancelled.

Project environment variables are passed to the container at runtime **and** exposed to the build as the
BuildKit secret `buildenv` (never stored in image layers). A Dockerfile can use them with
`RUN --mount=type=secret,id=buildenv sh -c '. /run/secrets/buildenv && npm run build'`.

## Migrating from Netlify

Choose the **Netlify** build type (the project form suggests it when *Check access* finds a `netlify.toml`
on the branch). Copy your Netlify site's environment variables (*Site configuration → Environment
variables*) into the project's environment variables: the build command sees them, as on Netlify.

**Build.** Each deploy reads `netlify.toml` from the base directory (the project's *Base directory*, or
`[build] base`) and applies the `production` context: `command`, `publish`, `[build.environment]` and
`[context.production]` overrides. The build runs in a Debian image, like Netlify's build image:

| Netlify feature                                   | Shipyard                                                                      |
|---------------------------------------------------|-------------------------------------------------------------------------------|
| `NODE_VERSION`, `.nvmrc`, `.node-version`         | Node.js of that version (`20`, `v18.17.0`, `lts/*`, `lts/iron`…), default 22  |
| `HUGO_VERSION` / a `hugo` build command           | Hugo extended from the official release (default 0.168.0 when unset)          |
| `GO_VERSION`, Hugo modules (`go.mod`)             | Go toolchain                                                                  |
| `Gemfile` / `requirements.txt`                    | Debian's Ruby (`bundle install`) / Python (`pip install`)                     |
| Dependency install                                | `npm install`, `yarn install`, `pnpm install` or `bun install` by lockfile, with `NPM_FLAGS`/`YARN_FLAGS`/`PNPM_FLAGS`/`BUN_FLAGS` |
| Read-only variables                               | `NETLIFY`, `CONTEXT=production`, `BRANCH`, `HEAD`, `COMMIT_REF`, `BUILD_ID`, `DEPLOY_ID`, `SITE_NAME` |

Precedence follows Netlify: `netlify.toml` environment > project (UI) variables.

**Serving.** The publish directory is served by a small Go server (`internal/netlify/server`, compiled
during the build, ~6 MB on distroless) that behaves like Netlify's CDN:

- `_redirects` and `[[redirects]]` (in that order): 301/302/303/307/308 redirects, `200` rewrites
  (SPA fallbacks such as `/* /index.html 200`), proxies to external URLs (with rule `headers`), custom
  `404` rules, splats (`*` → `:splat`), `:placeholders`, query parameter matching, `force` / `!`,
  shadowing (existing files win over non-forced rules), domain rules (`https://old.example.com/*`), query
  string pass-through, `Language` and `Cookie` conditions;
- `_headers` and `[[headers]]` with wildcard patterns (`/*.jpg`), and Netlify's default
  `Cache-Control: public, max-age=0, must-revalidate`;
- pretty URLs (`/about` → `about.html`, `/docs` → `/docs/`), `404.html`; `_redirects` / `_headers` are not served.

The container listens on port 8080.

**Netlify Functions.** JavaScript/TypeScript functions in `netlify/functions` (or `[functions] directory` /
`[build] functions`) — `<name>.{js,mjs,cjs,ts,mts,cts}`, `<name>/<name>.<ext>` or `<name>/index.<ext>` — are
bundled with esbuild (like Netlify) and run by a Node.js runtime of the site's version, started and
supervised by the static server in the same container:

- Lambda-style handlers (`exports.handler = async (event, context) => ({ statusCode, body })`, callbacks,
  `isBase64Encoded`, `multiValueHeaders`) and Request/Response handlers (`export default async (req, context)`,
  `context.params`, `context.cookies`, `context.ip`, `Netlify.env`, `Netlify.context`);
- `/.netlify/functions/<name>[/…]`, rewrites to it (`/api/* /.netlify/functions/api/:splat 200`; the Lambda
  event sees the rewritten path, Request handlers the original URL), and `config.path` routes with
  `:params`, `*`, `excludedPath`, `method` and `preferStatic`;
- background functions (`-background` suffix → 202, up to 15 min) and scheduled functions
  (`config.schedule` or `[functions."name"] schedule`, cron in UTC, not invocable over HTTP);
- `[functions]` and `[functions."pattern"]`: `external_node_modules` (node_modules is then shipped) and
  `included_files` (relative to the base directory, the functions' working directory);
- the project's environment variables (Netlify's "Functions" scope); synchronous functions time out
  after 30 s (`SHIPYARD_FUNCTIONS_TIMEOUT_MS` in the container).

With functions, the runtime image is the site's Node.js image instead of distroless.

**Netlify Blobs.** `@netlify/blobs` works unchanged in functions (`getStore`, `getDeployStore`, `listStores`,
metadata, `onlyIfNew` / `onlyIfMatch`, ETag revalidation, `consistency: "strong"`, `list({ directories })`,
`deleteAll`; Lambda-style handlers call `connectLambda(event)` as on Netlify). The static server answers the
Blobs edge API on loopback and passes `NETLIFY_BLOBS_CONTEXT` to the runtime. Blobs are files in the
`/blobs` directory of the container, on the named volume `shipyard-<slug>-blobs`: they survive redeploys and
rollbacks, and are removed with the project. Blobs are limited to 64 MB each. On Swarm the volume is local to
each node, so replicas on different nodes don't share blobs: pin the service to one node (placement
constraint) or keep one replica. Data from Netlify isn't migrated: copy it with a function or a script.

**Not supported** (reported as warnings in the form and the deployment log): Go/Rust functions, Edge
Functions, streaming via `stream()` from `@netlify/functions` older APIs, `context.geo`, build plugins (for `@netlify/plugin-nextjs` use the Next.js
template instead), `Country`/`Role` redirect conditions (those rules never match), signed proxies, Forms,
Identity, and non-production contexts (deploy previews, branch deploys). HTTPS and compression are left to
your reverse proxy.

## Picking repositories and folders (GitHub)

With **Access token** selected, paste a GitHub personal access token and click **Browse repositories**: the
repositories the token can reach (yours, collaborations, organizations; up to 1000, most recently pushed
first) are listed through the GitHub API, and picking one fills the URL, default branch and project name.
A fine-grained token needs *Contents: read* (and *Metadata: read*) on those repositories. For GitHub
Enterprise Server set `SHIPYARD_GITHUB_API_URL=https://<host>/api/v3`.

**Browse…** next to *Base directory* / *Build context* lists the branch's folders, tagged with what they
contain (Netlify, npm, Dockerfile, Hugo, Go…), to pick a site in a monorepo. It works with any git host and
credential type: Shipyard fetches only the commit's trees (a blobless partial clone), not file contents.

## Docker Swarm

When Shipyard runs on a **Swarm manager** (`SHIPYARD_TARGET=auto`, the default, detects it; or force
`swarm` / `docker`), each project becomes a stack `shipyard-<slug>` with one service, `app`:

- built images are pushed to `SHIPYARD_REGISTRY` so every node can pull them (without a registry the
  service is pinned to the manager);
- `docker stack deploy` with rolling **start-first** updates and `failure_action: rollback`; Shipyard follows
  the rollout (tasks per node, update state) and reports Swarm rollbacks as failed deployments; a failed
  first deployment removes the stack;
- per project: **replicas**, **placement constraints**, published port (routing mesh, every node), an
  external overlay **network** and **labels** (set on the service *and* the containers, for Traefik /
  caddy-docker-proxy in Swarm mode);
- start/stop become scale to N / 0, restart is a rolling `service update --force`, logs are `service logs`.

`deploy/stack.yml` runs Shipyard on the manager next to a `registry:2` service published on port 5000
through the routing mesh (every node pulls `127.0.0.1:5000/…`, which Docker accepts over plain HTTP because
it is a loopback address):

```sh
docker build -t shipyard:latest .            # on the manager
printf '%s' "$(openssl rand -hex 24)" | docker secret create shipyard_token -
docker stack deploy -c deploy/stack.yml shipyard
```

To use an external registry instead, set `SHIPYARD_REGISTRY=registry.example.com/team` and
`SHIPYARD_REGISTRY_USERNAME` / `SHIPYARD_REGISTRY_PASSWORD(_FILE)` (Shipyard runs `docker login` at startup).
Images left in the registry are not garbage-collected by Shipyard.

## Dockerfile templates

Templates are [Go templates](https://pkg.go.dev/text/template). Declare variables (name, default,
description) and use them anywhere:

```dockerfile
FROM {{ .NODE_IMAGE }}
{{ if .BUILD_COMMAND }}RUN {{ .BUILD_COMMAND }}{{ end }}
EXPOSE {{ .PORT }}
```

Projects override variable values; empty values fall back to the defaults. Using an undeclared variable is
an error, caught by the live preview. Template changes apply to projects on their next deploy.

Built-ins (seeded once, editable/deletable): `Next.js`, `Next.js (standalone)`, `Hugo website`,
`Single-page app`, `Static files`, `Go service`, `Node.js server`.

## Repository access

| Access       | URL form                          | Secret                                                        |
|--------------|-----------------------------------|---------------------------------------------------------------|
| Public       | `https://…`                       | –                                                             |
| Access token | `https://…`                       | PAT / deploy token (username: GitHub any, GitLab `oauth2`, Bitbucket `x-token-auth`) |
| SSH key      | `git@host:owner/repo.git`, `ssh://…` | Private key whose public half is a read-only deploy key    |

**Check access** in the project form runs `git ls-remote` and fills the branch list. Credentials are passed to
git through environment variables (never the URL or argv), the host user's git config is ignored, and only
`https`/`http`/`ssh` transports are allowed. SSH host keys are trusted on first use and pinned in
`$SHIPYARD_DATA_DIR/known_hosts`. Secrets are write-only through the API and masked in build logs.

## Deploy on push

Each project has a webhook URL (`/hooks/<project-id>/<secret>`, shown in the UI). Add it as a push webhook on
GitHub, GitLab, Gitea/Forgejo or Bitbucket. Pushes to other branches are ignored (based on the payload's
`ref`), GitHub pings get a `pong`. The secret can be regenerated.

## Configuration

| Variable                         | Default    | Description                                                   |
|----------------------------------|------------|---------------------------------------------------------------|
| `SHIPYARD_TOKEN`                 | random     | Admin token for the UI/API. Printed at startup when unset.    |
| `SHIPYARD_TOKEN_FILE`            | –          | Read the token from a file (Docker/Swarm secret)              |
| `SHIPYARD_TARGET`                | `auto`     | `docker`, `swarm`, or `auto` (swarm on a Swarm manager)       |
| `SHIPYARD_REGISTRY`              | –          | Registry prefix images are pushed to (Swarm), e.g. `127.0.0.1:5000` |
| `SHIPYARD_REGISTRY_USERNAME` / `_PASSWORD` / `_PASSWORD_FILE` | – | Registry credentials (`docker login` at startup) |
| `SHIPYARD_GITHUB_API_URL`        | `https://api.github.com` | GitHub API, for GitHub Enterprise Server        |
| `SHIPYARD_ADDR`                  | `:8080`    | Listen address                                                |
| `SHIPYARD_DATA_DIR`              | `./data`   | SQLite database, known_hosts, build workspaces                |
| `SHIPYARD_MAX_CONCURRENT_BUILDS` | `2`        | Builds running at the same time across projects               |
| `SHIPYARD_DOCKER_BIN`            | `docker`   | Docker CLI to use (honours `DOCKER_HOST` / docker contexts)   |
| `SHIPYARD_SECURE_COOKIES`        | `false`    | Set to `true` when served over HTTPS                          |
| `SHIPYARD_DEBUG`                 | –          | Log every API request                                         |

## Security notes

Shipyard runs arbitrary Dockerfiles against your Docker daemon, which is root-equivalent on the host.
Keep it behind a private network or an authenticating reverse proxy with TLS, use a long `SHIPYARD_TOKEN`,
and prefer read-only deploy keys / fine-grained tokens. Repository secrets and environment variables are
stored in plain text in the SQLite database (`0700` data directory).

## API

All under `/api`, authenticated by the session cookie (`POST /api/login {"token": …}`) or
`Authorization: Bearer <token>`.

```
GET/POST        /api/templates               POST /api/templates/preview
GET/PUT/DELETE  /api/templates/{id}
GET/POST        /api/projects                POST /api/git/branches   POST /api/git/inspect
POST            /api/git/dirs                POST /api/github/repos
GET/PUT/DELETE  /api/projects/{id}
POST            /api/projects/{id}/deploy    POST /api/projects/{id}/webhook/rotate
GET             /api/projects/{id}/deployments
GET             /api/projects/{id}/container        POST /api/projects/{id}/container/{start|stop|restart}
GET             /api/projects/{id}/container/logs?tail=300
GET             /api/deployments/{id}        GET  /api/deployments/{id}/logs   (SSE)
POST            /api/deployments/{id}/cancel POST /api/deployments/{id}/redeploy
POST            /hooks/{projectId}/{secret}  (no auth: webhook)
GET             /healthz
```

## Layout

```
main.go                  wiring, config, graceful shutdown
internal/store           SQLite persistence (templates, projects, deployments)
internal/templates       template rendering + built-in templates
internal/netlify         netlify.toml → build plan + generated Dockerfile
internal/netlify/server  Netlify-compatible static server (compiled into Netlify-mode images)
internal/netlify/functions  Netlify Functions runtime for Node.js (runtime.mjs + node tests)
internal/github          GitHub API client (repository listing)
deploy/stack.yml         Shipyard + registry on Docker Swarm
internal/deploy          clone → build → swap pipeline, queueing, cancel, rollback
internal/gitx            git CLI wrapper (token / SSH auth, ls-remote)
internal/dockerx         docker CLI wrapper (build, run, inspect, logs)
internal/logstream       live log fan-out for SSE
internal/api             REST API, webhook, SPA serving
web/                     React UI (embedded via web/embed.go)
```
