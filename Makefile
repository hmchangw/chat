.PHONY: lint fmt test test-integration generate build deps-up deps-down up down \
        logs federation-up tools sast sast-gosec sast-vuln sast-semgrep

DEPS_COMPOSE   := docker-local/compose.deps.yaml
SITE_A_COMPOSE := docker-local/compose.services.site-a.yaml
SITE_B_COMPOSE := docker-local/compose.services.site-b.yaml
FED_COMPOSE    := docker-local/compose.federation.yaml
SITE_A_ENV     := docker-local/site-a.env
SITE_B_ENV     := docker-local/site-b.env

# --- SAST / dev tooling ------------------------------------------------------
# Pinned tool versions. Keep GOLANGCI_LINT_VERSION in sync with
# .github/workflows/ci.yml. golangci-lint/gosec/govulncheck install via
# `go install` into $(GOBIN_DIR) (no go.mod impact); semgrep is a Python
# tool installed via pipx.
#
# TOOLS_GO_TOOLCHAIN pins the toolchain used to *source-build* the Go
# tools (via GOTOOLCHAIN) so installs are reproducible regardless of the
# runner's Go. Tool versions must themselves be Go 1.25-compatible:
# gosec < v2.26 pins golang.org/x/tools@v0.25.0, which fails to compile
# under any Go 1.25.x ("invalid array length -delta * delta"), so
# GOSEC_VERSION is held at a release whose dependency tree builds on
# Go 1.25. Tracks the repo-wide Go (go.mod / ci.yml); Go fetches the
# pinned toolchain on demand.
GOBIN_DIR             := $(shell go env GOPATH)/bin
TOOLS_GO_TOOLCHAIN    := go1.25.10
GOLANGCI_LINT_VERSION := v2.11.4
GOSEC_VERSION         := v2.26.1
GOVULNCHECK_VERSION   := v1.3.0
SEMGREP_VERSION       := 1.163.0

GOSEC       := $(GOBIN_DIR)/gosec
GOVULNCHECK := $(GOBIN_DIR)/govulncheck

# gosec scope: shipped product code only. tools/ holds dev/ops utilities
# (loadgen, nats-debug) that are not deployed services; chat-frontend is
# JS. -tests=false skips *_test.go (including generated mocks);
# -exclude-generated skips code-generated files. Gate: medium+ severity.
GOSEC_FLAGS := -quiet -severity medium -confidence medium -tests=false \
               -exclude-generated -exclude-dir=tools -exclude-dir=testdata

# semgrep: fail on medium+ (WARNING/ERROR; INFO is informational/low).
SEMGREP_FLAGS := --error --severity=WARNING --severity=ERROR --metrics=off \
                 --exclude=tools --exclude=chat-frontend --exclude=testdata \
                 --exclude=docs --config=p/golang --config=p/security-audit

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
# Start third-party deps (both NATSes + shared Mongo/Cassandra/ES/Valkey/
# Keycloak) in the background. Runs setup.sh on first use to generate the
# per-site NATS creds + configs. Blocks until every dep's healthcheck
# passes, then runs cassandra-init to create both keyspaces.
deps-up:
	@if [ ! -d docker-local/site-a ] || [ ! -d docker-local/site-b ]; then \
	  echo "First-time setup: generating per-site NATS creds + configs..."; \
	  ./docker-local/setup.sh; \
	fi
	docker compose -f $(DEPS_COMPOSE) up -d --wait
	docker compose -f $(DEPS_COMPOSE) --profile init run --rm cassandra-init

# Stop third-party deps.
deps-down:
	docker compose -f $(DEPS_COMPOSE) down

# Start every microservice for BOTH sites detached, run the one-shot
# federation-init to wire cross-site JetStream sources, then tail both
# sites' logs in the foreground (Ctrl-C detaches; containers keep running
# until `make down`). Use `make logs SITE=a` or `=b` for one side only.
up:
	@docker container inspect -f '{{.State.Running}}' chat-local-nats-a 2>/dev/null | grep -q true || { \
	  echo "Deps are not running. Run 'make deps-up' first."; exit 1; \
	}
	@docker container inspect -f '{{.State.Running}}' chat-local-nats-b 2>/dev/null | grep -q true || { \
	  echo "nats-b is not running. Run 'make deps-up' first."; exit 1; \
	}
	docker compose --env-file $(SITE_A_ENV) -f $(SITE_A_COMPOSE) up -d --build
	docker compose --env-file $(SITE_B_ENV) -f $(SITE_B_COMPOSE) up -d --build
	docker compose -f $(FED_COMPOSE) --profile init run --rm federation-init
	@echo ""
	@echo "==> Both sites up. Tailing logs (Ctrl-C detaches; containers keep running)."
	@echo ""
	docker compose --env-file $(SITE_A_ENV) -f $(SITE_A_COMPOSE) \
	               --env-file $(SITE_B_ENV) -f $(SITE_B_COMPOSE) logs -f

# Stop microservices for both sites. Deps stay up — use `make deps-down`.
down:
	docker compose --env-file $(SITE_B_ENV) -f $(SITE_B_COMPOSE) down
	docker compose --env-file $(SITE_A_ENV) -f $(SITE_A_COMPOSE) down

# Tail one site's logs only. Required: SITE=a or SITE=b.
logs:
ifndef SITE
	$(error SITE is required. Usage: make logs SITE=a|b)
endif
	docker compose --env-file docker-local/site-$(SITE).env \
	               -f docker-local/compose.services.site-$(SITE).yaml logs -f

# Re-run the federation-init one-shot (idempotent). Useful after
# rebuilding inbox-worker or recreating an INBOX stream.
federation-up:
	docker compose -f $(FED_COMPOSE) --profile init run --rm federation-init

# --- SAST -------------------------------------------------------------------
# Install pinned dev/SAST tooling. Go tools install into $(GOBIN_DIR) with
# no go.mod impact; semgrep installs via pipx. Idempotent — safe to re-run.
# setuptools is injected into semgrep's venv because semgrep imports
# pkg_resources, which setuptools-less Python 3.12+ (e.g. ubuntu-latest)
# no longer ships by default.
tools:
	GOTOOLCHAIN=$(TOOLS_GO_TOOLCHAIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	GOTOOLCHAIN=$(TOOLS_GO_TOOLCHAIN) go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	GOTOOLCHAIN=$(TOOLS_GO_TOOLCHAIN) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@if command -v pipx >/dev/null 2>&1; then \
	  pipx install --force semgrep==$(SEMGREP_VERSION) \
	    && pipx inject semgrep setuptools; \
	elif command -v semgrep >/dev/null 2>&1; then \
	  echo "pipx not found, but semgrep is already on PATH — skipping semgrep install"; \
	else \
	  echo "pipx not found and semgrep not on PATH — install pipx, or: pip install --user semgrep==$(SEMGREP_VERSION)" >&2; \
	  exit 1; \
	fi

# Run all SAST scans (gosec, govulncheck, semgrep). All three always run
# (no fail-fast) so every category is reported in one pass; exits non-zero
# if any scan finds an issue. This is the exact command CI enforces.
sast:
	@rc=0; g=PASS; v=PASS; s=PASS; \
	$(MAKE) --no-print-directory sast-gosec   || { rc=1; g=FAIL; }; \
	$(MAKE) --no-print-directory sast-vuln    || { rc=1; v=FAIL; }; \
	$(MAKE) --no-print-directory sast-semgrep || { rc=1; s=FAIL; }; \
	echo "==> SAST summary: gosec=$$g govulncheck=$$v semgrep=$$s"; \
	exit $$rc

# gosec: Go security static analysis (injection, weak crypto, unsafe code).
sast-gosec:
	@test -x "$(GOSEC)" || { echo "gosec not installed — run 'make tools'"; exit 1; }
	$(GOSEC) $(GOSEC_FLAGS) ./...

# govulncheck: known CVEs in dependencies with call-graph reachability.
# Requires outbound network access to https://vuln.go.dev.
sast-vuln:
	@test -x "$(GOVULNCHECK)" || { echo "govulncheck not installed — run 'make tools'"; exit 1; }
	GOTOOLCHAIN=$(TOOLS_GO_TOOLCHAIN) $(GOVULNCHECK) ./...

# semgrep: rule-based SAST (Go security + security-audit rulesets).
# Requires outbound network access to the Semgrep registry on first run.
sast-semgrep:
	@command -v semgrep >/dev/null 2>&1 || { echo "semgrep not installed — run 'make tools' (needs pipx), or: pipx install semgrep==$(SEMGREP_VERSION)"; exit 1; }
	semgrep scan $(SEMGREP_FLAGS) .
