package errcode

// Package errcode — prefix ownership registry.
//
// # Purpose
//
// RegisterPrefix / RegisteredPrefixes / OwnerOfCode implement a closed-set
// namespace registry that maps every production errcode.Code to an owning Go
// module. The registry enforces two structural rules:
//
//  1. Namespace entries (trailing underscore, e.g. "ERR_AUTH_") claim all codes
//     whose string starts with the prefix.
//  2. Whole-code entries (no trailing underscore, e.g. "ERR_INTERNAL") claim
//     exactly one code by exact match.
//
// OwnerOfCode resolves a code to its owner: namespace entries match by prefix,
// whole-code entries match exactly, and the longest matching key wins. A single
// owner may register overlapping entries (e.g. "ERR_AUTH_" and
// "ERR_AUTH_FORBIDDEN") to refine ownership; cross-owner overlap is rejected at
// registration time (see RegisterPrefix).
//
// # Invariant
//
// Every code minted by errcode.New / errcode.Wrap in production must have a
// registered prefix entry. archtest ERRCODE-PREFIX-OWNERSHIP-01 enforces this
// at CI time by scanning all mint callsites and sentinel declarations.
//
// # Concurrency
//
// RegisterPrefix is goroutine-safe. It is intended to be called from package
// init functions before any test or service logic runs, so contention is
// unlikely; the mutex is present for correctness.
//
// # Reference
//
// Issue #1091 — closed-set errcode prefix ownership registry.
// ADR: docs/architecture/202606031200-1091-adr-errcode-prefix-ownership-registry.md

import (
	"sort"
	"strings"
	"sync"

	"github.com/ghbvf/gocell/framework/pkg/panicregister"
)

var (
	prefixMu       sync.RWMutex
	prefixRegistry = map[string]string{} // prefix → owner
)

// PrefixOwner is a snapshot entry from the prefix ownership registry.
// Prefix is the registered prefix string (e.g. "ERR_AUTH_" or "ERR_INTERNAL");
// Owner is the module path that owns that namespace (e.g. "github.com/ghbvf/gocell").
type PrefixOwner struct {
	Prefix string
	Owner  string
}

// RegisterPrefix registers a code-prefix or whole-code string as owned by owner.
//
// It is intended to be called from package init() functions before any test or
// service logic runs. Calling it after init (e.g. from a request handler) is
// unsupported and may cause lock contention.
//
// prefix must start with "ERR_" (fail-fast otherwise). A trailing underscore
// denotes a namespace claim ("ERR_AUTH_" owns all ERR_AUTH_* codes); a missing
// underscore denotes a whole-code claim ("ERR_INTERNAL" owns only that code).
//
// Registering the same (prefix, owner) pair more than once is idempotent.
// Registering the same prefix with a different owner panics with an *Error
// whose message names both owners and the conflicting prefix. Registering a
// DIFFERENT prefix whose claimed code-set overlaps an existing entry owned by a
// different owner (one prefix contains the other) also panics — cross-module
// namespace claims must be disjoint. Overlap within a single owner is allowed.
//
// The panic format args intentionally carry runtime strings (prefix + module
// path). This is an init-stage programmer-error panic (see panic taxonomy in
// error-handling.md); these values are non-sensitive identifiers (module path /
// prefix), not PII, and the panic surfaces only in startup crash logs. This
// usage is consistent with the MESSAGE-CONST-LITERAL-01 allowlist for
// errcode.Assertion — Assertion explicitly permits runtime context in its
// message for programmer-error panics.
//
// All panics use the panicregister.Approved funnel (PANIC-REGISTERED-01).
func RegisterPrefix(prefix, owner string) {
	if prefix == "" || owner == "" {
		panic(panicregister.Approved(
			"errcode-prefix-empty-arg",
			Assertion("errcode.RegisterPrefix: prefix and owner must both be non-empty (got prefix=%q owner=%q)", prefix, owner),
		))
	}
	if !strings.HasPrefix(prefix, "ERR_") {
		panic(panicregister.Approved(
			"errcode-prefix-malformed",
			Assertion("errcode.RegisterPrefix: prefix %q must start with \"ERR_\"", prefix),
		))
	}

	prefixMu.Lock()
	defer prefixMu.Unlock()

	if existing, ok := prefixRegistry[prefix]; ok {
		if existing == owner {
			return // idempotent same-owner re-registration
		}
		panic(panicregister.Approved(
			"errcode-prefix-owner-conflict",
			Assertion("errcode.RegisterPrefix: prefix %q already owned by %q, cannot reassign to %q", prefix, existing, owner),
		))
	}
	// Reject cross-owner namespace overlap. The registry's purpose is to keep
	// each owner's claimed code-set disjoint from other modules'; an exact-string
	// re-registration is caught above, but a NEW prefix whose claimed code-set
	// intersects a different owner's entry (one is a prefix of the other) is the
	// same collision and must fail-fast at startup, not coexist silently.
	// Same-owner overlap is intentional (e.g. gocell owns both "ERR_AUTH_" and
	// "ERR_AUTH_FORBIDDEN"; longest-key wins in OwnerOfCode).
	for existing, existingOwner := range prefixRegistry {
		if existingOwner == owner || !entriesOverlap(prefix, existing) {
			continue
		}
		panic(panicregister.Approved(
			"errcode-prefix-cross-owner-overlap",
			Assertion(
				"errcode.RegisterPrefix: prefix %q (owner %q) overlaps existing "+
					"prefix %q (owner %q); cross-module namespace claims must be disjoint",
				prefix, owner, existing, existingOwner,
			),
		))
	}
	prefixRegistry[prefix] = owner
}

// RegisteredPrefixes returns a sorted snapshot of all registered (Prefix, Owner) pairs.
// The slice is a copy — callers may modify it freely.
func RegisteredPrefixes() []PrefixOwner {
	prefixMu.RLock()
	defer prefixMu.RUnlock()

	out := make([]PrefixOwner, 0, len(prefixRegistry))
	for prefix, owner := range prefixRegistry {
		out = append(out, PrefixOwner{Prefix: prefix, Owner: owner})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Prefix < out[j].Prefix
	})
	return out
}

// OwnerOfCode returns the owner of code by longest-matching-key.
//
// A registered key matches per entryClaimsCode: a namespace entry (trailing
// "_") matches by prefix, a whole-code entry (no trailing "_") matches only by
// exact equality. Among all matching keys the longest one wins (so
// "ERR_AUTH_FORBIDDEN" matches "ERR_AUTH_" when both "ERR_AUTH_" and "ERR_" are
// registered, preferring the more specific entry).
//
// Returns ("", false) when no registered entry covers code.
func OwnerOfCode(code Code) (string, bool) {
	prefixMu.RLock()
	defer prefixMu.RUnlock()

	codeStr := string(code)
	bestLen := -1
	bestOwner := ""

	for k, owner := range prefixRegistry {
		if entryClaimsCode(k, codeStr) && len(k) > bestLen {
			bestLen = len(k)
			bestOwner = owner
		}
	}
	if bestLen < 0 {
		return "", false
	}
	return bestOwner, true
}

// entryClaimsCode reports whether a registered registry key claims code.
//
// A namespace entry (trailing "_", e.g. "ERR_AUTH_") claims every code whose
// string starts with the prefix. A whole-code entry (no trailing "_", e.g.
// "ERR_INTERNAL") claims exactly one code by EXACT match — it must NOT be
// treated as a prefix, otherwise "ERR_INTERNAL" would falsely claim an
// unrelated "ERR_INTERNAL_DETAIL" and let an unregistered code slip past the
// closed-set guard (ERRCODE-PREFIX-OWNERSHIP-01 reuses OwnerOfCode).
func entryClaimsCode(entry, code string) bool {
	if strings.HasSuffix(entry, "_") {
		return strings.HasPrefix(code, entry)
	}
	return code == entry
}

// entriesOverlap reports whether two registry keys claim any code in common.
// Used by RegisterPrefix to reject cross-owner collisions.
//
//   - namespace × namespace: one prefix contains the other
//   - namespace × whole-code: the whole-code lies under the namespace prefix
//   - whole-code × whole-code: identical (the exact-string case is handled by
//     RegisterPrefix's same-key check before this is reached)
func entriesOverlap(a, b string) bool {
	aNS := strings.HasSuffix(a, "_")
	bNS := strings.HasSuffix(b, "_")
	switch {
	case aNS && bNS:
		return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
	case aNS: // a namespace, b whole-code
		return strings.HasPrefix(b, a)
	case bNS: // a whole-code, b namespace
		return strings.HasPrefix(a, b)
	default: // both whole-code
		return a == b
	}
}

// gocellModuleOwner is the canonical module path for all GoCell platform codes.
const gocellModuleOwner = "github.com/ghbvf/gocell"

// gocellPlatformPrefixes lists every errcode prefix / whole-code that GoCell
// owns. Derived by scanning all 256 production Code constants in errcode.go:
//
//   - Namespace entry ("ERR_<SEG>_") for subsystem segments with ≥2 codes.
//   - Whole-code entry for the ERR_NOT_ exception + generic single-concept codes.
//   - Namespace entry for single-code subsystem segments (subsystem noun).
//
// Reference: ADR docs/architecture/202606031200-1091-adr-errcode-prefix-ownership-registry.md
// Issue #1091.
var gocellPlatformPrefixes = []string{
	// ── Namespace entries (≥2 codes per first segment, all subsystems) ──────────
	"ERR_ADAPTER_",
	"ERR_AUTH_",
	"ERR_WEBHOOK_",
	"ERR_CONFIG_",
	"ERR_WEBSOCKET_",
	"ERR_CELL_",
	"ERR_KEY_",
	"ERR_SECURECOOKIE_",
	"ERR_WS_",
	"ERR_AUDIT_",
	"ERR_CONTROLPLANE_",
	"ERR_DISTLOCK_",
	"ERR_FLAG_",
	"ERR_IDEMPOTENCY_",
	"ERR_METRICS_",
	"ERR_SAGA_",
	"ERR_ARCHIVE_",
	"ERR_BOOTSTRAP_",
	"ERR_FENCED_",
	"ERR_METADATA_",
	"ERR_OUTBOX_",
	"ERR_READYZ_",
	"ERR_RECONCILE_",
	"ERR_REFRESH_",
	"ERR_REGISTRATION_",
	"ERR_SESSION_",
	"ERR_VALIDATION_",

	// ── Whole-code entries (ERR_NOT_ exception + generic single-concept codes) ──
	"ERR_NOT_FOUND",
	"ERR_NOT_IMPLEMENTED",
	"ERR_INTERNAL",
	"ERR_CONFLICT",
	"ERR_RATE_LIMITED",
	"ERR_VERSION_CONFLICT",
	"ERR_SERVICE_UNAVAILABLE",
	"ERR_UPSTREAM_CELL_UNAVAILABLE",
	"ERR_REFERENCE_BROKEN",
	"ERR_BODY_TOO_LARGE",
	"ERR_LIFECYCLE_INVALID",
	"ERR_DEPENDENCY_CYCLE",
	"ERR_INVALID_TIME_FORMAT",
	"ERR_ZERO_TEST_MATCH",

	// ── Namespace entries for single-code subsystem segments (subsystem nouns) ──
	"ERR_CERT_",
	"ERR_COMMAND_",
	"ERR_DEVICE_",
	"ERR_ORDER_",
	"ERR_GRPC_",
	"ERR_VAULT_",
	"ERR_WORKER_",
	"ERR_SCAFFOLD_",
	"ERR_RELAY_",
	"ERR_OBSERVABILITY_",
	"ERR_LISTENER_",
	"ERR_JOURNEY_",
	"ERR_ENVELOPE_",
	"ERR_CONTRACT_",
	"ERR_ASSEMBLY_",
	"ERR_SLICE_",
	"ERR_PG_",
	"ERR_PROJECTION_",
	"ERR_CIRCUIT_",
	"ERR_CURSOR_",
	"ERR_CSRF_",
	"ERR_CHECKREF_",
	"ERR_BUS_",
	"ERR_SETUP_",
	"ERR_CLIENT_",
	"ERR_PAGE_",
	"ERR_SERVER_",
	"ERR_TEST_",
}

func init() {
	for _, p := range gocellPlatformPrefixes {
		RegisterPrefix(p, gocellModuleOwner)
	}
}
