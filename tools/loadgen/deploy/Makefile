COMPOSE ?= docker compose -f docker-compose.loadtest.yml

.PHONY: up seed run run-dashboards run-natsrouter down logs validate-rules validate-dashboards

up:
	$(COMPOSE) up -d --build

seed:
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen seed --preset=$(PRESET)

run:
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen run \
	    --preset=$(PRESET) \
	    --rate=$(or $(RATE),500) \
	    --duration=$(or $(DURATION),60s)

run-dashboards:
	$(COMPOSE) --profile dashboards up -d
	$(MAKE) run PRESET=$(PRESET) RATE=$(RATE) DURATION=$(DURATION)

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
