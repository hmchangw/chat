.PHONY: lint fmt test test-integration generate build nats-up nats-down up down

NATS_COMPOSE := docker-local/compose.nats.yaml
NATS_CREDS   := docker-local/backend.creds
NATS_CONTAINER := chat-local-nats

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
# Start the shared NATS server in the foreground (streams logs; Ctrl-C stops it).
# Runs setup.sh on first use. Run this in its own terminal, then 'make up' in another.
nats-up:
	@if [ ! -f $(NATS_CREDS) ]; then \
	  echo "First-time setup: generating nats.conf + backend.creds..."; \
	  ./docker-local/setup.sh; \
	fi
	docker compose -f $(NATS_COMPOSE) up

# Stop the shared NATS server (use when 'make nats-up' isn't running in a terminal).
nats-down:
	docker compose -f $(NATS_COMPOSE) down

# Start a microservice via its docker-compose.yml, ensuring NATS is running.
# Runs in the foreground so container logs stream to the terminal; Ctrl-C stops it.
# Usage: make up SERVICE=<name>
up:
ifndef SERVICE
	$(error SERVICE is required. Usage: make up SERVICE=<name>)
endif
	@docker container inspect -f '{{.State.Running}}' $(NATS_CONTAINER) 2>/dev/null | grep -q true || { \
	  echo "NATS is not running. Run 'make nats-up' first."; exit 1; \
	}
	@test -f $(NATS_CREDS) || { echo "$(NATS_CREDS) missing. Run './docker-local/setup.sh'."; exit 1; }
	docker compose -f $(SERVICE)/deploy/docker-compose.yml up --build

# Stop a microservice.
# Usage: make down SERVICE=<name>
down:
ifndef SERVICE
	$(error SERVICE is required. Usage: make down SERVICE=<name>)
endif
	docker compose -f $(SERVICE)/deploy/docker-compose.yml down
