// INVARIANT: SEED-ROLE-IFACE-01
//
// Production code must not name the concrete *mem.RoleRepository type. The rule
// logic lives in seed_role_iface.go (CheckSeedRoleIface01) so it is importable
// by an external Cell repo; this _test.go dogfoods the same Check (single
// source) and carries the RED/GREEN fixtures proving the scanner is not
// vacuously green. Full rule rationale + blind-spot inventory: seed_role_iface.go.
package archtest

import (
	"go/token"
	"os"
	"testing"
)

// TestSEED_ROLE_IFACE_01 dogfoods CheckSeedRoleIface01 against the GoCell tree.
func TestSEED_ROLE_IFACE_01(t *testing.T) {
	t.Parallel()
	Report(t, "SEED-ROLE-IFACE-01", CheckSeedRoleIface01(t, ConfigForExternalCell{}))
}

// TestSEED_ROLE_IFACE_01_RedFixture_FuncParam verifies the scanner correctly
// flags a function with parameter type *mem.RoleRepository.
func TestSEED_ROLE_IFACE_01_RedFixture_FuncParam(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
func badParam(repo *mem.RoleRepository) {}
`
	violations := scanSrcForViolations(t, src, "corecells/accesscore/some_prod.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (func param *mem.RoleRepository) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_RedFixture_StructField verifies the scanner flags
// a struct field of type *mem.RoleRepository.
func TestSEED_ROLE_IFACE_01_RedFixture_StructField(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
type Bad struct {
	Repo *mem.RoleRepository
}
`
	violations := scanSrcForViolations(t, src, "corecells/accesscore/cell.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (struct field *mem.RoleRepository) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_RedFixture_VarDecl verifies the scanner flags a
// var declaration with explicit type *mem.RoleRepository.
func TestSEED_ROLE_IFACE_01_RedFixture_VarDecl(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
var bad *mem.RoleRepository
`
	violations := scanSrcForViolations(t, src, "corecells/accesscore/cell.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (var *mem.RoleRepository) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_RedFixture_AliasedImport verifies the scanner flags
// the violation even when the package is imported under an alias.
func TestSEED_ROLE_IFACE_01_RedFixture_AliasedImport(t *testing.T) {
	t.Parallel()
	src := `package p
import accessmem "github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
type Bad struct {
	R *accessmem.RoleRepository
}
`
	violations := scanSrcForViolations(t, src, "corecells/accesscore/cell.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (aliased import) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_GreenFixture_NoImport verifies the scanner does NOT
// flag a file that does not import the mem package even if "RoleRepository"
// appears as text (e.g., in a comment or as a bare Ident from another type).
func TestSEED_ROLE_IFACE_01_GreenFixture_NoImport(t *testing.T) {
	t.Parallel()
	src := `package p
// This comment mentions mem.RoleRepository as documentation only.
type Other struct {
	Name string
}
`
	violations := scanSrcForViolations(t, src, "corecells/accesscore/cell.go")
	if len(violations) != 0 {
		t.Errorf("GREEN fixture (no mem import) must produce 0 violations; got %d: %v",
			len(violations), violations)
	}
}

// TestSEED_ROLE_IFACE_01_RedFixture_TypeAlias verifies the scanner flags a type
// alias declaration `type LocalRoleRepo = mem.RoleRepository` (T-ALIAS RED fixture
// from the blind-spot inventory).
func TestSEED_ROLE_IFACE_01_RedFixture_TypeAlias(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
type LocalRoleRepo = mem.RoleRepository
`
	violations := scanSrcForViolations(t, src, "corecells/accesscore/some_prod.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (type alias mem.RoleRepository) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_RedFixture_TypeAliasPointer verifies the scanner flags
// `type LocalRoleRepo = *mem.RoleRepository` (StarExpr-wrapped alias form).
func TestSEED_ROLE_IFACE_01_RedFixture_TypeAliasPointer(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
type LocalRoleRepo = *mem.RoleRepository
`
	violations := scanSrcForViolations(t, src, "corecells/accesscore/some_prod.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (type alias *mem.RoleRepository) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_GreenFixture_BlankImport verifies a blank import
// (side-effect only) of mem produces no violation; the blank alias cannot
// be used as a selector qualifier.
func TestSEED_ROLE_IFACE_01_GreenFixture_BlankImport(t *testing.T) {
	t.Parallel()
	src := `package p
import _ "github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
`
	violations := scanSrcForViolations(t, src, "corecells/accesscore/cell.go")
	if len(violations) != 0 {
		t.Errorf("GREEN fixture (blank import) must produce 0 violations; got %d: %v",
			len(violations), violations)
	}
}

// TestSEED_ROLE_IFACE_01_StructuredLocation verifies each emitted Diagnostic
// carries a real Rel + 1-based Line so Report renders "<rel>:<line>", not the
// ":0:" garbage an empty-Rel Diagnostic{Message} produces (PR #1687 review C1
// regression guard).
func TestSEED_ROLE_IFACE_01_StructuredLocation(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
func badParam(repo *mem.RoleRepository) {}
`
	diags := scanSrcForViolations(t, src, "corecells/accesscore/some_prod.go")
	if len(diags) == 0 {
		t.Fatalf("expected ≥1 diagnostic for *mem.RoleRepository param; got 0")
	}
	for _, d := range diags {
		if d.Rel == "" {
			t.Errorf("diagnostic must carry Rel (got empty): %+v", d)
		}
		if d.Line <= 0 {
			t.Errorf("diagnostic must carry a 1-based Line (got %d): %+v", d.Line, d)
		}
	}
}

// scanSrcForViolations is a test helper that writes src to a temp file and
// runs scanForMemRoleRepositoryUsage (from seed_role_iface.go) on it with the
// given relative path. It returns the structured Diagnostics so callers can
// assert both presence and the Rel/Line location contract.
func scanSrcForViolations(t *testing.T, src, rel string) []Diagnostic {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "seed_role_iface_*.go")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := tmp.WriteString(src); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}
	return scanForMemRoleRepositoryUsage(token.NewFileSet(), tmp.Name(), rel)
}
