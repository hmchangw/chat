ROOT             := ../../..
DEPS_COMPOSE     := $(ROOT)/docker-local/compose.deps.yaml
SERVICES_COMPOSE := $(ROOT)/docker-local/compose.services.yaml
OVERLAY          := docker-compose.yml
COMPOSE          := docker compose -f $(OVERLAY)

# Encryption is the default for broadcast-worker; the broadcast-worker compose
# reads ENCRYPTION_ENABLED via env interpolation (defaulting to true). Flip to
# `ENCRYPTION_ENABLED=false make up` for a plaintext comparison run.
export ENCRYPTION_ENABLED ?= true

.PHONY: up stack-up overlay-up seed teardown run run-dashboards run-natsrouter down logs \
        validate-rules validate-dashboards shellcheck-scripts validate-chaos validate-compose smoke

up: stack-up overlay-up

# Bring up the shared docker-local stack: third-party deps, then microservices.
stack-up:
	$(MAKE) -C $(ROOT) deps-up
	docker compose -f $(SERVICES_COMPOSE) up -d --build

overlay-up:
	$(COMPOSE) up -d --build

seed:
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen seed --preset=$(PRESET)

teardown:
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen teardown --preset=$(PRESET)

run: seed
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen run \
	    --preset=$(PRESET) \
	    --rate=$(or $(RATE),500) \
	    --duration=$(or $(DURATION),60s) \
	    $(if $(SCENARIO),--scenario=$(SCENARIO)) \
	    $(if $(INJECT),--inject=$(INJECT))

run-dashboards:
	$(COMPOSE) --profile dashboards up -d
	$(MAKE) run PRESET=$(PRESET) RATE=$(RATE) DURATION=$(DURATION) SCENARIO=$(SCENARIO) INJECT=$(INJECT)

# run-natsrouter executes the full Phase 1+2 read-scenario sweep:
# seed fixtures, brief messaging-pipeline warm-up, history-read,
# search-read, room-rpc — each at conservative rates suitable for
# a laptop. Override RATE/DURATION per-target via env if needed.
run-natsrouter:
	$(COMPOSE) exec -T loadgen /loadgen seed --preset=realistic
	$(COMPOSE) exec -T loadgen /loadgen run \
	    --preset=realistic --scenario=messaging-pipeline \
	    --rate=300 --duration=30s --warmup=5s
	$(COMPOSE) exec -T loadgen /loadgen run \
	    --preset=history-read --scenario=history-read \
	    --rate=200 --duration=30s --warmup=10s
	$(COMPOSE) exec -T loadgen /loadgen run \
	    --preset=search-read --scenario=search-read \
	    --rate=100 --duration=30s --warmup=5s
	$(COMPOSE) exec -T loadgen /loadgen run \
	    --preset=room-rpc --scenario=room-rpc \
	    --rate=200 --duration=30s --warmup=5s

down:
	$(COMPOSE) --profile dashboards down -v
	docker compose -f $(SERVICES_COMPOSE) down
	$(MAKE) -C $(ROOT) deps-down

logs:
	$(COMPOSE) logs -f loadgen

# validate-dashboards checks that all v2 dashboard JSON files are syntactically
# valid. A deeper schema check (Grafana toolkit) is deferred — JSON validity is
# the bar for Phase 1b §1.12. For a smoke test that verifies Grafana actually
# loads the dashboards, run:
#   make run-dashboards && curl http://localhost:3000/api/dashboards/uid/loadgen-overview
validate-dashboards:
	@echo "Validating v2 dashboard JSON..."
	@for f in $(CURDIR)/grafana/dashboards/v2/*.json; do \
		echo "  $$f"; \
		python3 -c "import json,sys; json.load(open(sys.argv[1]))" "$$f" || { echo "FAIL: $$f is not valid JSON"; exit 1; }; \
	done
	@echo "All v2 dashboard JSON files are valid."

# validate-rules runs promtool test rules against the synthetic test suite.
# Requires Docker. Mounts the rules directory read-only into the official
# prom/prometheus image and runs the tests in a throwaway container.
validate-rules:
	docker run --rm \
		-v $(CURDIR)/prometheus/rules:/rules:ro \
		prom/prometheus:latest \
		promtool test rules /rules/loadgen_test.yml

# shellcheck-scripts runs shellcheck against all operator-facing shell scripts.
# Install shellcheck: apt-get install shellcheck  or  brew install shellcheck
shellcheck-scripts:
	@which shellcheck >/dev/null || { echo "shellcheck not installed; install with 'apt-get install shellcheck' or 'brew install shellcheck'"; exit 2; }
	shellcheck $(CURDIR)/../scripts/*.sh

# validate-chaos checks that the chaos overlay Compose file is syntactically
# valid and that run-chaos.sh passes shellcheck (if shellcheck is installed).
.PHONY: validate-chaos
validate-chaos:
	@$(COMPOSE) -f docker-compose.chaos.yml --profile chaos config -q
	@which shellcheck >/dev/null || { echo "shellcheck not installed; skipping script lint"; exit 0; }
	@shellcheck $(CURDIR)/../scripts/run-chaos.sh

# validate-compose checks the primary compose file parses cleanly with the
# detected compose command, and optionally runs a v1 cross-check.
validate-compose:
	$(COMPOSE) config -q
	@if command -v docker-compose >/dev/null 2>&1 && command -v docker >/dev/null 2>&1; then \
	  docker-compose -f $(OVERLAY) config -q || echo "WARN: v1 rejected config"; \
	fi

# smoke brings the stack up, waits for health, then tears it down.
# Requires Docker to be running.
smoke:
	$(COMPOSE) up -d --wait
	$(COMPOSE) down
