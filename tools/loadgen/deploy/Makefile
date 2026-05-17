ROOT             := ../../..
DEPS_COMPOSE     := $(ROOT)/docker-local/compose.deps.yaml
SERVICES_COMPOSE := $(ROOT)/docker-local/compose.services.yaml
OVERLAY          := docker-compose.yml
COMPOSE          := docker compose -f $(OVERLAY)

# Encryption is the default for broadcast-worker; the broadcast-worker compose
# reads ENCRYPTION_ENABLED via env interpolation (defaulting to true). Flip to
# `ENCRYPTION_ENABLED=false make up` for a plaintext comparison run.
export ENCRYPTION_ENABLED ?= true

.PHONY: up stack-up overlay-up seed teardown run run-dashboards down logs

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

run:
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen run \
	    --preset=$(PRESET) \
	    --rate=$(or $(RATE),500) \
	    --duration=$(or $(DURATION),60s)

run-dashboards:
	$(COMPOSE) --profile dashboards up -d
	$(MAKE) run PRESET=$(PRESET) RATE=$(RATE) DURATION=$(DURATION)

down:
	$(COMPOSE) --profile dashboards down -v
	docker compose -f $(SERVICES_COMPOSE) down
	$(MAKE) -C $(ROOT) deps-down

logs:
	$(COMPOSE) logs -f loadgen
