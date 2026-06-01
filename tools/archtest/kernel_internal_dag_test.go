// INVARIANT: KERNEL-INTERNAL-DAG-01
//
// KERNEL-INTERNAL-DAG-01 — invariant-driven gate.
//
// Invariant: kernel-internal cross-owner imports must match the canonical
// adjacency declared in `allowedKernelEdges`. Every edge in the actual
// production import graph must be explicitly allowed; every declared edge
// must still exist in the code; the set of kernel sub-module owners must
// be exactly the set of allowlist keys.
//
// Owner is the first path segment under "kernel/" (so kernel/cell,
// kernel/cell/celltest, and kernel/cell/levelrank all resolve to owner
// "cell"). Self-edges within an owner are not asserted. Test-only nodes
// (depgraph.Node.TestOnly == true) are skipped to keep parity with
// LAYER-05/06 production-only semantics.
//
// AI-robust: Medium. Uses kernel/depgraph (typed import graph) plus a
// typed Go map allowlist; no string anchors, comment exemptions, or name
// conventions. The Go language has no Hard mechanism for "package A may
// not import package B" (internal/ inverts direction; //go:build does not
// constrain imports; type system cannot constrain import lists), so this
// is the language ceiling for this problem domain — see ai-robust.md.
//
// Rule: KERNEL-INTERNAL-DAG-01
package archtest

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
)

const ruleKernelInternalDAG = "KERNEL-INTERNAL-DAG-01"

// allowedKernelEdges is the canonical kernel-internal cross-owner adjacency
// table. Keys are the kernel sub-modules; values are sorted owner names
// that the key may import. nil means leaf (no kernel→kernel out-edges).
//
// Two leaves were added in an earlier PR — `contractspec` (extracted from
// kernel/wrapper) and `cellvocab` (extracted from kernel/cell, absorbing
// the existing kernel/cell/levelrank sub-package) — to break the
// cell→wrapper, governance→cell, and metadata→cell (via levelrank)
// reverse edges. After that PR the roadmap claim that wrapper is top-tier
// and governance/metadata do not reach into runtime cell becomes literally
// true; cellvocab becomes the single source of truth for the consistency
// level / contract kind / role / lifecycle / cell type vocabulary.
//
// `contractspec` was initially leaf (→cellvocab only). It now also imports
// `metadata` to use metadata.MatchCellID / metadata.CellIDPattern as the
// single-source cell-id validator (PR #487 review finding: drop byte-wise
// isCellIDLike duplicate).
//
// observability is a leaf imported by the upper-tier sub-modules
// (assembly/cell/outbox) for their metrics provider; this is a structural
// metrics dependency akin to wrapper→outbox and stays as-is.
//
// W6 added two saga sub-modules: `fsm` (leaf — the generic state-machine
// helper, no kernel→kernel out-edges) and `saga`. saga→fsm (status.go uses
// the fsm transition table), saga→clock (journal lease timing), saga→healthz
// (sagajournaltest RepoReady conformance prober — a non-test .go file, so the
// edge is counted). None of fsm/clock/healthz import saga back (no cycle).
//
// PR #1187 round-3 review F6 added healthz→clock: ctxsafe.go (Probe interface
// sealed marker + WrapCtxSafe ctx-racing wrapper) was sunk from
// runtime/observability/healthz to kernel/healthz so the sealed marker
// (`isHealthzProbe()` unexported) can be implemented by ctxSafeProbe. The
// ctx-racing logic needs clock.Clock for the late-outcome watcher; clock is
// a leaf so no cycle.
//
// KERNEL-WEBHOOK-01 PR-2 (#1265) registers `webhook`: the kernel/webhook pure
// HMAC signer/verifier kernel (PR-1 #1251) imports only clock (replay-window
// timing); clock is a leaf so no cycle. cell→webhook is the registration edge.
// kernel/projection and kernel/reconcile (W10 / #661) are registered as
// consumer-layer kernels: they depend on cell/outbox/wrapper/observability etc.
// but nothing imports them back, so no cycle is introduced. governance→saga
// (rules_saga.go validates saga contract shape) is likewise acyclic (saga does
// not import governance).
var allowedKernelEdges = map[string][]string{
	"assembly":      {"cell", "clock", "metadata", "observability", "outbox", "registry"},
	"auth":          {"cell"},
	"cell":          {"cellvocab", "contractspec", "healthz", "metadata", "outbox", "webhook"},
	"cellvocab":     nil,
	"clock":         nil,
	"command":       {"metautil"},
	"contractspec":  {"cellvocab", "metadata"},
	"crypto":        nil,
	"ctxkeys":       nil,
	"depgraph":      nil,
	"fsm":           nil,
	"governance":    {"cellvocab", "clock", "metadata", "registry", "saga", "verify"},
	"healthz":       {"clock"},
	"idempotency":   {"clock"},
	"journey":       {"metadata"},
	"lifecycle":     {"healthz", "worker"},
	"metadata":      {"cellvocab"},
	"metautil":      nil,
	"observability": nil,
	"outbox":        {"cellvocab", "clock", "fsm", "healthz", "idempotency", "metautil", "observability", "persistence"},
	"persistence":   nil,
	"projection":    {"cell", "cellvocab", "clock", "contractspec", "healthz", "observability", "outbox", "persistence", "wrapper"},
	"reconcile":     {"observability"},
	"registry":      {"metadata"},
	"saga":          {"clock", "fsm", "healthz"},
	"verify":        {"metadata"},
	"webhook":       {"clock"},
	"worker":        nil,
	"wrapper":       {"contractspec", "ctxkeys", "outbox"},
}

// kernelEdgeViolation describes a single DAG breach.
type kernelEdgeViolation struct {
	Kind    string // "forward" | "reverse" | "coverage-extra" | "coverage-missing"
	From    string
	To      string
	Message string
}

// kernelOwnerOf returns the kernel sub-module owner for pkgID — the first
// path segment after "kernel/" — or "" if pkgID is not under <module>/kernel/.
// modulePrefix must include the trailing slash (e.g. "github.com/ghbvf/gocell/").
func kernelOwnerOf(modulePrefix, pkgID string) string {
	const kernelSeg = "kernel/"
	want := modulePrefix + kernelSeg
	if !strings.HasPrefix(pkgID, want) {
		return ""
	}
	rel := strings.TrimPrefix(pkgID, want)
	if rel == "" {
		return ""
	}
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return rel
}

// TestKernelInternalDAG enforces KERNEL-INTERNAL-DAG-01.
func TestKernelInternalDAG(t *testing.T) {
	root := findModuleRoot(t)
	module := readModulePath(t, root)
	modPrefix := module + "/"

	g, _ := loadModule(t, root)
	require.NotEmpty(t, g.Packages, "depgraph returned no packages")

	// Build owner -> actual cross-owner out-edges set, restricted to
	// production (non-test-only) packages.
	actual := map[string]map[string]bool{}
	owners := map[string]bool{}
	for _, node := range g.Packages {
		if node.TestOnly {
			continue
		}
		fromOwner := kernelOwnerOf(modPrefix, node.ID)
		if fromOwner == "" {
			continue
		}
		owners[fromOwner] = true
		if actual[fromOwner] == nil {
			actual[fromOwner] = map[string]bool{}
		}
		for _, imp := range node.Imports {
			toOwner := kernelOwnerOf(modPrefix, imp)
			if toOwner == "" || toOwner == fromOwner {
				continue
			}
			actual[fromOwner][toOwner] = true
		}
	}

	violations := computeKernelDAGViolations(allowedKernelEdges, owners, actual)

	if len(violations) > 0 {
		// Deterministic order for stable failure output.
		sort.SliceStable(violations, func(i, j int) bool {
			if violations[i].Kind != violations[j].Kind {
				return violations[i].Kind < violations[j].Kind
			}
			if violations[i].From != violations[j].From {
				return violations[i].From < violations[j].From
			}
			return violations[i].To < violations[j].To
		})

		t.Logf("%s: %d violation(s) found:", ruleKernelInternalDAG, len(violations))
		for _, v := range violations {
			t.Logf("  [%s] %s", v.Kind, v.Message)
		}
	}

	assert.Empty(t, violations,
		"%s: kernel-internal DAG must match allowedKernelEdges exactly.",
		ruleKernelInternalDAG)

	// Sub-test 1: kernelOwnerOf folds sub-packages to first segment.
	//
	// kernel/cell/levelrank was absorbed into kernel/cellvocab during G-04;
	// the historical path is retained in this table to verify the folding
	// rule itself (string-only, not filesystem-bound) still classifies
	// hypothetical kernel sub-package paths correctly. This guards against
	// regressions where a future re-introduction of a cell sub-package
	// silently lands without owner-folding coverage.
	t.Run("kernelOwnerOf_folds_subpackages", func(t *testing.T) {
		cases := []struct {
			id, want string
		}{
			{module + "/kernel/cell", "cell"},
			{module + "/kernel/cell/celltest", "cell"},
			{module + "/kernel/cell/levelrank", "cell"}, // historical path; absorbed into cellvocab
			{module + "/kernel/outbox/outboxtest", "outbox"},
			{module + "/runtime/auth", ""},
			{module + "/kernel", ""},
			{"", ""},
		}
		for _, c := range cases {
			got := kernelOwnerOf(modPrefix, c.id)
			assert.Equal(t, c.want, got, "kernelOwnerOf(%q)", c.id)
		}
	})

	// Sub-test 2: depgraph LayerOf labels every owner under kernel/ as LayerKernel.
	t.Run("owners_classified_as_kernel_layer", func(t *testing.T) {
		for owner := range owners {
			pkg := module + "/kernel/" + owner
			assert.Equal(t, kerneldepgraph.LayerKernel,
				kerneldepgraph.LayerOf(module, pkg),
				"kernel/%s must classify as LayerKernel", owner)
		}
	})
}

// sortedSet returns the keys of m in deterministic order.
func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// computeKernelDAGViolations checks the three invariants of the kernel-internal
// DAG rule against the provided allowlist, owner set, and actual edge map.
// Extracted from TestKernelInternalDAG for unit-testability.
func computeKernelDAGViolations(
	allow map[string][]string,
	owners map[string]bool,
	edges map[string]map[string]bool,
) []kernelEdgeViolation {
	var violations []kernelEdgeViolation

	// Forward check: every actual cross-owner edge must be in allowlist.
	allowedKeys := make([]string, 0, len(edges))
	for k := range edges {
		allowedKeys = append(allowedKeys, k)
	}
	sort.Strings(allowedKeys)
	for _, from := range allowedKeys {
		allowSet := map[string]bool{}
		for _, to := range allow[from] {
			allowSet[to] = true
		}
		for _, to := range sortedSet(edges[from]) {
			if !allowSet[to] {
				violations = append(violations, kernelEdgeViolation{
					Kind: "forward",
					From: from,
					To:   to,
					Message: ruleKernelInternalDAG + ": kernel/" + from +
						" imports kernel/" + to + " (not in DAG allowlist)",
				})
			}
		}
	}

	// Reverse check: every allowlist edge must still exist in actual.
	allowKeys := make([]string, 0, len(allow))
	for k := range allow {
		allowKeys = append(allowKeys, k)
	}
	sort.Strings(allowKeys)
	for _, from := range allowKeys {
		actualSet := edges[from]
		for _, to := range allow[from] {
			if !actualSet[to] {
				violations = append(violations, kernelEdgeViolation{
					Kind: "reverse",
					From: from,
					To:   to,
					Message: ruleKernelInternalDAG + ": declared edge kernel/" + from +
						" → kernel/" + to + " no longer present in code; remove from allowlist",
				})
			}
		}
	}

	// Coverage check: actual owner set must equal allowlist key set.
	for _, owner := range sortedSet(owners) {
		if _, ok := allow[owner]; !ok {
			violations = append(violations, kernelEdgeViolation{
				Kind: "coverage-extra",
				From: owner,
				Message: ruleKernelInternalDAG + ": kernel sub-module kernel/" + owner +
					" exists but is not in allowedKernelEdges; add it",
			})
		}
	}
	for _, owner := range allowKeys {
		if !owners[owner] {
			violations = append(violations, kernelEdgeViolation{
				Kind: "coverage-missing",
				From: owner,
				Message: ruleKernelInternalDAG + ": kernel sub-module kernel/" + owner +
					" is in allowedKernelEdges but not present in code; remove it",
			})
		}
	}

	return violations
}

// TestKernelInternalDAG_ViolationKinds_TableDriven verifies that the
// computeKernelDAGViolations helper detects all four violation kinds.
func TestKernelInternalDAG_ViolationKinds_TableDriven(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		allow    map[string][]string
		owners   map[string]bool
		edges    map[string]map[string]bool
		wantKind string
	}{
		{
			name:   "forward — unauthorized edge detected",
			allow:  map[string][]string{"a": {"b"}, "b": nil},
			owners: map[string]bool{"a": true, "b": true},
			// a actually imports c, which is not in allowlist
			edges:    map[string]map[string]bool{"a": {"b": true, "c": true}, "b": {}},
			wantKind: "forward",
		},
		{
			name:  "reverse — declared edge no longer present",
			allow: map[string][]string{"a": {"b"}, "b": nil},
			// allow says a→b but actual edges has no a→b edge
			owners:   map[string]bool{"a": true, "b": true},
			edges:    map[string]map[string]bool{"a": {}, "b": {}},
			wantKind: "reverse",
		},
		{
			name:  "coverage-extra — owner exists but not in allowlist",
			allow: map[string][]string{"a": nil},
			// b exists in code but not in allowlist
			owners:   map[string]bool{"a": true, "b": true},
			edges:    map[string]map[string]bool{"a": {}, "b": {}},
			wantKind: "coverage-extra",
		},
		{
			name:  "coverage-missing — allowlist has key missing from code",
			allow: map[string][]string{"a": nil, "b": nil},
			// b is in allowlist but not in actual owners
			owners:   map[string]bool{"a": true},
			edges:    map[string]map[string]bool{"a": {}},
			wantKind: "coverage-missing",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vios := computeKernelDAGViolations(tc.allow, tc.owners, tc.edges)
			found := false
			for _, v := range vios {
				if v.Kind == tc.wantKind {
					found = true
					break
				}
			}
			require.True(t, found,
				"expected at least one %q violation; got %+v", tc.wantKind, vios)
		})
	}
}
