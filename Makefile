.PHONY: lint fmt test test-integration generate build deps-up deps-down up down

DEPS_COMPOSE     := docker-local/compose.deps.yaml
SERVICES_COMPOSE := docker-local/compose.services.yaml
NATS_CREDS       := docker-local/backend.creds
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
# Runs setup.sh on first use. Blocks until every dep's healthcheck passes.
deps-up:
	@if [ ! -f $(NATS_CREDS) ]; then \
	  echo "First-time setup: generating nats.conf + backend.creds..."; \
	  ./docker-local/setup.sh; \
	fi
	docker compose -f $(DEPS_COMPOSE) up -d --wait

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
	@test -f $(NATS_CREDS) || { echo "$(NATS_CREDS) missing. Run './docker-local/setup.sh'."; exit 1; }
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
