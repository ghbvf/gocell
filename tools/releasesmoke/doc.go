// Package releasesmoke holds the external-consumer smoke test for the
// synchronized multi-module release (#1843). The test body is build-tagged
// `releasesmoke` (see external_get_smoke_test.go); this file keeps the package
// non-empty so `go build ./...` and `go vet ./...` do not trip over a
// constraint-only directory.
//
// INVARIANT: RELEASE-EXTERNAL-GET-01
//
// Proves an EXTERNAL consumer can `go get github.com/ghbvf/gocell/<satellite>@vX.Y.Z`
// and `go list -m all` against the release-BUMPED library go.mods, via a hermetic
// local file GOPROXY — no real git tag, no network. This is the machine-checked
// successor to the "external-consumer smoke, run once by hand" deferral named in
// ROOT-MODULE-NO-REPLACE-01's godoc (#1767).
//
// # How it stays hermetic, and why keeping replace is correct
//
// The proxy serves an INTERNAL-DEPS-ONLY PROJECTION of each module: external
// requires are stripped and the stub package blank-imports the module's internal
// siblings. So a consumer that imports the probe traverses the real internal
// graph (postgres → gocell, adapterutil) under a fresh per-test GOMODCACHE — no
// network, no warm cache, no external universe. The published go.mod still
// carries its local `replace … => ../..` (OTel-canonical shape); Go ignores a
// dependency module's replace, so the consumer resolves the BUMPED `require …
// vX.Y.Z` from each sibling's proxy version (replace inert). The transitive
// assertion (gocell AND adapterutil resolve to the synthetic version, none at
// v0.0.0, none via `=>`) is the anti-vacuity teeth.
//
// # Synthetic red case (ai-robust §archtest 文件命名)
//
// TestExternalGet_IncompleteSet_Fails publishes every module EXCEPT one internal
// sibling (gocell) — the real #1842/#1843 failure mode "a satellite with no
// per-module tag" — and asserts the consumer build FAILS to resolve the missing
// sibling. This proves the green path depends on the COMPLETE synchronized
// published set, not a vacuous pass.
//
// Note on the bump vs. publishing: once every module IS published at one version,
// Go's MVS leniently upgrades any stale `v0.0.0` lower bound to the available
// version, so an unbumped-but-published proxy is NOT a single-version
// discriminator — the bump's value is compatible CROSS-version pinning (the
// published go.mod declaring the tested-compatible sibling version), which is
// byte-locked separately by modrelease's BumpModule golden. This smoke proves the
// publish→resolve half end-to-end; the rewrite-correctness half is the golden.
//
// # Grading: Medium (correct ceiling, not Soft)
//
// "external resolution works" is only checkable by running the real go toolchain
// against the real bumped+projected artifacts — an execution guard, inherently
// not Hard-expressible (a go.mod is data, not a type). It is far above Soft (no
// comment/checklist; it executes the toolchain). No cheap Hard-upgrade exists, so
// none is opened. Faithfulness gaps it does NOT cover (network-inherent): VCS
// tag→version mapping, GOSUMDB interop, pseudo-versions, v2+ major suffix, and the
// external (non-gocell) dependency closure — the tag-name shape is covered
// separately by PUBLISHABLE-MODULE-SET-01's tag-path assertion; the rest stay the
// "run once by hand on the real tag" residual.
package releasesmoke
