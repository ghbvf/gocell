.PHONY: build check-build test validate generate cover clean \
        up down \
        test-integration \
        test-examples-smoke \
        healthcheck-verify

# ---------------------------------------------------------------------------
# Go targets
# ---------------------------------------------------------------------------

# build produces shippable binaries into bin/. Use `make check-build` when the
# goal is a full-repo compile check (no artefacts) — mirrors the
# Kubernetes/kratos/go-zero split between `verify` and `build`.
build:
	mkdir -p bin
	go build -o bin/ ./cmd/... ./examples/...

check-build:
	go build ./...

test:
	go test ./... -count=1

validate:
	go run ./cmd/gocell validate

generate:
	go run ./cmd/gocell generate assembly --id=corebundle

cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

clean:
	rm -rf bin/
	rm -f coverage.out
	rm -f gocell corebundle iotdevice ssobff todoorder

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
# examples/ssobff startup smoke
# Builds the demo binary and runs TestSSOBFFStartupSmoke (subprocess +
# /readyz probe + SIGTERM graceful path). Mirrors the CI examples-smoke
# job; useful before pushing a main.go / option-wiring change.
# ---------------------------------------------------------------------------

test-examples-smoke:
	go test ./examples/ssobff/... -tags=examples_smoke -count=1 -timeout 90s -run TestSSOBFFStartupSmoke -v

# ---------------------------------------------------------------------------
# Healthcheck verification  (T09)
# Delegates to scripts/healthcheck-verify.sh
# ---------------------------------------------------------------------------

healthcheck-verify:
	bash scripts/healthcheck-verify.sh
