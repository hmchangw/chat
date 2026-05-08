COMPOSE ?= docker compose -f docker-compose.loadtest.yml

.PHONY: up seed run run-dashboards run-natsrouter down logs

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
