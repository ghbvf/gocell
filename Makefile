.PHONY: build check-build test fmt verify validate generate proto-lint proto-gen cover clean \
        install-hooks update-archtest-golden \
        up down \
        local-up local-down \
        test-integration \
        test-integration-cluster \
        test-examples-smoke \
        healthcheck-verify

# ---------------------------------------------------------------------------
# Go targets
# ---------------------------------------------------------------------------

# build produces shippable binaries into bin/. Use `make check-build` when the
# goal is a full-repo compile check (no artefacts) — mirrors the
# Kubernetes/kratos/go-zero split between `verify` and `build`.
#
# `go generate ./cmd/corebundle/` runs first so cmd/corebundle/catalog_gen.go
# (gated by `//go:build catalog_gen`) is regenerated for the local platform;
# the `-tags=catalog_gen` build flag selects that file over catalog_gen_stub.go,
# producing a binary with the full package dep graph. Plain `go build ./...`
# without the tag uses the stub (empty graph) so newcomers don't need to run
# `go generate` before their first build. See docs/guides/devtools-catalog.md.
build:
	mkdir -p bin
	go generate ./cmd/corebundle/
	go build -tags=catalog_gen -o bin/ ./cmd/... ./examples/...

check-build:
	go build ./...

test:
	go test ./... -count=1

# fmt rewrites Go sources in place via every formatter declared under
# .golangci.yml `formatters.enable` (currently gofmt + goimports + gofumpt).
# Pair-mate of `make verify` (specifically hack/verify-gofumpt.sh): fmt fixes,
# verify checks.
#
# golangci-lint is bootstrapped from hack/lib/golangci-lint.sh at the version
# pinned to .github/workflows/_build-lint.yml — never from $PATH — so local
# fmt and CI lint apply identical formatter rules.
#
# ref: kubernetes/kubernetes hack/update-gofmt.sh + hack/verify-golangci-lint.sh.
fmt:
	@bash -c 'source hack/lib/golangci-lint.sh && exec "$$(gocell::golangci_lint::ensure)" fmt ./...'

# update-archtest-golden regenerates the diagnostic golden files of golden-based
# archtests (errcode / clock / span / … fixtures), then leaves the diff for
# review as a first-class artifact (see AssertGolden godoc). It does NOT touch
# ARCHTEST-MODULE-PATH-FUNNEL-01's frozen baseline (testdata/module_path_funnel.baseline),
# which is shrink-only and hand-maintained by design — never auto-regenerated.
update-archtest-golden:
	go test ./tools/archtest/... -update -count=1

# verify discovers and runs every hack/verify-*.sh in deterministic order,
# accumulating failures. Single entry point for static governance gates
# (validate --strict, archtest, contract-health, journey, etc.).
# ref: kubernetes/kubernetes hack/make-rules/verify.sh
verify:
	bash hack/make-rules/verify.sh

# install-hooks points git at the tracked hack/githooks/ dir (per-repo
# config, shared across all worktrees of this repo). Run once after clone
# and after `git worktree add`. The pre-push hook runs the fast CI subset
# (gofumpt / build+vet / codegen staleness) AI co-authors most often skip.
install-hooks:
	git config core.hooksPath hack/githooks
	@echo "core.hooksPath -> hack/githooks (pre-push active)"

validate:
	go run ./cmd/gocell validate

generate:
	go run ./cmd/gocell generate assembly --all
	go run ./cmd/gocell generate metrics-schema --all
	go run ./cmd/gocell generate cell --all
	go run ./cmd/gocell generate contract --all
	go run ./cmd/gocell generate required-deps --all
	go run ./cmd/gocell generate shared-schema --all
	go run ./cmd/gocell generate saga-coverage
	go generate ./cmd/corebundle/

# proto-gen regenerates generated/contracts/grpc/**/*.pb.go from
# contracts/grpc/**.proto (+ examples/*/contracts/grpc once cell protos land,
# see contracts/grpc/README.md). The toolchain is hermetic and adds NOTHING to
# the main module's go.mod: buf and the grpc plugin run through `go run` at
# pinned versions (BUF_VERSION below; protoc-gen-go-grpc in buf.gen.yaml), and
# protoc-gen-go resolves from the module's protobuf require so the generator
# stays in lockstep with the runtime. Commit the regenerated *.pb.go; CI runs
# hack/verify-codegen-proto.sh (regenerate + diff) to gate drift.
#
# ref: bufbuild/buf `buf generate`; protocolbuffers/protobuf-go protoc-gen-go.
BUF_VERSION := v1.70.0

# proto-lint runs buf's STANDARD lint set (declared in buf.yaml) — package
# directory match, service/RPC naming, etc. Separated from proto-gen so it can
# be run on its own (`make proto-lint`); hack/verify-codegen-proto.sh runs it
# before generation so the buf.yaml lint config is an enforced gate, not dead
# config. ref: bufbuild/buf `buf lint`.
proto-lint:
	go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION) lint

proto-gen:
	go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION) generate

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

# Local docker deploy: full stack (PG+Redis+migrate+corebundle).
# Prereq: scripts/gen-deploy-secrets.sh to produce .env.local first.
local-up:
	@test -f .env.local || (echo "missing .env.local; run scripts/gen-deploy-secrets.sh first" >&2; exit 1)
	docker compose -f docker-compose.local.yml --env-file .env.local up -d --wait

local-down:
	@test -f .env.local || (echo "missing .env.local; run scripts/gen-deploy-secrets.sh first" >&2; exit 1)
	docker compose -f docker-compose.local.yml --env-file .env.local down -v

# ---------------------------------------------------------------------------
# Integration tests  (T08)
# Testcontainers self-provisions required services. GOCELL_TEST_DOCKER_REQUIRED
# makes Docker provider failures fail fast instead of producing local skips.
# ---------------------------------------------------------------------------

test-integration:
	GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags=integration,e2e \
		./adapters/... \
		./tests/integration/... \
		./tests/e2e/internal/... \
		./cmd/corebundle/... \
		./examples/ssobff/... \
		./cells/accesscore/... \
		./cells/configcore/... \
		./cells/auditcore/... \
		./runtime/bootstrap/... \
		-count=1 -timeout 15m -v

# ---------------------------------------------------------------------------
# Real Redis Cluster tests (B10 PR-V1-REDIS-CLUSTER)
# Requires GOCELL_TEST_REDIS_CLUSTER_ADDRS pointing at a pre-launched cluster
# (see docs/ops/redis-cluster-deployment.md). The test skips when the env is
# unset; without it CI compile-gates the cluster build tag via
# `go vet -tags=integration_cluster` instead of running the live tests.
# ---------------------------------------------------------------------------

test-integration-cluster:
	@if [ -z "$$GOCELL_TEST_REDIS_CLUSTER_ADDRS" ]; then \
		echo "GOCELL_TEST_REDIS_CLUSTER_ADDRS is unset; cluster tests will skip."; \
		echo "Launch grokzen/redis-cluster locally and export the seed addresses first."; \
	fi
	go test -tags=integration_cluster ./adapters/redis/... -count=1 -timeout 5m -v

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
