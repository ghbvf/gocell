.PHONY: build test validate generate cover clean \
        up down \
        test-integration \
        healthcheck-verify

# ---------------------------------------------------------------------------
# Delegate to src/Makefile for Go targets
# ---------------------------------------------------------------------------

build:
	$(MAKE) -C src build

test:
	$(MAKE) -C src test

validate:
	$(MAKE) -C src validate

generate:
	$(MAKE) -C src generate

cover:
	$(MAKE) -C src cover

clean:
	$(MAKE) -C src clean

# ---------------------------------------------------------------------------
# Docker Compose lifecycle
# ---------------------------------------------------------------------------

up:
	docker compose up -d --wait

down:
	docker compose down

# ---------------------------------------------------------------------------
# Integration tests  (T08)
# Boots all services, runs adapter-level tests, tears down.
# ---------------------------------------------------------------------------

test-integration:
	docker compose up -d --wait
	cd src && go test ./adapters/... -tags=integration -count=1 -v
	docker compose down

# ---------------------------------------------------------------------------
# Healthcheck verification  (T09)
# Delegates to scripts/healthcheck-verify.sh
# ---------------------------------------------------------------------------

healthcheck-verify:
	bash scripts/healthcheck-verify.sh
