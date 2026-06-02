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
// OwnerOfCode uses longest-prefix matching, so "ERR_AUTH_" and "ERR_AUTH_FORBIDDEN"
// can both be registered by different owners if needed (though GoCell currently
// uses a single owner for all platform codes).
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

	"github.com/ghbvf/gocell/pkg/panicregister"
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
// prefix must start with "ERR_" (fail-fast otherwise). A trailing underscore
// denotes a namespace claim ("ERR_AUTH_" owns all ERR_AUTH_* codes); a missing
// underscore denotes a whole-code claim ("ERR_INTERNAL" owns only that code).
//
// Registering the same (prefix, owner) pair more than once is idempotent.
// Registering the same prefix with a different owner panics with an *Error
// whose message names both owners and the conflicting prefix.
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

// OwnerOfCode returns the owner of code by longest-prefix matching.
//
// A registered key k matches iff strings.HasPrefix(string(code), k). Among
// all matching keys the longest one wins (so "ERR_AUTH_FORBIDDEN" matches
// "ERR_AUTH_" when both "ERR_AUTH_" and "ERR_" are registered, preferring
// the more specific entry).
//
// Returns ("", false) when no registered prefix covers code.
func OwnerOfCode(code Code) (string, bool) {
	prefixMu.RLock()
	defer prefixMu.RUnlock()

	codeStr := string(code)
	bestLen := -1
	bestOwner := ""

	for k, owner := range prefixRegistry {
		if strings.HasPrefix(codeStr, k) && len(k) > bestLen {
			bestLen = len(k)
			bestOwner = owner
		}
	}
	if bestLen < 0 {
		return "", false
	}
	return bestOwner, true
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
	"ERR_REFERENCE_BROKEN",
	"ERR_BODY_TOO_LARGE",
	"ERR_LIFECYCLE_INVALID",
	"ERR_DEPENDENCY_CYCLE",
	"ERR_INVALID_TIME_FORMAT",
	"ERR_ZERO_TEST_MATCH",

	// ── Namespace entries for single-code subsystem segments (subsystem nouns) ──
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
