# Integration-suite Makefile — self-contained.
#
# Assumes you have already run, from the repo root:
#   ./docker-local/setup.sh   (one-time)
#   make deps-up              (NATS + Mongo + Cassandra + …)
#   make up                   (the microservices)
#
# Invocation styles:
#   from repo root:    make -C tools/integration-suite [target]
#   from this dir:     make [target]
#
# Targets accept the same SCOPE / TAGS / FEATURE knobs as the suite.

# Use bash for recipes — required because the docker-local site ID is
# `site-local` (hyphenated), and the suite's per-site env vars (e.g.
# AUTH_SERVICE_URL_SITE-LOCAL) contain that hyphen. POSIX /bin/sh
# (dash on Ubuntu/Debian) strips env vars whose names aren't valid
# identifiers from its environment block; bash preserves them.
# Without this, `env FOO-BAR=val …` from inside a recipe would lose
# the var before go test ever sees it.
SHELL := /bin/bash

# Suppress make's noisy "Entering/Leaving directory" lines.
MAKEFLAGS += --no-print-directory

# Repo root is one level up. All `go` invocations run from there so the
# monorepo's single go.mod is honored.
REPO_ROOT  := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../..)
TOOL_DIR   := tools/integration-suite

# ── Docker / docker-local single-site assumptions ─────────────────────────
# These match what `make deps-up && make up` sets up.
NATS_CONTAINER := chat-local-nats
NATS_URL       := nats://localhost:4222
AUTH_URL       := http://localhost:8080
ROOM_URL       := http://localhost:8080
LOCAL_SITE     := site-local

# Per-scope hints — which services that scope of scenarios actually exercises.
# Pre-flight is informational, not blocking: if a non-auth service isn't up,
# the runtime classifier surfaces it as RouteNotFound / Timeout / Unreachable
# rather than failing here.
SERVICES_service  := auth-service room-service history-service
SERVICES_pipeline := auth-service message-gatekeeper
SERVICES_full     := auth-service room-service room-worker history-service message-gatekeeper broadcast-worker notification-worker

.PHONY: help default local run steps lint coverage audit audit-tally preflight scope-hint

# ── Default ───────────────────────────────────────────────────────────────
default: help

help:
	@echo "Integration-suite targets (invoke via 'make -C tools/integration-suite <target>'):"
	@echo ""
	@echo "  local                  Run scenarios against the docker-local single-site stack"
	@echo "                         (auto-fills env vars; pre-flights NATS + auth-service)"
	@echo "  run                    Run scenarios against any stack (caller provides env vars)"
	@echo "  steps                  List all registered godog step regexes"
	@echo "  lint                   Verify @blindspot:<slug> <-> blindspots.md consistency"
	@echo "  coverage               Score documented-case coverage (report-only)"
	@echo "  audit SAMPLE=<n>       Generate a confusion-metric audit checklist"
	@echo "  audit-tally IN=<path>  Tally a reviewer-filled audit checklist"
	@echo ""
	@echo "  Knobs (apply to local & run):"
	@echo "    SCOPE=<dir>      Limit to features/<dir> (e.g. SCOPE=service)"
	@echo "    TAGS=<expr>      godog tag filter (e.g. TAGS=@smoke)"
	@echo "    FEATURE=<path>   Single feature file"
	@echo ""

# ── Pre-flight ────────────────────────────────────────────────────────────
# Universal: NATS broker reachable + auth-service /healthz responsive.
preflight:
	@docker container inspect -f '{{.State.Running}}' $(NATS_CONTAINER) 2>/dev/null | grep -q true || { \
	  echo "ERROR: $(NATS_CONTAINER) is not running."; \
	  echo "       From the repo root: 'make deps-up && make up' first."; \
	  exit 1; \
	}
	@curl -fsS $(AUTH_URL)/healthz >/dev/null 2>&1 || { \
	  echo "ERROR: auth-service not reachable on $(AUTH_URL)."; \
	  echo "       Is 'make up' running in another terminal?"; \
	  exit 1; \
	}

# Scope-aware hint — prints which services this scope ASSUMES are running.
# Non-blocking: a missing NATS-only service will surface at runtime as
# RouteNotFound (no responders), which the classifier handles cleanly.
scope-hint:
	@scope="$(or $(SCOPE),full)"; \
	case "$$scope" in \
	  service)  list="$(SERVICES_service)"  ;; \
	  pipeline) list="$(SERVICES_pipeline)" ;; \
	  *)        list="$(SERVICES_full)"     ;; \
	esac; \
	echo "Scope '$$scope' assumes these services are running (from 'make up'):"; \
	echo "  $$list"; \
	echo ""

# ── Run targets ───────────────────────────────────────────────────────────
# Shared godog invocation. `local` and `run` both end up here. Kept in a
# single place so there is no drift between the env-prefixed and the
# caller-provides-env paths.
#
# IMPORTANT: `local` calls `env … go test` DIRECTLY — no recursive
# `$(MAKE)` — because GNU make's variable table can't represent env-var
# names with hyphens (it silently skips them on entry), so passing them
# through a recursive make would drop them before the inner recipe shell
# ever sees them. Combined with SHELL := /bin/bash at the top, the
# hyphenated AUTH_SERVICE_URL_SITE-LOCAL etc. survive end-to-end into
# go test's environment.
GODOG_ARGS := -v -run TestFeatures \
              $(if $(SCOPE),-godog.paths=features/$(SCOPE)) \
              $(if $(TAGS),-godog.tags="$(TAGS)") \
              $(if $(FEATURE),-godog.paths=$(FEATURE)) \
              ./$(TOOL_DIR)/

local: preflight scope-hint
	@cd $(REPO_ROOT) && env SITES=$(LOCAL_SITE) PRIMARY_SITE=$(LOCAL_SITE) \
	   AUTH_SERVICE_URL_SITE-LOCAL=$(AUTH_URL) \
	   NATS_URL_SITE-LOCAL=$(NATS_URL) \
	   ROOM_SERVICE_URL_SITE-LOCAL=$(ROOM_URL) \
	   go test $(GODOG_ARGS)

# `run` = the actual godog invocation. Caller supplies env.
run:
	@cd $(REPO_ROOT) && go test $(GODOG_ARGS)

# ── Tooling targets ───────────────────────────────────────────────────────
steps:
	@cd $(REPO_ROOT) && go run ./$(TOOL_DIR)/cmd/steps

lint:
	@cd $(REPO_ROOT) && go run ./$(TOOL_DIR)/cmd/lint

coverage:
	@cd $(REPO_ROOT) && go run ./$(TOOL_DIR)/cmd/coverage

audit:
	@cd $(REPO_ROOT) && go run ./$(TOOL_DIR)/cmd/audit -sample=$(or $(SAMPLE),30)

audit-tally:
	@if [ -z "$(IN)" ]; then echo "usage: make audit-tally IN=<path>"; exit 2; fi
	@cd $(REPO_ROOT) && go run ./$(TOOL_DIR)/cmd/audit-tally -in=$(IN)
