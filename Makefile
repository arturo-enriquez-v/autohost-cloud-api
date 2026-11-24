SHELL := /bin/bash
.DEFAULT_GOAL := help

BINARY    := autohost-cloud-api
PLATFORMS := linux/amd64 linux/arm64
AIR       := $(shell go env GOPATH)/bin/air
AGENT_DIR := ../autohost-agent

# Scalar Registry entry behind the hosted MCP server (mcp.scalar.com/@<namespace>/<slug>).
SCALAR           := npx -y @scalar/cli@2.21.0
SCALAR_NAMESPACE ?= default-team-vpudn
SCALAR_SLUG      ?= autohost-cloud-api

# free-ports <ports>: kill whatever listens on them (a previous dev server).
free-ports = for p in $(1); do pids=$$(lsof -t -i :$$p 2>/dev/null); [ -z "$$pids" ] || kill -9 $$pids; done

.PHONY: help dev dev-rest dev-grpc dev-split services services-down \
	migrate-up migrate-down migrate-version migrate-force \
	proto build build-all test docker docker-multiarch clean mcp-update

help: ## Show this help
	@awk 'BEGIN{FS=":.*## "} /^##@/{printf "\n\033[1m%s\033[0m\n", substr($$0,5)} \
		/^[a-zA-Z0-9_%-]+:.*## /{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

##@ Development (live reload with Air)

dev: services ## REST + gRPC in one process (:8080, :9090)
	@$(call free-ports,8080 9090)
	@mkdir -p tmp && SERVICE_MODE=all $(AIR) -c .air.toml

dev-rest: services ## REST only (:8080), like the Cloud Run deployment
	@$(call free-ports,8080)
	@mkdir -p tmp/http && PORT=8080 SERVICE_MODE=http $(AIR) -c .air.http.toml

dev-grpc: services ## gRPC only (:9090), like the VPS deployment
	@$(call free-ports,9090)
	@mkdir -p tmp/grpc && GRPC_PORT=9090 SERVICE_MODE=grpc $(AIR) -c .air.grpc.toml

dev-split: ## REST and gRPC as separate processes, in parallel
	@$(MAKE) --no-print-directory -j2 dev-rest dev-grpc

services: ## Start Postgres (docker compose)
	@docker compose up -d

services-down: ## Stop docker compose services
	docker compose down

##@ Database

migrate-up: ## Apply pending migrations
	go run ./cmd/migrate up

migrate-down: ## Roll back the last migration
	go run ./cmd/migrate down 1

migrate-version: ## Show the current migration version
	go run ./cmd/migrate version

migrate-force: ## Force the migration version after a failed run (VERSION=<n>)
	@[ -n "$(VERSION)" ] || { echo "Usage: make migrate-force VERSION=<n>"; exit 1; }
	go run ./cmd/migrate force $(VERSION)

##@ Build

proto: ## Regenerate gRPC code from proto/
	protoc --go_out=. --go_opt=module=github.com/arturo/autohost-cloud-api \
		--go-grpc_out=. --go-grpc_opt=module=github.com/arturo/autohost-cloud-api \
		proto/node_agent/v1/node_agent.proto

build: ## Build bin/autohost-cloud-api
	go build -o bin/$(BINARY) ./cmd/api

build-all: ## Cross-compile linux/amd64 and linux/arm64 into bin/
	@for p in $(PLATFORMS); do os=$${p%/*}; arch=$${p#*/}; out=bin/$(BINARY)-$$os-$$arch; \
		echo "  → $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags "-s -w" -o $$out ./cmd/api || exit 1; \
	done

test: ## Run the tests
	go test ./...

docker: ## Build the Docker image
	docker build -t $(BINARY) .

docker-multiarch: ## Build the Docker image for amd64 and arm64
	docker buildx build --platform linux/amd64,linux/arm64 -t $(BINARY):latest .

clean: ## Remove build output
	rm -rf bin tmp

##@ API docs

mcp-update: ## Check and publish api/openapi.yaml to Scalar, updating the MCP server (VERSION=x.y.z, default next patch)
	@echo "==> Checking the spec"
	@go test ./internal/server -run 'TestOpenAPI' -count=1 >/dev/null || { echo "OpenAPI tests failed (go test ./internal/server -run TestOpenAPI)"; exit 1; }
	@npx --no-install @redocly/cli lint api/openapi.yaml >/dev/null 2>&1 || { echo "Spec lint failed (npx @redocly/cli lint api/openapi.yaml)"; exit 1; }
	@current=$$($(SCALAR) registry list --namespace $(SCALAR_NAMESPACE) --json --no-input | \
		node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const e=JSON.parse(s).find(x=>x.slug===process.argv[1]);console.log(e?e.version:"")})' $(SCALAR_SLUG)); \
	version="$(VERSION)"; \
	if [ -z "$$version" ]; then \
		[[ "$$current" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$$ ]] || { echo "Cannot bump registry version '$$current'; pass VERSION=x.y.z"; exit 1; }; \
		version="$${BASH_REMATCH[1]}.$${BASH_REMATCH[2]}.$$(( BASH_REMATCH[3] + 1 ))"; \
	fi; \
	[[ "$$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$$ ]] || { echo "Invalid version '$$version' (expected x.y.z)"; exit 1; }; \
	echo "==> Publishing $(SCALAR_NAMESPACE)/$(SCALAR_SLUG) $${current:-none} -> $$version"; \
	$(SCALAR) registry publish api/openapi.yaml --namespace $(SCALAR_NAMESPACE) --slug $(SCALAR_SLUG) \
		--version "$$version" --private --no-input && \
	echo "Published $$version: https://mcp.scalar.com/@$(SCALAR_NAMESPACE)/$(SCALAR_SLUG) now serves it"

##@ Lab (runs in ../autohost-agent, see `make -C ../autohost-agent help`)

lab-%: ## Any lab-* target, e.g. lab-create, lab-deploy, lab-update
	@$(MAKE) --no-print-directory -C $(AGENT_DIR) $@

node-%: ## Any node-* target, e.g. node-update, node-setup
	@$(MAKE) --no-print-directory -C $(AGENT_DIR) $@
