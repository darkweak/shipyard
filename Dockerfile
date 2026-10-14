# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM node:24-alpine AS ui
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS server
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/shipyard .

# Runtime needs git (+ ssh for deploy keys) and the docker CLI with buildx;
# the daemon itself is the host's, reached through the mounted socket.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates git openssh-client docker-cli docker-cli-buildx
COPY --from=server /out/shipyard /usr/local/bin/shipyard
ENV SHIPYARD_ADDR=:8080 SHIPYARD_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["shipyard"]
