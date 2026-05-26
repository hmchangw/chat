SHELL := /bin/bash
TOOL_DIR  := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
REPO_ROOT := $(abspath $(TOOL_DIR)/../..)
REPORT_PATH := $(REPO_ROOT)/docs/integration-suite-v2/last-run.md

NATS_CONTAINER := chat-local-nats

.PHONY: help validate local run preflight

help:
	@echo "make -C tools/integration-suite-v2 <target>"
	@echo "  validate   — run the catalog validator (no live infra needed)"
	@echo "  local      — run against the docker-local stack (assumes 'make up' is running)"
	@echo "  run        — alias for local; honors any env-var overrides"

validate:
	@cd $(TOOL_DIR) && go run ./cmd/validator

local: preflight
	@cd $(TOOL_DIR) && \
		AUTH_SERVICE_URL=http://localhost:8080 \
		NATS_URL=nats://localhost:4222 \
		NATS_CREDS_FILE=$(REPO_ROOT)/docker-local/backend.creds \
		MONGO_URI=mongodb://localhost:27017 \
		MONGO_DB=chat \
		VALKEY_ADDR=localhost:6379 \
		SITE_ID=site-local \
		OUTPUT_PATH=$(REPORT_PATH) \
		APPROVED_OUTPUT_PATH=$(REPO_ROOT)/docs/integration-suite-v2/last-run-approved.md \
		PERFORMANCE_PATH=$(REPO_ROOT)/docs/integration-suite-v2/performance.json \
		REPO_ROOT=$(REPO_ROOT) \
		go run ./cmd/runner

run: local

preflight:
	@docker container inspect -f '{{.State.Running}}' $(NATS_CONTAINER) 2>/dev/null | grep -q true || { \
	  echo "ERROR: $(NATS_CONTAINER) is not running. Run 'make deps-up && make up' from repo root."; exit 1; \
	}
	@curl -fsS http://localhost:8080/healthz >/dev/null 2>&1 || { \
	  echo "ERROR: auth-service not reachable on :8080. Is 'make up' running?"; exit 1; \
	}
