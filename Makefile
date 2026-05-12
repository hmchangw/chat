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

# ── integration-suite ─────────────────────────────────────────────
# A scenario-driven black-box integration test suite. See
# tools/integration-suite/README.md.

INTEGRATION_SUITE_DIR := tools/integration-suite

.PHONY: integration-suite integration-suite-steps integration-suite-lint integration-suite-audit integration-suite-audit-tally

integration-suite:
	@cd $(INTEGRATION_SUITE_DIR) && \
		go test -v -run TestFeatures $(if $(SCOPE),-godog.paths=features/$(SCOPE)) \
		                              $(if $(TAGS),-godog.tags="$(TAGS)") \
		                              $(if $(FEATURE),-godog.paths=$(FEATURE))

integration-suite-steps:
	@go run ./$(INTEGRATION_SUITE_DIR)/cmd/steps

integration-suite-lint:
	@go run ./$(INTEGRATION_SUITE_DIR)/cmd/lint

integration-suite-audit:
	@go run ./$(INTEGRATION_SUITE_DIR)/cmd/audit -sample=$(or $(SAMPLE),30)

integration-suite-audit-tally:
	@if [ -z "$(IN)" ]; then echo "usage: make integration-suite-audit-tally IN=<path>"; exit 2; fi
	@go run ./$(INTEGRATION_SUITE_DIR)/cmd/audit-tally -in=$(IN)
