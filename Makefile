.PHONY: lint fmt test test-integration generate build deps-up deps-down up down

DEPS_COMPOSE     := docker-local/compose.deps.yaml
SERVICES_COMPOSE := docker-local/compose.services.yaml
NATS_CREDS       := docker-local/backend.creds
NATS_CONF        := docker-local/nats.conf
NATS_CONTAINER   := chat-local-nats

# Makefile for the distributed multi-site chat system.

# Run golangci-lint (includes go vet, staticcheck, errcheck, goimports, etc.)
lint:
	golangci-lint run ./...

# Run goimports via golangci-lint to format all .go files
fmt:
	golangci-lint fmt ./...

# Run all unit tests with race detector (excludes integration tests)
test:
ifdef SERVICE
	go test -race ./$(SERVICE)/...
else
	go test -race ./...
endif

# Run integration tests (requires Docker)
test-integration:
ifdef SERVICE
	go test -race -tags integration ./$(SERVICE)/...
else
	go test -race -tags integration ./...
endif

# Regenerate all mocks via go generate
generate:
ifdef SERVICE
	go generate ./$(SERVICE)/...
else
	go generate ./...
endif

# Build a single service binary (requires SERVICE=<name>)
build:
ifndef SERVICE
	$(error SERVICE is required. Usage: make build SERVICE=<name>)
endif
ifeq ($(SERVICE),history-service)
	CGO_ENABLED=0 go build -o bin/$(SERVICE) ./$(SERVICE)/cmd/
else
	CGO_ENABLED=0 go build -o bin/$(SERVICE) ./$(SERVICE)/
endif

# --- Local dev docker targets -------------------------------------------------
# Start third-party deps (NATS, Mongo, Cassandra, ES, Keycloak) in the background.
# Runs setup.sh on first use. Blocks until every dep's healthcheck passes,
# then runs the cassandra-init one-shot to create the keyspace + tables.
deps-up:
	@if [ ! -f $(NATS_CREDS) ] || [ ! -f $(NATS_CONF) ]; then \
	  echo "First-time setup: generating nats.conf + backend.creds..."; \
	  ./docker-local/setup.sh; \
	fi
	docker compose -f $(DEPS_COMPOSE) up -d --wait
	docker compose -f $(DEPS_COMPOSE) --profile init run --rm cassandra-init

# Stop third-party deps.
deps-down:
	docker compose -f $(DEPS_COMPOSE) down

# Start microservices. With SERVICE=<name>, starts just that service's compose;
# without, starts every service via compose.services.yaml. Foreground either way
# so container logs stream to the terminal; Ctrl-C stops.
up:
	@docker container inspect -f '{{.State.Running}}' $(NATS_CONTAINER) 2>/dev/null | grep -q true || { \
	  echo "Deps are not running. Run 'make deps-up' first."; exit 1; \
	}
	@test -f $(NATS_CREDS) && test -f $(NATS_CONF) || { \
	  echo "Missing $(NATS_CREDS) or $(NATS_CONF). Run './docker-local/setup.sh'."; exit 1; \
	}
ifdef SERVICE
	docker compose -f $(SERVICE)/deploy/docker-compose.yml up --build
else
	docker compose -f $(SERVICES_COMPOSE) up --build
endif

# Stop microservices. SERVICE=<name> stops one; otherwise stops every service.
down:
ifdef SERVICE
	docker compose -f $(SERVICE)/deploy/docker-compose.yml down
else
	docker compose -f $(SERVICES_COMPOSE) down
endif

# ---------------------------------------------------------------------------
# E2E suite. Two-site backend stack via testcontainers + docker compose.
# See docs/superpowers/plans/2026-05-08-e2e-tests.md.
# ---------------------------------------------------------------------------
E2E_DIR     := docker-local/e2e
E2E_COMPOSE := $(E2E_DIR)/compose.e2e.yaml
E2E_ENV     := $(E2E_DIR)/.env

# Auto-create .env from .env.example on first run; idempotent.
$(E2E_ENV):
	@cp $(E2E_DIR)/.env.example $@
	@echo "Created $@ from .env.example."
	@echo "Edit it to point at an internal registry mirror, or leave defaults for upstream."

.PHONY: e2e e2e-only e2e-up e2e-down e2e-down-clean e2e-logs

# Full run: TestMain owns stack lifecycle.
e2e: $(E2E_ENV)
	go test -tags e2e -race -count=1 ./e2e/...

# Iteration loop: assumes a stack already running via `make e2e-up`.
e2e-only: $(E2E_ENV)
	@[ -n "$$(docker compose -f $(E2E_COMPOSE) ps --status running --quiet nats-a 2>/dev/null)" ] || { \
	  echo "E2E stack is not running. Run 'make e2e-up' first."; exit 1; \
	}
	E2E_REUSE_STACK=1 go test -tags e2e -race -count=1 ./e2e/...

# Manual stack control.
e2e-up: $(E2E_ENV)
	@[ -n "$$(docker compose -f $(DEPS_COMPOSE) ps --status running --quiet nats 2>/dev/null)" ] && { \
	  echo "ERROR: 'make deps-up' is running. Stop it first ('make deps-down')."; \
	  echo "Even with non-overlapping host ports, dual-stack on one box risks OOM."; \
	  exit 1; \
	} || true
	docker compose -f $(E2E_COMPOSE) up -d --wait
	docker compose -f $(E2E_COMPOSE) --profile init run --rm cassandra-init-a
	docker compose -f $(E2E_COMPOSE) --profile init run --rm search-init-a

# Non-destructive: stops containers, preserves volumes (keycloak realm + cass
# schema survive). Use this for fast iteration.
e2e-down:
	docker compose -f $(E2E_COMPOSE) down

# Destructive: drops volumes too. Next `e2e-up` re-imports the Keycloak realm
# and re-runs cassandra-init-a / search-init-a from a fresh state.
e2e-down-clean:
	docker compose -f $(E2E_COMPOSE) down -v

# `make e2e-logs SERVICE=msg-gk-a` to tail one container.
e2e-logs:
	docker compose -f $(E2E_COMPOSE) logs -f $(SERVICE)
