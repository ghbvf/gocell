.PHONY: build check-build test fmt verify validate generate proto-lint proto-gen cover clean \
        install-hooks update-archtest-golden update-modrelease-golden release-smoke \
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
# examples/* are their own go.work modules (#1556), so a root `./examples/...`
# pattern matches zero packages here; their release-consistency build is covered
# by hack/verify-workspace.sh (GOWORK=off per-module). `make build` ships the cmd
# binaries. cmd/gocell (#1557) and cmd/corebundle (#1559) are each their own
# go.work module now, so a root `./cmd/...` wildcard would match only the
# cmd/internal library (compiled transitively by both binaries) — both binaries
# are built explicitly via their dir paths (the explicit dir path resolves the
# satellite under go.work; CWD stays the repo root so bin/ is the repo bin/).
# `go generate ./cmd/corebundle/` runs the corebundle catalog codegen in-module
# (its directive pins --module-path to the base module, #1559).
build:
	mkdir -p bin
	go generate ./cmd/corebundle/
	go build -tags=catalog_gen -o bin/ ./cmd/corebundle
	go build -o bin/ ./cmd/gocell

# check-build is the full-repo compile check (no artefacts). Module-aware
# (#1556/#1557): iterate every go.work member from the single funnel
# (hack/lib/modules.sh) and build each, so root AND satellite modules compile
# (a bare root `./...` stops at the nested-module boundary).
# The CodeQL security workflow runs this as its trace build, so its extractor
# follows automatically; the deliberately-broken archtest fixtures under
# tools/archtest/testdata/ stay out because they are not go.work members.
# Uses `( cd "$$d" && go build ... ./... )` rather than `go -C "$$d" build`:
# under CodeQL's Go build-tracer the canonical `go build` command form is what
# the tracing shim recognizes — a leading `-C` flag makes it miss the build and
# the database ends up empty ("no source code seen during build"). `-o` points at
# a temp directory so main-package modules do not leave binaries in satellite dirs.
# Library-only modules (no main package, e.g. cellmodules #1559) build WITHOUT `-o`
# because `go build -o <dir> ./...` errors "no main packages to build" — a plain
# `go build ./...` compile-checks every package and produces no binary to pollute
# the dir. Both forms are the canonical `( cd "$$d" && go build ./... )` the CodeQL
# tracer recognizes. Fail-closed: a broken funnel aborts under `set -e`.
check-build:
	@bash -c 'set -euo pipefail; \
	build_out="$$(mktemp -d)"; trap '\''rm -rf "$$build_out"'\'' EXIT; \
	source hack/lib/util.sh; source hack/lib/modules.sh; \
	dirs="$$(gocell::modules::dirs)"; \
	while IFS= read -r d; do [ -n "$$d" ] || continue; \
	  echo "+++ go build ($$d)"; \
	  if ( cd "$$d" && go list -f '\''{{.Name}}'\'' ./... 2>/dev/null ) | grep -qx main; then \
	    ( cd "$$d" && go build -o "$$build_out/" ./... ); \
	  else \
	    ( cd "$$d" && go build ./... ); \
	  fi; \
	done <<< "$$dirs"'

# Root `./...` stops at nested-module boundaries. Iterate every
# go.work member from the single funnel (hack/lib/modules.sh → `go work edit
# -json`) and `go -C "$$dir" test ./...` each — covers root + every satellite
# (#1556) with zero hardcoded list, and an ambient GOWORK=off can't silently
# mis-narrow coverage the way a hardcoded
# `github.com/ghbvf/gocell/examples/...` wildcard would. Fail-closed: a broken
# funnel (missing jq / malformed go.work) aborts under `set -e` rather than
# skipping satellites. Mirrors hack/verify-workspace-test.sh + the CI lanes.
test:
	@bash -c 'set -euo pipefail; \
	source hack/lib/util.sh; source hack/lib/modules.sh; \
	dirs="$$(gocell::modules::dirs)"; \
	while IFS= read -r d; do [ -n "$$d" ] || continue; \
	  echo "+++ go test ($$d)"; go -C "$$d" test ./... -count=1; \
	done <<< "$$dirs"'

# fmt rewrites Go sources in place via every formatter declared under
# .golangci.yml `formatters.enable` (currently gofmt + goimports + gofumpt).
# Pair-mate of `make verify` (specifically hack/verify-gofumpt.sh): fmt fixes,
# verify checks. Root `./...` stops at nested-module boundaries, so iterate every
# go.work member through hack/lib/modules.sh and run the formatter in that
# module with the repo-root config.
#
# golangci-lint is bootstrapped from hack/lib/golangci-lint.sh at the version
# pinned to .github/workflows/_build-lint.yml — never from $PATH — so local
# fmt and CI lint apply identical formatter rules.
#
# ref: kubernetes/kubernetes hack/update-gofmt.sh + hack/verify-golangci-lint.sh.
fmt:
	@bash -c 'set -euo pipefail; \
	repo_root="$$(pwd -P)"; \
	source hack/lib/util.sh; source hack/lib/modules.sh; source hack/lib/golangci-lint.sh; \
	golangci_lint="$$(gocell::golangci_lint::ensure)"; \
	dirs="$$(gocell::modules::dirs)"; \
	while IFS= read -r d; do [ -n "$$d" ] || continue; \
	  echo "+++ golangci-lint fmt ($$d)"; \
	  ( cd "$$d" && "$$golangci_lint" fmt -c "$$repo_root/.golangci.yml" ./... ); \
	done <<< "$$dirs"'

# update-archtest-golden regenerates the diagnostic golden files of golden-based
# archtests (errcode / clock / span / … fixtures), then leaves the diff for
# review as a first-class artifact (see AssertGolden godoc). It does NOT touch
# ARCHTEST-MODULE-PATH-FUNNEL-01's frozen baseline (testdata/module_path_funnel.baseline),
# which is shrink-only and hand-maintained by design — never auto-regenerated.
# -tags=archtest: the golden tests are leaf *_test.go files gated behind
# //go:build archtest (ARCHTEST-LEAF-BUILD-TAG-01); without the tag the load is
# `[no test files]` and -update is a silent no-op. Leaf-only (no /...): the
# -update flag is registered solely in archtest's own test binary, so widening
# to internal/ subpackages would fail with "flag provided but not defined: -update".
update-archtest-golden:
	go test -tags=archtest ./tools/archtest -update -count=1

# update-modrelease-golden regenerates testdata/publishable.golden, the
# byte-frozen derived publishable module set (PUBLISHABLE-MODULE-SET-01). Run
# after intentionally adding/removing a go.work member or changing IsPublishable.
update-modrelease-golden:
	go test ./tools/modrelease -run TestPublishableModuleSet01 -update -count=1

# release-smoke runs the external-consumer go-get/build smoke for the
# synchronized multi-module release (RELEASE-EXTERNAL-GET-01). Network-free at
# test execution, Docker-free. This is a convenience shortcut; the CI gate
# hack/verify-release-smoke.sh (workspace bucket) additionally enforces the
# anti-vacuity / build-tag-presence guards a bare `go test` does not.
release-smoke:
	go test -tags=releasesmoke -count=1 ./tools/releasesmoke/...

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
	@bash -c 'set -euo pipefail; \
	cover_tmp="$$(mktemp -d)"; trap '\''rm -rf "$$cover_tmp"'\'' EXIT; \
	source hack/lib/util.sh; source hack/lib/modules.sh; \
	dirs="$$(gocell::modules::dirs)"; \
	printf "mode: set\n" > coverage.out; \
	while IFS= read -r d; do [ -n "$$d" ] || continue; \
	  profile="$$cover_tmp/$$(printf "%s" "$$d" | tr "/." "__").out"; \
	  echo "+++ go test cover ($$d)"; \
	  go -C "$$d" test ./... -coverprofile="$$profile"; \
	  if [ -s "$$profile" ]; then tail -n +2 "$$profile" >> coverage.out; fi; \
	done <<< "$$dirs"; \
	go tool cover -func=coverage.out | tail -1'

clean:
	rm -rf bin/
	rm -f coverage.out
	rm -f gocell corebundle iotdevice ssobff todoorder
	rm -f examples/corebundlestarter/corebundlestarter \
		examples/iotdevice/iotdevice \
		examples/orderfulfillment/orderfulfillment \
		examples/ssobff/ssobff \
		examples/todoorder/todoorder
	rm -f cmd/gocell/gocell cmd/corebundle/corebundle

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
	# corecells is its own go.work module (#1560); root ./corecells/... matches
	# zero packages. Run the root packages first, then run corecells in-module.
	GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags=integration,e2e \
		github.com/ghbvf/gocell/adapters/... \
		github.com/ghbvf/gocell/tests/integration/... \
		./tests/e2e/internal/... \
		github.com/ghbvf/gocell/cmd/corebundle/... \
		github.com/ghbvf/gocell/examples/ssobff/... \
		./runtime/bootstrap/... \
		-count=1 -timeout 15m -v
	GOCELL_TEST_DOCKER_REQUIRED=1 go -C corecells test -tags=integration,e2e \
		./accesscore/... \
		./configcore/... \
		./auditcore/... \
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
	go test -tags=integration_cluster github.com/ghbvf/gocell/adapters/redis/... -count=1 -timeout 5m -v

# ---------------------------------------------------------------------------
# examples/ssobff startup smoke
# Builds the demo binary and runs TestSSOBFFStartupSmoke (subprocess +
# /readyz probe + SIGTERM graceful path). Mirrors the CI examples-smoke
# job; useful before pushing a main.go / option-wiring change.
# ---------------------------------------------------------------------------

# `-C examples/ssobff`: ssobff is its own go.work module (#1556); run from inside.
test-examples-smoke:
	go test -C examples/ssobff -tags=examples_smoke -count=1 -timeout 90s -run TestSSOBFFStartupSmoke -v ./...

# ---------------------------------------------------------------------------
# Healthcheck verification  (T09)
# Delegates to scripts/healthcheck-verify.sh
# ---------------------------------------------------------------------------

healthcheck-verify:
	bash scripts/healthcheck-verify.sh
