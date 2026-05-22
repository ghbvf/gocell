package requireddepsgen_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/codegen/requireddepsgen"
)

// writeFixture writes src to a temp dir as service.go and returns the dir path.
func writeFixture(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	//nolint:gosec // dir is t.TempDir(), path is controlled
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("writeFixture: %v", err)
	}
	return dir
}

func TestGenerate_AllInterfaceFields_BasicFunnel(t *testing.T) {
	src := `package mypkg

type Repo interface{ Get() }
type Store interface{ Put() }
type Emitter interface{ Emit() }

type Service struct {
	repo    Repo    ` + "`gocell:\"required\"`" + `
	store   Store   ` + "`gocell:\"required\"`" + `
	emitter Emitter ` + "`gocell:\"required\"`" + `
}
`
	dir := writeFixture(t, src)
	out, err := requireddepsgen.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := string(out)

	for _, field := range []string{"repo", "store", "emitter"} {
		want := "validation.IsNilInterface(s." + field + ")"
		if !strings.Contains(got, want) {
			t.Errorf("missing guard for field %q: want %q in output:\n%s", field, want, got)
		}
	}
}

func TestGenerate_InterfaceVsPointer(t *testing.T) {
	src := `package mypkg

type Repo interface{ Get() }
type Concrete struct{}

type Service struct {
	repo    Repo       ` + "`gocell:\"required\"`" + `
	ptr     *Concrete  ` + "`gocell:\"required\"`" + `
}
`
	dir := writeFixture(t, src)
	out, err := requireddepsgen.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "validation.IsNilInterface(s.repo)") {
		t.Errorf("expected IsNilInterface for interface field repo; got:\n%s", got)
	}
	if !strings.Contains(got, "s.ptr == nil") {
		t.Errorf("expected == nil check for pointer field ptr; got:\n%s", got)
	}
}

func TestGenerate_TagOverrides(t *testing.T) {
	// Tags use separate lines in the string to avoid exceeding line length limits.
	overrideTags := `gocell:"required"` +
		` gocellKind:"KindInvalid"` +
		` gocellCode:"ErrValidationFailed"` +
		` gocellErr:"mypkg: TxRunner required; use WithTxManager"`
	src := "package mypkg\n\ntype TxRunner interface{ Run() }\n\ntype Service struct {\n\ttxRunner TxRunner `" +
		overrideTags + "`\n}\n"
	dir := writeFixture(t, src)
	out, err := requireddepsgen.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "errcode.KindInvalid") {
		t.Errorf("expected KindInvalid override; got:\n%s", got)
	}
	if !strings.Contains(got, "errcode.ErrValidationFailed") {
		t.Errorf("expected ErrValidationFailed override; got:\n%s", got)
	}
	if !strings.Contains(got, "mypkg: TxRunner required; use WithTxManager") {
		t.Errorf("expected custom error message; got:\n%s", got)
	}
}

func TestGenerate_NoRequiredFields_DegenerateEmit(t *testing.T) {
	src := `package mypkg

import "log/slog"

type Service struct {
	logger *slog.Logger
}
`
	dir := writeFixture(t, src)
	out, err := requireddepsgen.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "func (s *Service) validateRequired() error") {
		t.Errorf("expected validateRequired function; got:\n%s", got)
	}
	if !strings.Contains(got, "return nil") {
		t.Errorf("expected 'return nil' body; got:\n%s", got)
	}
	// No imports of errcode or validation since no guards needed.
	if strings.Contains(got, `"github.com/ghbvf/gocell/pkg/errcode"`) {
		t.Errorf("unexpected errcode import in degenerate case; got:\n%s", got)
	}
	if strings.Contains(got, `"github.com/ghbvf/gocell/pkg/validation"`) {
		t.Errorf("unexpected validation import in degenerate case; got:\n%s", got)
	}
}

func TestGenerate_UnknownTagValue_ReturnsError(t *testing.T) {
	src := `package mypkg

type Repo interface{ Get() }

type Service struct {
	repo Repo ` + "`gocell:\"requied\"`" + `
}
`
	dir := writeFixture(t, src)
	_, err := requireddepsgen.Generate(dir)
	if err == nil {
		t.Fatal("expected error for unknown tag value, got nil")
	}
	if !errors.Is(err, requireddepsgen.ErrUnknownTagValue) {
		t.Errorf("expected ErrUnknownTagValue; got %v", err)
	}
}

func TestGenerate_DeterministicOrder(t *testing.T) {
	src := `package mypkg

type A interface{ A() }
type B interface{ B() }
type C interface{ C() }

type Service struct {
	a A ` + "`gocell:\"required\"`" + `
	b B ` + "`gocell:\"required\"`" + `
	c C ` + "`gocell:\"required\"`" + `
}
`
	dir := writeFixture(t, src)
	first, err := requireddepsgen.Generate(dir)
	if err != nil {
		t.Fatalf("Generate first: %v", err)
	}
	for i := 0; i < 50; i++ {
		out, err := requireddepsgen.Generate(dir)
		if err != nil {
			t.Fatalf("Generate iteration %d: %v", i, err)
		}
		if !bytes.Equal(out, first) {
			t.Fatalf("non-deterministic output at iteration %d", i)
		}
	}
}

func TestGenerate_FieldOrder_FollowsStructDeclaration(t *testing.T) {
	src := `package mypkg

type Alpha interface{ A() }
type Beta interface{ B() }
type Gamma interface{ G() }

type Service struct {
	alpha Alpha ` + "`gocell:\"required\"`" + `
	beta  Beta  ` + "`gocell:\"required\"`" + `
	gamma Gamma ` + "`gocell:\"required\"`" + `
}
`
	dir := writeFixture(t, src)
	out, err := requireddepsgen.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := string(out)

	posAlpha := strings.Index(got, "s.alpha")
	posBeta := strings.Index(got, "s.beta")
	posGamma := strings.Index(got, "s.gamma")
	if posAlpha < 0 || posBeta < 0 || posGamma < 0 {
		t.Fatalf("missing field reference in output:\n%s", got)
	}
	if posAlpha >= posBeta || posBeta >= posGamma {
		t.Errorf("guard order does not follow struct declaration order; alpha=%d beta=%d gamma=%d\n%s",
			posAlpha, posBeta, posGamma, got)
	}
}

func TestGenerate_Golden_Sessionlogin(t *testing.T) {
	srcPath := filepath.Join("testdata", "golden", "sessionlogin_service.go")
	wantPath := filepath.Join("testdata", "golden", "sessionlogin_service_required_gen.go")

	//nolint:gosec // testdata paths are hardcoded relative paths, not user input
	srcBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read fixture input: %v", err)
	}
	//nolint:gosec // testdata paths are hardcoded relative paths, not user input
	wantBytes, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read golden output: %v", err)
	}

	dir := writeFixture(t, string(srcBytes))
	got, err := requireddepsgen.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !bytes.Equal(got, wantBytes) {
		t.Errorf("golden mismatch.\nGOT:\n%s\nWANT:\n%s", got, wantBytes)
	}
}

func TestGenerate_Golden_FlagwriteSkipsClock(t *testing.T) {
	srcPath := filepath.Join("testdata", "golden", "flagwrite_service.go")
	//nolint:gosec // testdata paths are hardcoded relative paths, not user input
	srcBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	dir := writeFixture(t, string(srcBytes))
	out, err := requireddepsgen.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := string(out)

	if strings.Contains(got, "clock") {
		t.Errorf("expected no reference to clock field (no gocell:\"required\" tag); got:\n%s", got)
	}
	// repo and txRunner should still be guarded
	if !strings.Contains(got, "s.repo") {
		t.Errorf("expected guard for repo field; got:\n%s", got)
	}
	if !strings.Contains(got, "s.txRunner") {
		t.Errorf("expected guard for txRunner field; got:\n%s", got)
	}
}

func TestGenerate_NoServiceStruct_ReturnsError(t *testing.T) {
	src := `package mypkg

type Handler struct {
	foo string
}
`
	dir := writeFixture(t, src)
	_, err := requireddepsgen.Generate(dir)
	if err == nil {
		t.Fatal("expected error for missing Service struct, got nil")
	}
	if !errors.Is(err, requireddepsgen.ErrNoServiceStruct) {
		t.Errorf("expected ErrNoServiceStruct; got %v", err)
	}
}
