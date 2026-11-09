SHELL := bash
.DEFAULT_GOAL := help

COMPOSE_DIR  := deploy/compose
COMPOSE_ENV  := --env-file $(COMPOSE_DIR)/.env
INFRA        := docker compose $(COMPOSE_ENV) -f $(COMPOSE_DIR)/docker-compose.infra.yml
OBS          := docker compose $(COMPOSE_ENV) -f $(COMPOSE_DIR)/docker-compose.infra.yml -f $(COMPOSE_DIR)/docker-compose.observability.yml
STACK        := $(OBS) -f $(COMPOSE_DIR)/docker-compose.services.yml

# Local runs of the services against the infra started by `make obs-up`.
LOCAL_ENV   := LOG_FORMAT=text LOG_LEVEL=debug \
               OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 OTEL_METRIC_EXPORT_INTERVAL=10000
EVENT_ENV   := $(LOCAL_ENV) DB_URL=postgres://event:event@localhost:5432/event?sslmode=disable \
               HTTP_ADDR=:8081 GRPC_ADDR=:9000
BOOKING_ENV := $(LOCAL_ENV) DB_URL=postgres://booking:booking@localhost:5432/booking?sslmode=disable \
               HTTP_ADDR=:8082 EVENT_GRPC_ADDR=localhost:9000 REDIS_ADDR=localhost:6379 REDIS_PASSWORD=redis

BUF_VERSION                := v1.57.2
PROTOC_GEN_GO_VERSION      := v1.36.9
PROTOC_GEN_GO_GRPC_VERSION := v1.5.1

# The race detector needs cgo (a C compiler). Enable it only when gcc exists,
# so `make test` also works on Windows machines without a toolchain. CI always has it.
RACE ?= $(if $(shell command -v gcc 2>/dev/null),-race,)

# Docker Desktop on Windows: the Ryuk reaper container of testcontainers fails to
# start, and Docker 26 rejects the newer API version the client asks for.
ifeq ($(OS),Windows_NT)
export TESTCONTAINERS_RYUK_DISABLED ?= true
export DOCKER_API_VERSION ?= 1.45
endif

GOLANGCI_LINT_VERSION := v2.4.0
GOVULNCHECK_VERSION   := v1.1.4

##@ General

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: fmt
fmt: ## Format code (gofumpt + goimports via golangci-lint)
	golangci-lint fmt ./...

.PHONY: lint
lint: ## Run linters
	golangci-lint run --build-tags integration ./...

.PHONY: test
test: ## Run unit tests (with -race when gcc is available)
	go test $(RACE) -count=1 ./...

.PHONY: test-integration
test-integration: ## Run integration tests (needs Docker; uses testcontainers)
	go test $(RACE) -count=1 -tags integration ./...

.PHONY: cover
cover: ## Run tests and write coverage.html
	go test $(RACE) -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

.PHONY: vuln
vuln: ## Scan dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: build
build: ## Build all packages
	go build ./...

.PHONY: check
check: lint test vuln ## Run everything CI runs

.PHONY: tools
tools: ## Install development tools (linters, buf, protoc plugins)
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)

.PHONY: proto
proto: ## Lint protobuf files and regenerate Go code into api/gen
	buf lint
	buf generate

.PHONY: proto-breaking
proto-breaking: ## Fail if protobuf changes break compatibility with main
	buf breaking --against '.git#branch=main'

##@ Infrastructure

$(COMPOSE_DIR)/.env:
	cp $(COMPOSE_DIR)/.env.example $(COMPOSE_DIR)/.env

.PHONY: infra-up
infra-up: $(COMPOSE_DIR)/.env ## Start Postgres, Redis and Redpanda
	$(INFRA) up -d --wait

.PHONY: infra-down
infra-down: ## Stop infrastructure (keeps data)
	$(INFRA) down

.PHONY: infra-reset
infra-reset: ## Stop infrastructure and delete all data volumes
	$(INFRA) down -v

.PHONY: infra-ps
infra-ps: ## Show infrastructure containers
	$(INFRA) ps

.PHONY: infra-logs
infra-logs: ## Follow infrastructure logs
	$(INFRA) logs -f

##@ Observability

.PHONY: obs-up
obs-up: $(COMPOSE_DIR)/.env ## Start infrastructure plus the collector, Prometheus, Tempo, Loki and Grafana
	$(OBS) up -d --wait

.PHONY: obs-down
obs-down: ## Stop infrastructure and observability stack
	$(OBS) down

##@ Services

.PHONY: run-event
run-event: ## Migrate and run the event service locally (needs infra-up)
	$(EVENT_ENV) go run ./services/event/cmd/event migrate
	$(EVENT_ENV) go run ./services/event/cmd/event serve

.PHONY: run-booking
run-booking: ## Migrate and run the booking service locally (needs obs-up and run-event)
	$(BOOKING_ENV) go run ./services/booking/cmd/booking migrate
	$(BOOKING_ENV) go run ./services/booking/cmd/booking serve

.PHONY: services-up
services-up: $(COMPOSE_DIR)/.env ## Build and start infrastructure and all services in Docker
	$(STACK) up -d --build --wait

.PHONY: services-down
services-down: ## Stop infrastructure and all services
	$(STACK) down

.PHONY: services-logs
services-logs: ## Follow logs of all services
	$(STACK) logs -f event booking

.PHONY: compose-config
compose-config: $(COMPOSE_DIR)/.env ## Validate the compose files
	$(STACK) config --quiet
