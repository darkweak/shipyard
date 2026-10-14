package templates

import (
	"context"
	"log/slog"

	"shipyard/internal/store"
)

const nodeIgnore = `.git
node_modules
.next
dist
build
coverage
npm-debug.log*
`

// Builtins are seeded once each (tracked in the meta table), so users can edit
// or delete them and new versions of Shipyard can still add more.
var Builtins = []store.Template{
	{
		ID:          "nextjs",
		Name:        "Next.js",
		Description: "Any Next.js app, served with `next start`. Works without changes to next.config.",
		DefaultPort: 3000,
		Variables: []store.Variable{
			{Name: "NODE_IMAGE", Default: "node:24-alpine", Description: "Base Node.js image"},
			{Name: "BUILD_COMMAND", Default: "npm run build", Description: "Command producing the production build"},
			{Name: "PORT", Default: "3000", Description: "Port the app listens on"},
		},
		Dockerignore: nodeIgnore,
		Dockerfile: `# syntax=docker/dockerfile:1
FROM {{ .NODE_IMAGE }} AS deps
WORKDIR /app
COPY package.json package-lock.json* pnpm-lock.yaml* yarn.lock* ./
RUN if [ -f pnpm-lock.yaml ]; then corepack enable pnpm && pnpm install --frozen-lockfile; \
    elif [ -f yarn.lock ]; then corepack enable yarn && yarn install --frozen-lockfile; \
    elif [ -f package-lock.json ]; then npm ci; \
    else npm install; fi

FROM {{ .NODE_IMAGE }} AS builder
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN {{ .BUILD_COMMAND }}

FROM {{ .NODE_IMAGE }}
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT={{ .PORT }} HOSTNAME=0.0.0.0
COPY --from=builder --chown=node:node /app ./
USER node
EXPOSE {{ .PORT }}
CMD ["npx", "next", "start"]
`,
	},
	{
		ID:          "nextjs-standalone",
		Name:        "Next.js (standalone)",
		Description: "Small image for apps with `output: 'standalone'` in next.config.",
		DefaultPort: 3000,
		Variables: []store.Variable{
			{Name: "NODE_IMAGE", Default: "node:24-alpine", Description: "Base Node.js image"},
			{Name: "BUILD_COMMAND", Default: "npm run build", Description: "Command producing the production build"},
			{Name: "PORT", Default: "3000", Description: "Port the app listens on"},
		},
		Dockerignore: nodeIgnore,
		Dockerfile: `# syntax=docker/dockerfile:1
FROM {{ .NODE_IMAGE }} AS deps
WORKDIR /app
COPY package.json package-lock.json* pnpm-lock.yaml* yarn.lock* ./
RUN if [ -f pnpm-lock.yaml ]; then corepack enable pnpm && pnpm install --frozen-lockfile; \
    elif [ -f yarn.lock ]; then corepack enable yarn && yarn install --frozen-lockfile; \
    elif [ -f package-lock.json ]; then npm ci; \
    else npm install; fi

FROM {{ .NODE_IMAGE }} AS builder
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN {{ .BUILD_COMMAND }}
# public/ is optional in Next.js projects.
RUN mkdir -p public

FROM {{ .NODE_IMAGE }}
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT={{ .PORT }} HOSTNAME=0.0.0.0
COPY --from=builder --chown=node:node /app/public ./public
COPY --from=builder --chown=node:node /app/.next/standalone ./
COPY --from=builder --chown=node:node /app/.next/static ./.next/static
USER node
EXPOSE {{ .PORT }}
CMD ["node", "server.js"]
`,
	},
	{
		ID:          "hugo",
		Name:        "Hugo website",
		Description: "Builds a Hugo site (extended edition, npm dependencies included) and serves it with nginx.",
		DefaultPort: 80,
		Variables: []store.Variable{
			{Name: "HUGO_IMAGE", Default: "ghcr.io/gohugoio/hugo:latest", Description: "Official Hugo image (extended); pin a version, e.g. ghcr.io/gohugoio/hugo:v0.168.0"},
			{Name: "BASE_URL", Default: "", Description: "Overrides baseURL from the site config (leave empty to keep it)"},
			{Name: "HUGO_ENV", Default: "production", Description: "Hugo environment"},
			{Name: "NGINX_IMAGE", Default: "nginx:alpine", Description: "Image serving the generated site"},
		},
		Dockerignore: "public\nresources/_gen\n.hugo_build.lock\n",
		Dockerfile: `# syntax=docker/dockerfile:1
# The official image runs as the "hugo" user in /project.
FROM {{ .HUGO_IMAGE }} AS builder
COPY --chown=hugo:hugo . /project
# Sites bundling JS/CSS (Tailwind, Alpine…) declare their tools in package.json.
RUN if [ -f package-lock.json ]; then npm ci; elif [ -f package.json ]; then npm install; fi
RUN hugo --gc --minify --environment {{ .HUGO_ENV }}{{ if .BASE_URL }} --baseURL "{{ .BASE_URL }}"{{ end }}

FROM {{ .NGINX_IMAGE }}
COPY --from=builder /project/public /usr/share/nginx/html
EXPOSE 80
`,
	},
	{
		ID:          "vite-spa",
		Name:        "Single-page app (Vite, CRA, …)",
		Description: "Builds a static front-end with npm and serves it with nginx, falling back to index.html for client-side routing.",
		DefaultPort: 80,
		Variables: []store.Variable{
			{Name: "NODE_IMAGE", Default: "node:24-alpine", Description: "Base Node.js image"},
			{Name: "BUILD_COMMAND", Default: "npm run build", Description: "Command producing the static files"},
			{Name: "OUTPUT_DIR", Default: "dist", Description: "Directory the build writes to"},
			{Name: "NGINX_IMAGE", Default: "nginx:alpine", Description: "Image serving the files"},
		},
		Dockerignore: nodeIgnore,
		Dockerfile: `# syntax=docker/dockerfile:1
FROM {{ .NODE_IMAGE }} AS builder
WORKDIR /app
COPY package.json package-lock.json* pnpm-lock.yaml* yarn.lock* ./
RUN if [ -f pnpm-lock.yaml ]; then corepack enable pnpm && pnpm install --frozen-lockfile; \
    elif [ -f yarn.lock ]; then corepack enable yarn && yarn install --frozen-lockfile; \
    elif [ -f package-lock.json ]; then npm ci; \
    else npm install; fi
COPY . .
RUN {{ .BUILD_COMMAND }}

FROM {{ .NGINX_IMAGE }}
COPY <<'EOF' /etc/nginx/conf.d/default.conf
server {
    listen 80;
    root /usr/share/nginx/html;
    location / {
        try_files $uri $uri/ /index.html;
    }
}
EOF
COPY --from=builder /app/{{ .OUTPUT_DIR }} /usr/share/nginx/html
EXPOSE 80
`,
	},
	{
		ID:          "static",
		Name:        "Static files",
		Description: "Serves a directory of the repository as-is with nginx.",
		DefaultPort: 80,
		Variables: []store.Variable{
			{Name: "SITE_DIR", Default: ".", Description: "Directory to publish, relative to the build context"},
			{Name: "NGINX_IMAGE", Default: "nginx:alpine", Description: "Image serving the files"},
		},
		Dockerignore: ".git\n",
		Dockerfile: `FROM {{ .NGINX_IMAGE }}
COPY {{ .SITE_DIR }} /usr/share/nginx/html
EXPOSE 80
`,
	},
	{
		ID:          "go",
		Name:        "Go service",
		Description: "Compiles a static Go binary and runs it on distroless.",
		DefaultPort: 8080,
		Variables: []store.Variable{
			{Name: "GO_IMAGE", Default: "golang:1.26-alpine", Description: "Go toolchain image"},
			{Name: "MAIN_PACKAGE", Default: ".", Description: "Package to build, e.g. ./cmd/server"},
			{Name: "PORT", Default: "8080", Description: "Exported as $PORT for the app"},
		},
		Dockerignore: ".git\n",
		Dockerfile: `# syntax=docker/dockerfile:1
FROM {{ .GO_IMAGE }} AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app {{ .MAIN_PACKAGE }}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/app /app
ENV PORT={{ .PORT }}
EXPOSE {{ .PORT }}
USER nonroot
ENTRYPOINT ["/app"]
`,
	},
	{
		ID:          "node",
		Name:        "Node.js server",
		Description: "Generic Node.js service (Express, Fastify, NestJS, Nuxt, Remix…).",
		DefaultPort: 3000,
		Variables: []store.Variable{
			{Name: "NODE_IMAGE", Default: "node:24-alpine", Description: "Base Node.js image"},
			{Name: "BUILD_COMMAND", Default: "", Description: "Optional build step, e.g. npm run build"},
			{Name: "START_COMMAND", Default: "npm start", Description: "Command starting the server"},
			{Name: "PORT", Default: "3000", Description: "Exported as $PORT for the app"},
		},
		Dockerignore: nodeIgnore,
		Dockerfile: `# syntax=docker/dockerfile:1
FROM {{ .NODE_IMAGE }}
WORKDIR /app
COPY package.json package-lock.json* pnpm-lock.yaml* yarn.lock* ./
RUN if [ -f pnpm-lock.yaml ]; then corepack enable pnpm && pnpm install --frozen-lockfile; \
    elif [ -f yarn.lock ]; then corepack enable yarn && yarn install --frozen-lockfile; \
    elif [ -f package-lock.json ]; then npm ci; \
    else npm install; fi
COPY . .
{{ if .BUILD_COMMAND }}RUN {{ .BUILD_COMMAND }}
{{ end -}}
ENV NODE_ENV=production PORT={{ .PORT }}
USER node
EXPOSE {{ .PORT }}
CMD {{ .START_COMMAND }}
`,
	},
}

// SeedBuiltins inserts every built-in template that was never seeded before.
func SeedBuiltins(ctx context.Context, s *store.Store, log *slog.Logger) error {
	for _, t := range Builtins {
		key := "builtin_template:" + t.ID
		_, seeded, err := s.GetMeta(ctx, key)
		if err != nil {
			return err
		}
		if seeded {
			continue
		}
		tpl := t
		tpl.Builtin = true
		if err := s.CreateTemplate(ctx, &tpl); err != nil {
			return err
		}
		if err := s.SetMeta(ctx, key, "1"); err != nil {
			return err
		}
		log.Info("seeded built-in template", "id", t.ID)
	}
	return nil
}
