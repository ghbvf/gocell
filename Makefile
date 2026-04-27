.PHONY: build check-build test verify validate generate cover clean \
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

# verify discovers and runs every hack/verify-*.sh in deterministic order,
# accumulating failures. Single entry point for static governance gates
# (validate --strict, archtest, contract-health, journey, etc.).
# ref: kubernetes/kubernetes hack/make-rules/verify.sh
verify:
	bash hack/make-rules/verify.sh

validate:
	go run ./cmd/gocell validate

generate:
	for d in assemblies/*/; do go run ./cmd/gocell generate assembly --id="$$(basename "$$d")" --boundary-only; done
	for d in assemblies/*/; do go run ./cmd/gocell generate metrics-schema --id="$$(basename "$$d")"; done

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
