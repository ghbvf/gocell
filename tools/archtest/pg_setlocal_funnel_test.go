// pg_setlocal_funnel_test.go — guards how the PR-3 row-level-security tenant GUC
// (app.tenant_id) is written in adapters/postgres.
//
//   - INVARIANT: PG-SETLOCAL-FUNNEL-01
//
// # What this guards (two prongs)
//
// Prong 1 — bare-SET tripwire. A session-scope `SET x = …` survives the
// connection's return to the pgxpool, so the next acquirer inherits the value:
// for app.tenant_id that is a cross-request tenant leak. Only `SET LOCAL`
// (transaction-scoped, auto-reset on COMMIT/ROLLBACK) is safe. This prong scans
// every `.Exec` call in adapters/postgres production for a string-literal SQL
// argument that begins with `SET ` (case-insensitive) but not `SET LOCAL `.
// Production uses the parameterized `set_config('app.tenant_id', $1, true)` form
// (= SET LOCAL semantics, zero interpolation), so this prong has ZERO production
// matches today — it is a regression tripwire. The reverse fixture
// (internal/pgsetlocalfixture) proves the scanner actually fires on a bare SET.
//
// Prong 2 — GUC-write funnel (the real lock). The app.tenant_id GUC write is the
// tenant-isolation injection point; it must live in exactly one sanctioned
// helper (tx_manager.go::setLocalTenant). This prong scans adapters/postgres
// production string literals for a GUC-WRITE statement targeting app.tenant_id
// (`set_config('app.tenant_id'…` / `SET [LOCAL] app.tenant_id`) and reports any
// outside tx_manager.go. The anti-vacuity check requires the sole sanctioned
// writer to be observed, so a scanner regression or a moved writer fails CI.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Prong 2 downstream: MEDIUM. Type-aware on the `.Exec` callee is not used
//     here because the GUC name is a magic string with no Go symbol; the lock is
//     (GUC-write-shaped string literal) AND (file ∉ allowlist). The shape match
//     is precise enough that error-message / comment mentions of "app.tenant_id"
//     do NOT match (they are not `set_config('app.tenant_id'` / `SET … app.tenant_id`
//     statements).
//   - Upstream: MEDIUM, Go-language ceiling. Hard upstream would be a sealed
//     GUC-write handle making "write app.tenant_id outside setLocalTenant"
//     compile-impossible; not reachable while the write is a raw SQL string to
//     pgx.Tx.Exec. Same permanent-ceiling family as #851/#893/#1282. Hard-upgrade
//     tracked as won't-do-now in gh #1619.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - A GUC write assembled by runtime string concatenation / fmt.Sprintf (not a
//     single literal) is invisible to both prongs. Production builds the statement
//     as one literal; a future dynamic form would need its own coverage. Same
//     known blind spot as every literal-scanning funnel in this suite.
//   - Prong 1 matches by method name `Exec` (+ SET-prefixed literal arg), so a DB
//     write through a differently-named wrapper is not seen; adapters/postgres
//     uses pgx `.Exec` directly.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adaptersPostgresPkgPath is derived from PlatformModulePath (not a bare literal)
// per ARCHTEST-MODULE-PATH-FUNNEL-01.
var adaptersPostgresPkgPath = PlatformModulePath + "/adapters/postgres"

// pgSetLocalGUCWriterFile is the sole sanctioned location of an app.tenant_id
// GUC write (Prong 2 allowlist + anti-vacuity anchor).
const pgSetLocalGUCWriterFile = "adapters/postgres/tx_manager.go"

// scanPGBareSet reports every `.Exec` call whose string-literal SQL argument
// begins with `SET ` but not `SET LOCAL ` (case-insensitive). Used over both
// adapters/postgres production (expect zero) and the RED fixture (expect one).
func scanPGBareSet(p *Pass) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	var d []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Exec" {
				return
			}
			for _, arg := range call.Args {
				s, ok := EvaluateConstString(p.TypesInfo, arg)
				if !ok {
					continue
				}
				up := strings.ToUpper(strings.TrimLeft(s, " \t\r\n"))
				if strings.HasPrefix(up, "SET ") && !strings.HasPrefix(up, "SET LOCAL ") {
					pos := p.Fset.Position(arg.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"PG-SETLOCAL-FUNNEL-01: bare session-scope SET in %s:%d — a session GUC survives the "+
								"connection's return to the pool and leaks across requests. Use SET LOCAL (or "+
								"set_config(name, value, true)) so the setting is transaction-scoped.",
							rel, pos.Line,
						),
					})
				}
			}
		})
	}
	return d
}

// isTenantGUCWrite reports whether a SQL string literal is a WRITE of the
// app.tenant_id GUC (set_config or SET [LOCAL]). It deliberately does NOT match
// a mere mention of the GUC name (error messages, comments-as-strings), only a
// write statement shape.
func isTenantGUCWrite(s string) bool {
	up := strings.ToUpper(s)
	return strings.Contains(up, "SET_CONFIG('APP.TENANT_ID'") ||
		strings.Contains(up, "SET LOCAL APP.TENANT_ID") ||
		strings.Contains(up, "SET APP.TENANT_ID")
}

// TestPGSetLocalFunnel01_BareSetBanned asserts no bare SET reaches the pool in
// adapters/postgres production code (Prong 1).
func TestPGSetLocalFunnel01_BareSetBanned(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil || p.Pkg.Path() != adaptersPostgresPkgPath {
			return nil
		}
		return scanPGBareSet(p)
	})
	Report(t, "PG-SETLOCAL-FUNNEL-01", diags)
}

// TestPGSetLocalFunnel01_FixtureCatchesBareSet is the reverse self-check: the RED
// fixture has one bare SET and one SET LOCAL; the scanner must flag exactly the
// bare SET.
func TestPGSetLocalFunnel01_FixtureCatchesBareSet(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	fixturePkg := modPath + "/tools/archtest/internal/pgsetlocalfixture"
	pattern := "./tools/archtest/internal/pgsetlocalfixture/..."
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != fixturePkg {
			return nil
		}
		return scanPGBareSet(p)
	})
	for _, d := range diags {
		t.Log(d.Message)
	}
	require.Len(t, diags, 1,
		"fixture must yield exactly 1 RED (badBareSet); goodSetLocal (SET LOCAL) must not be flagged")
	assert.Contains(t, diags[0].Message, "bare session-scope SET")
}

// scanPGTenantGUCWrite reports every app.tenant_id GUC-write string literal NOT in
// writerFile (the sole sanctioned writer), and separately whether writerFile itself
// was observed (anti-vacuity). Reused over both adapters/postgres production
// (writerFile = tx_manager.go → expect zero offenders, writer seen) and the RED
// fixture (the production writerFile never matches a fixture rel → every GUC write
// is reported).
func scanPGTenantGUCWrite(p *Pass, writerFile string) (offenders []Diagnostic, writerSeen bool) {
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.BasicLit](file, func(lit *ast.BasicLit) {
			if lit.Kind != token.STRING {
				return
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil || !isTenantGUCWrite(val) {
				return
			}
			if rel == writerFile {
				writerSeen = true
				return
			}
			pos := p.Fset.Position(lit.Pos())
			offenders = append(offenders, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"PG-SETLOCAL-FUNNEL-01: app.tenant_id GUC write in %s:%d is outside the sole sanctioned "+
						"writer (%s::setLocalTenant). The tenant RLS GUC injection is the isolation boundary and "+
						"must be funneled through one helper; do not write app.tenant_id elsewhere.",
					rel, pos.Line, pgSetLocalGUCWriterFile,
				),
			})
		})
	}
	return offenders, writerSeen
}

// TestPGSetLocalFunnel01_TenantGUCWriterFunnel asserts the app.tenant_id GUC
// write appears only in tx_manager.go (Prong 2) and that the sole writer is
// actually observed (anti-vacuity).
func TestPGSetLocalFunnel01_TenantGUCWriterFunnel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	observedWriter := false
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != adaptersPostgresPkgPath {
			return nil
		}
		offenders, writerSeen := scanPGTenantGUCWrite(p, pgSetLocalGUCWriterFile)
		if writerSeen {
			observedWriter = true
		}
		return offenders
	})
	Report(t, "PG-SETLOCAL-FUNNEL-01", diags)
	if !observedWriter {
		t.Errorf("PG-SETLOCAL-FUNNEL-01 anti-vacuity: expected an app.tenant_id GUC write in %s but found none — "+
			"the scanner regressed or setLocalTenant moved; the funnel would be silently vacuous.", pgSetLocalGUCWriterFile)
	}
}

// TestPGSetLocalFunnel01_FixtureCatchesGUCWrite is the reverse self-check for
// prong 2 (#1622 F4): the RED fixture holds an allowlist-external
// set_config('app.tenant_id', …) write — the CANONICAL production GUC-write form —
// alongside the bare-SET / SET-LOCAL forms. The prong-2 scanner must report all
// three (none is in the sanctioned writer file). Before this fixture, prong 2 had
// only production + anti-vacuity coverage, so a scanner regression that stopped
// matching the set_config shape would have passed silently as long as production
// itself stayed clean.
func TestPGSetLocalFunnel01_FixtureCatchesGUCWrite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	fixturePkg := modPath + "/tools/archtest/internal/pgsetlocalfixture"
	pattern := "./tools/archtest/internal/pgsetlocalfixture/..."
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != fixturePkg {
			return nil
		}
		// pgSetLocalGUCWriterFile (adapters/postgres/tx_manager.go) never matches a
		// fixture rel, so every app.tenant_id GUC write in the fixture is reported.
		offenders, _ := scanPGTenantGUCWrite(p, pgSetLocalGUCWriterFile)
		return offenders
	})
	for _, d := range diags {
		t.Log(d.Message)
	}
	// The fixture holds exactly three app.tenant_id GUC writes — bare SET, SET LOCAL,
	// and set_config — all outside the sanctioned writer. A count of 2 means the
	// set_config production form was dropped (fixture regression) or the scanner
	// stopped matching it.
	require.Len(t, diags, 3,
		"prong-2 scanner must report all three allowlist-external app.tenant_id GUC writes "+
			"(bare SET / SET LOCAL / set_config), proving the canonical set_config form is covered")
}
