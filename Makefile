.PHONY: dev dev-api dev-ui ui build test docker run clean

BIN := bin/shipyard
export SHIPYARD_TOKEN ?= dev-token

## dev: API on :8080 + Vite dev server with hot reload on :5173
dev:
	@$(MAKE) -j2 dev-api dev-ui

dev-api:
	SHIPYARD_DATA_DIR=./data go run .

dev-ui: web/node_modules
	cd web && npm run dev

web/node_modules: web/package.json
	cd web && npm install
	@touch $@

## ui: build the React app into web/dist (embedded by the Go binary)
ui: web/node_modules
	cd web && npm run build

## build: single self-contained binary with the UI embedded
build: ui
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) .

## run: build and start the binary
run: build
	SHIPYARD_DATA_DIR=./data ./$(BIN)

test:
	go vet ./...
	go test ./...
	node --test internal/netlify/functions/runtime.test.mjs
	cd web && npm run typecheck

docker:
	docker build -t shipyard:latest .

clean:
	rm -rf bin web/dist/assets web/dist/index.html
