.PHONY: build test validate generate cover clean \
        up down \
        test-integration \
        healthcheck-verify

# ---------------------------------------------------------------------------
# Go targets
# ---------------------------------------------------------------------------

build:
	mkdir -p bin
	go build -o bin/ ./cmd/... ./examples/...

test:
	go test ./... -count=1

validate:
	go run ./cmd/gocell validate

generate:
	go run ./cmd/gocell generate assembly --id=core-bundle

cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

clean:
	rm -rf bin/
	rm -f coverage.out

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
	go test ./adapters/... ./tests/integration/... -tags=integration -count=1 -v
	docker compose down

# ---------------------------------------------------------------------------
# Healthcheck verification  (T09)
# Delegates to scripts/healthcheck-verify.sh
# ---------------------------------------------------------------------------

healthcheck-verify:
	bash scripts/healthcheck-verify.sh
