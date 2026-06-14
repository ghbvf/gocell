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

func TestGenerate_WithOpts_BuildTagHeaderEmitted(t *testing.T) {
	src := `package mypkg

type Repo interface{ Get() }

type Service struct {
	repo Repo ` + "`gocell:\"required\"`" + `
}
`
	dir := writeFixture(t, src)
	out, err := requireddepsgen.GenerateWithOpts(dir, requireddepsgen.Opts{BuildTag: "archtest_fixture"})
	if err != nil {
		t.Fatalf("GenerateWithOpts: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("//go:build archtest_fixture\n\n")) {
		t.Errorf("output must start with build tag header; got:\n%s", out)
	}
	// Empty BuildTag must NOT emit the header.
	plain, err := requireddepsgen.GenerateWithOpts(dir, requireddepsgen.Opts{})
	if err != nil {
		t.Fatalf("GenerateWithOpts plain: %v", err)
	}
	if bytes.Contains(plain, []byte("//go:build")) {
		t.Errorf("plain output must not contain build tag; got:\n%s", plain)
	}
	// Generate() and GenerateWithOpts({}) must produce identical output.
	via := bytes.NewBuffer(nil)
	via2, err := requireddepsgen.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	via.Write(via2)
	if !bytes.Equal(via.Bytes(), plain) {
		t.Errorf("Generate() differs from GenerateWithOpts({}); production parity broken")
	}
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
	if strings.Contains(got, `"github.com/ghbvf/gocell/framework/pkg/errcode"`) {
		t.Errorf("unexpected errcode import in degenerate case; got:\n%s", got)
	}
	if strings.Contains(got, `"github.com/ghbvf/gocell/framework/pkg/validation"`) {
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

// TestGenerate_InjectionAttempt_ReturnsError verifies that malicious gocellKind
// or gocellCode tag values that do not match the errcode identifier whitelist
// are rejected with ErrUnknownTagValue rather than being emitted into the
// generated source.
//
// Note: gocellKind/gocellCode values that contain Go syntax injection payloads
// (e.g. ";", "//", parentheses) would normally be embedded inside struct tag
// double-quote delimiters, which constrains the value space. The whitelist
// regex `^errcode\.(Kind|Err)[A-Z][A-Za-z0-9]*$` rejects anything containing
// these characters at the identifier level, since such characters are never
// valid in a Go exported identifier name.
func TestGenerate_InjectionAttempt_ReturnsError(t *testing.T) {
	// Each case uses a rawTag string that is embedded directly into the struct
	// field definition. The rawTag must be syntactically valid Go struct tag
	// syntax (backtick-delimited, key:"value" pairs) so that parseStructFields
	// can read the tag value; the rejection happens inside resolveTagOrDefault
	// when the value does not match errcodeIdentRE.
	cases := []struct {
		name   string
		rawTag string
	}{
		{
			// "KindInternalBad" does not end on a word boundary matching [A-Za-z0-9]
			// — wait, it does. Use a value with non-identifier chars that survive
			// struct tag parsing: spaces are valid in tag values.
			name:   "space in gocellKind value is not a valid identifier",
			rawTag: `gocell:"required" gocellKind:"KindInternal injected"`,
		},
		{
			name:   "dot in gocellCode value beyond allowed pattern",
			rawTag: `gocell:"required" gocellCode:"ErrCellInvalidConfig.Extra"`,
		},
		{
			name:   "arbitrary non-errcode string in gocellKind",
			rawTag: `gocell:"required" gocellKind:"NotAnErrcode"`,
		},
		{
			name:   "bare lowercase identifier not matching whitelist",
			rawTag: `gocell:"required" gocellKind:"kindInternal"`,
		},
		{
			name:   "underscore in identifier not in allowed char class",
			rawTag: `gocell:"required" gocellCode:"Err_Invalid"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package mypkg\n\ntype Repo interface{ Get() }\n\ntype Service struct {\n" +
				"\trepo Repo `" + tc.rawTag + "`\n}\n"
			dir := writeFixture(t, src)
			_, err := requireddepsgen.Generate(dir)
			if err == nil {
				t.Fatalf("expected error for injection attempt tag=%q, got nil", tc.rawTag)
			}
			if !errors.Is(err, requireddepsgen.ErrUnknownTagValue) {
				t.Errorf("expected ErrUnknownTagValue; got %v", err)
			}
		})
	}
}

// TestGenerate_MalformedTag_ReturnsBadTagSyntax verifies that a struct field
// whose tag is malformed at the struct-tag-convention level (a valid Go
// backtick literal, but not well-formed key:"value" pairs) fails closed with
// ErrBadTagSyntax instead of being silently treated as untagged — which would
// drop a required-dep guard. reflect.StructTag.Get returns "" for these.
func TestGenerate_MalformedTag_ReturnsBadTagSyntax(t *testing.T) {
	cases := []struct {
		name   string
		rawTag string
	}{
		{name: "value not quoted", rawTag: `gocell:required`},
		{name: "unterminated quote", rawTag: `gocell:"required`},
		{name: "missing colon", rawTag: `gocell"required"`},
		{name: "empty key", rawTag: `:"required"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package mypkg\n\ntype Repo interface{ Get() }\n\ntype Service struct {\n" +
				"\trepo Repo `" + tc.rawTag + "`\n}\n"
			dir := writeFixture(t, src)
			_, err := requireddepsgen.Generate(dir)
			if err == nil {
				t.Fatalf("expected error for malformed tag=%q, got nil", tc.rawTag)
			}
			if !errors.Is(err, requireddepsgen.ErrBadTagSyntax) {
				t.Errorf("expected ErrBadTagSyntax; got %v", err)
			}
		})
	}
}

// TestGenerate_KindCodeFamilySwapped_ReturnsError verifies that a code-family
// identifier supplied to gocellKind (or a kind-family identifier to gocellCode)
// is rejected — even though both are well-formed errcode identifiers. The
// per-tag whitelists (errcodeKindRE / errcodeCodeRE) prevent this swap; a single
// shared regex would have accepted both.
func TestGenerate_KindCodeFamilySwapped_ReturnsError(t *testing.T) {
	cases := []struct {
		name   string
		rawTag string
	}{
		{name: "code in gocellKind slot", rawTag: `gocell:"required" gocellKind:"ErrValidationFailed"`},
		{name: "kind in gocellCode slot", rawTag: `gocell:"required" gocellCode:"KindInvalid"`},
		{name: "fully-qualified code in kind slot", rawTag: `gocell:"required" gocellKind:"errcode.ErrValidationFailed"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package mypkg\n\ntype Repo interface{ Get() }\n\ntype Service struct {\n" +
				"\trepo Repo `" + tc.rawTag + "`\n}\n"
			dir := writeFixture(t, src)
			_, err := requireddepsgen.Generate(dir)
			if err == nil {
				t.Fatalf("expected error for swapped family tag=%q, got nil", tc.rawTag)
			}
			if !errors.Is(err, requireddepsgen.ErrUnknownTagValue) {
				t.Errorf("expected ErrUnknownTagValue; got %v", err)
			}
		})
	}
}

// TestGenerate_ErrMsgWithSpecialChars_ProducesValidSource verifies that
// gocellErr values containing characters that would break naive string
// concatenation (e.g. backslash, embedded double-quote) are safely quoted
// via strconv.Quote and produce syntactically valid Go source.
func TestGenerate_ErrMsgWithSpecialChars_ProducesValidSource(t *testing.T) {
	// The gocellErr value contains a double-quote character — naive interpolation
	// via `fmt.Fprintf(... "%s" ...)` would break the generated Go syntax.
	src := "package mypkg\n\ntype Repo interface{ Get() }\n\ntype Service struct {\n" +
		"\trepo Repo `gocell:\"required\" gocellErr:\"dep \\\"Repo\\\" required\"`\n}\n"
	dir := writeFixture(t, src)
	out, err := requireddepsgen.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// format.Source already validates Go syntax, so reaching here means the
	// output compiled. Additionally verify the message appears quoted.
	got := string(out)
	if !strings.Contains(got, `dep \"Repo\" required`) && !strings.Contains(got, `dep "Repo" required`) {
		t.Errorf("expected escaped error message in output; got:\n%s", got)
	}
}
