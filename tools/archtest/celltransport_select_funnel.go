package archtest

// celltransport_select_funnel.go — importable CELLTRANSPORT-SELECT-FUNNEL-01
// rule logic (US5 #1966). Non-test home so the detector core can be shared by
// the production scan, the synthetic RED fixture test, and the dot-import
// blind-spot guard — single source, no parallel rule body.

// # CELLTRANSPORT-SELECT-FUNNEL-01
//
// transport.NewRemoteHTTP is the sole constructor for the remote
// CellTransport implementation. Wiring-layer packages (cmd/*, cellmodules/*,
// examples/*) MUST NOT call it directly: they must route through
// cellmodules/celltransport.Resolve, which gates the selection on the sealed
// deployment topology and enforces fail-closed invariants for unclassified
// cells.
//
// A wiring-layer package that calls transport.NewRemoteHTTP directly:
//  1. Hard-codes "remote" for a cell that may be co-located in a different
//     assembly configuration — wrong CellTransport for that topology.
//  2. Bypasses the nil-inProc guard for co-located cells.
//  3. Bypasses the KindInternal defense-in-depth for unclassified cells.
//
// This is the call-granularity sibling of REPLAYDEPS-INMEM-FUNNEL-01 (whose
// pattern this rule mirrors): a depguard import-ban cannot express it because
// runtime/transport is legitimately imported for its CellTransport interface,
// TransportMode, and Metrics types.
//
// The sanctioned caller (cellmodules/celltransport) is not under any scanned
// root, so its own transport.NewRemoteHTTP call is naturally out of scope.
//
// # AI-robust grade: Medium (AST callsite scan)
//
// The upstream seal (REMOTE-TRANSPORT-SEALED-01) is Hard (reflect field
// freeze: zero exported fields on RemoteHTTPTransport). The downstream
// restriction on wiring-layer construction is a CI-time AST scan (Medium),
// mirroring REPLAYDEPS-INMEM-FUNNEL-01. A Hard form (sealing NewRemoteHTTP
// behind a type funnel) is deferred: adapters/ legitimately holds *http.Client
// and would need a special carve-out, making a depguard import-ban on
// net/http unfeasible across the board.
//
// # Blind spots
//
//   - Dot-import of runtime/transport: `import . ".../transport"; NewRemoteHTTP(...)` —
//     the symbol becomes a bare ident the SelectorExpr scan misses. Closed by the
//     reverse self-test TestCELLTRANSPORT_SELECT_FUNNEL_01_NoDotImportBlindSpot.
//   - Function-value reference `f := transport.NewRemoteHTTP; f(...)`: COVERED —
//     firstQualifiedSelectorLine walks every <alias>.<sel> SelectorExpr, not just
//     CallExpr.Fun.
//   - Reflection-based construction: out of scope, treated as theoretical.

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// runtimeTransportModule is the import path of the transport package whose
// NewRemoteHTTP constructor the funnel bans in wiring-layer roots.
const runtimeTransportModule = PlatformFrameworkModulePath + "/runtime/transport"

// celltransportWiringRoots are the module-relative wiring-layer directory
// prefixes the funnel scans. These are the roots that must NOT call
// transport.NewRemoteHTTP directly — they must route through
// cellmodules/celltransport.Resolve. The celltransport package itself is
// intentionally NOT scanned (it is the sanctioned constructor).
var celltransportWiringRoots = []string{"cmd", "cellmodules", "examples"}

// CheckCelltransportSelectFunnel01 scans the wiring-layer roots for direct
// calls to transport.NewRemoteHTTP and returns one Diagnostic per offending
// callsite. It is the single rule body — the Test* dogfoods it via Report —
// so the exact scan is the one enforced.
//
// The allowlist excludes cellmodules/celltransport (the sole sanctioned caller):
// because celltransport lives under cellmodules/ (a scanned root), its path is
// explicitly allowlisted here rather than excluded from the root set, so that
// other packages under cellmodules/ are still scanned correctly.
func CheckCelltransportSelectFunnel01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, celltransportWiringRoots).Files()
	if err != nil {
		t.Fatalf("CELLTRANSPORT-SELECT-FUNNEL-01: scanner.DirsScope: %v", err)
	}

	var diags []Diagnostic
	for _, path := range files {
		rel := funnelRelSlash(root, path)

		// Allowlist: the sanctioned constructor in celltransport/resolve.go.
		if isCelltransportAllowlisted(rel) {
			continue
		}

		line, ok, perr := firstQualifiedSelectorLine(path, runtimeTransportModule, "transport", "NewRemoteHTTP")
		if perr != nil {
			t.Fatalf("CELLTRANSPORT-SELECT-FUNNEL-01: parse %s: %v", rel, perr)
		}
		if ok {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: "transport.NewRemoteHTTP in a wiring-layer package outside celltransport/; " +
					"route through cellmodules/celltransport.Resolve " +
					"(CELLTRANSPORT-SELECT-FUNNEL-01)",
			})
		}
	}
	return diags
}

// isCelltransportAllowlisted reports whether rel is the sanctioned construction
// site (cellmodules/celltransport/resolve.go). Using path prefix rather than
// exact file so future helpers in the same package are also allowed.
func isCelltransportAllowlisted(rel string) bool {
	// rel uses forward slashes (funnelRelSlash guarantee).
	const allowedPrefix = "cellmodules/celltransport/"
	if len(rel) >= len(allowedPrefix) && rel[:len(allowedPrefix)] == allowedPrefix {
		return true
	}
	return false
}
