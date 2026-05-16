package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// stdoutCaptureMu / stderrCaptureMu serialize os.Stdout / os.Stderr
// redirection. captureStdout/captureStderr mutate process-global file
// descriptors; without serialization two t.Parallel() tests racing on the
// same stream clobber each other's pipe (one test's restore reverts the
// fd while another is still printing → empty capture). Independent mutexes
// per stream avoid cross-stream deadlock; same-stream nested capture is not
// a pattern any test uses.
//
// WARNING: nested same-stream capture DEADLOCKS — sync.Mutex is not
// re-entrant. Calling captureStdout inside a captureStdout callback (or
// captureStderr inside captureStderr) will block forever waiting for a lock
// the outer call already holds. Cross-stream nesting (captureStdout inside
// captureStderr or vice-versa) is safe because they use independent mutexes.
var (
	stdoutCaptureMu sync.Mutex
	stderrCaptureMu sync.Mutex
)

func TestPrintUsage(t *testing.T) {
	out := captureStdout(t, PrintUsage)
	for _, want := range []string{
		"generate    Generate assembly code and derived files",
		"assembly --id=<assemblyID>",
		"metrics-schema --id=<assemblyID>",
		"generated [--module=<module>]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("PrintUsage() missing %q in:\n%s", want, out)
		}
	}
}

func TestFindRoot(t *testing.T) {
	// Save and restore working directory.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if chErr := os.Chdir(orig); chErr != nil {
			t.Logf("cleanup: failed to restore working directory: %v", chErr)
		}
	})

	// findRoot should find the go.mod in the project root (walk up from
	// current test dir to find it).
	root, err := findRoot()
	if err != nil {
		t.Fatalf("findRoot() error: %v", err)
	}

	// Verify go.mod exists at the found root.
	gomod := filepath.Join(root, "go.mod")
	if _, statErr := os.Stat(gomod); statErr != nil {
		t.Fatalf("findRoot() returned %q but go.mod not found there: %v", root, statErr)
	}
}

func TestReadModule(t *testing.T) {
	root, err := findRoot()
	if err != nil {
		t.Fatalf("findRoot() error: %v", err)
	}

	mod, err := readModule(root)
	if err != nil {
		t.Fatalf("readModule() error: %v", err)
	}

	if mod != "github.com/ghbvf/gocell" {
		t.Errorf("readModule() = %q, want %q", mod, "github.com/ghbvf/gocell")
	}
}

func TestReadModuleNotFound(t *testing.T) {
	// Use a temp directory without go.mod.
	dir := t.TempDir()
	_, err := readModule(dir)
	if err == nil {
		t.Error("readModule() should return error for directory without go.mod")
	}
}

// TestFormatResults / TestFormatResultsContainsCodeAndMessage previously
// covered the inline formatResults helper. After PR-A10 the rendering moved
// to cmd/gocell/app/printers/TextPrinter; full coverage (including IDE
// click-to-open file:line:col, scope-only, fail-fast, golden snapshots)
// lives in cmd/gocell/app/printers/printer_test.go. The integration tests
// further down in this file (TestRunValidate, TestDispatch_Contract,
// TestDispatch_SuccessPath_ExitZero) exercise the wiring end-to-end.

func TestCommands(t *testing.T) {
	// Verify all expected commands are registered.
	expected := []string{"validate", "scaffold", "generate", "check", "verify"}
	for _, name := range expected {
		if _, ok := commands[name]; !ok {
			t.Errorf("command %q not registered in commands map", name)
		}
	}
}

func TestDispatch_ErrcodeUsesPublicMessage(t *testing.T) {
	const cmdName = "test-errcode-public"
	orig, hadOrig := commands[cmdName]
	commands[cmdName] = func(context.Context, []string) error {
		return errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			"invalid generated metadata",
			errcode.WithInternal("token=hunter2 raw=/private/generated.yaml"),
			errcode.WithDetails(slog.String("field", "cell.id")),
		)
	}
	t.Cleanup(func() {
		if hadOrig {
			commands[cmdName] = orig
			return
		}
		delete(commands, cmdName)
	})

	out := captureStderr(t, func() {
		if code := Dispatch(context.Background(), []string{cmdName}); code != ExitRuntime {
			t.Fatalf("Dispatch exit code = %d, want %d", code, ExitRuntime)
		}
	})
	for _, want := range []string{"ERR_VALIDATION_FAILED", "invalid generated metadata", `field="cell.id"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("Dispatch stderr missing %q in:\n%s", want, out)
		}
	}
	for _, leak := range []string{"hunter2", "/private/generated.yaml"} {
		if strings.Contains(out, leak) {
			t.Fatalf("Dispatch stderr leaked %q in:\n%s", leak, out)
		}
	}
}

func TestDispatch_ErrcodeServerErrorKeepsOperatorRoutingMetadata(t *testing.T) {
	const cmdName = "test-errcode-operator"
	orig, hadOrig := commands[cmdName]
	commands[cmdName] = func(context.Context, []string) error {
		return errcode.New(
			errcode.KindInternal,
			errcode.ErrAuthRoleFetchFailed,
			"role repository failed: postgres://user:secret@example/db",
			errcode.WithInternal("token=hunter2"),
		)
	}
	t.Cleanup(func() {
		if hadOrig {
			commands[cmdName] = orig
			return
		}
		delete(commands, cmdName)
	})

	out := captureStderr(t, func() {
		if code := Dispatch(context.Background(), []string{cmdName}); code != ExitRuntime {
			t.Fatalf("Dispatch exit code = %d, want %d", code, ExitRuntime)
		}
	})
	for _, want := range []string{"ERR_INTERNAL", "internal server error", "status=500", "sourceCode=ERR_AUTH_ROLE_FETCH_FAILED"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Dispatch stderr missing %q in:\n%s", want, out)
		}
	}
	for _, leak := range []string{"postgres://", "hunter2"} {
		if strings.Contains(out, leak) {
			t.Fatalf("Dispatch stderr leaked %q in:\n%s", leak, out)
		}
	}
}

// TestSubcommandHelpFlagsRenderHelp guards against a regression where
// dispatch.go advertises `gocell <command> -h` but the sub-command parses
// args[0] eagerly and reports "unknown … type" instead. Each runner must
// recognize -h / --help / help and render its own help surface.
func TestSubcommandHelpFlagsRenderHelp(t *testing.T) {
	cases := []struct {
		name string
		run  func(context.Context, []string) error
		want []string
	}{
		{"generate", runGenerate, []string{"Usage: gocell generate", "metrics-schema", "owned by gocell"}},
		{"verify", runVerify, []string{"Usage: gocell verify", "generated", "stale, staged-only"}},
		{"scaffold", runScaffold, []string{"Usage: gocell scaffold", "cell", "--dry-run"}},
		{"check", runCheck, []string{"Usage: gocell check", "contract-health", "unconditional-skip"}},
	}

	for _, tc := range cases {
		for _, flag := range []string{"-h", "--help", "help"} {
			t.Run(tc.name+"_"+flag, func(t *testing.T) {
				assertHelpOutput(t, tc.name, tc.run, flag, tc.want)
			})
		}
	}
}

func assertHelpOutput(t *testing.T, name string, run func(context.Context, []string) error, flag string, want []string) {
	t.Helper()
	out := captureStdout(t, func() {
		if err := run(context.Background(), []string{flag}); err != nil {
			t.Fatalf("%s %q: unexpected error: %v", name, flag, err)
		}
	})
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("%s %q help output missing %q in:\n%s", name, flag, w, out)
		}
	}
}

// TestPrintHelpRendersEntryWithoutDescription guards the data-driven help
// renderer's fallback branch: an entry whose desc slice is empty (or nil)
// must still surface its name in the "Types:" listing so a future caller
// that forgets to add the description does not silently drop the type
// from operator-visible output.
func TestPrintHelpRendersEntryWithoutDescription(t *testing.T) {
	out := captureStdout(t, func() {
		printHelp("demo", []helpEntry{
			{name: "with-desc", desc: []string{"has a description"}},
			{name: "no-desc"},
		})
	})
	if !strings.Contains(out, "with-desc") || !strings.Contains(out, "has a description") {
		t.Fatalf("help output missing populated entry rendering:\n%s", out)
	}
	if !strings.Contains(out, "no-desc") {
		t.Fatalf("help output dropped name-only entry; rendering must surface the name even with empty desc:\n%s", out)
	}
}

// --- Command function tests ---

func TestRunValidate(t *testing.T) {
	root, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	// Run validate with explicit root. It may return an error if there are
	// validation errors in the project, but it should not panic.
	// We only assert no panic; validation errors are expected.
	t.Logf("runValidate result: %v", runValidate(context.Background(), []string{"--root", root}))
}

func TestRunValidateNoRoot(t *testing.T) {
	// Running without --root should auto-detect.
	// May succeed or fail with validation errors; just ensure no panic.
	t.Logf("runValidate result: %v", runValidate(context.Background(), []string{}))
}

func TestRunScaffoldNoArgs(t *testing.T) {
	err := runScaffold(context.Background(), []string{})
	if err == nil {
		t.Error("scaffold with no args should return error")
	}
}

func TestRunScaffoldUnknownType(t *testing.T) {
	err := runScaffold(context.Background(), []string{"unknown"})
	if err == nil {
		t.Error("scaffold unknown type should return error")
	}
}

func TestRunScaffoldCellMissingFlags(t *testing.T) {
	err := runScaffold(context.Background(), []string{"cell"})
	if err == nil {
		t.Error("scaffold cell without --id should return error")
	}
}

func TestRunScaffoldCellMissingTeam(t *testing.T) {
	err := runScaffold(context.Background(), []string{"cell", "--id=testcell"})
	if err == nil {
		t.Error("scaffold cell without --team should return error")
	}
}

func TestRunScaffoldSliceMissingFlags(t *testing.T) {
	err := runScaffold(context.Background(), []string{"slice"})
	if err == nil {
		t.Error("scaffold slice without --id should return error")
	}
	err = runScaffold(context.Background(), []string{"slice", "--id=test-slice"})
	if err == nil {
		t.Error("scaffold slice without --cell should return error")
	}
}

func TestRunScaffoldContractMissingFlags(t *testing.T) {
	err := runScaffold(context.Background(), []string{"contract"})
	if err == nil {
		t.Error("scaffold contract without --id should return error")
	}
	err = runScaffold(context.Background(), []string{"contract", "--id=test.contract.v1"})
	if err == nil {
		t.Error("scaffold contract without --kind should return error")
	}
	err = runScaffold(context.Background(), []string{"contract", "--id=test.contract.v1", "--kind=http"})
	if err == nil {
		t.Error("scaffold contract without --owner should return error")
	}
}

func TestRunScaffoldJourneyMissingFlags(t *testing.T) {
	err := runScaffold(context.Background(), []string{"journey"})
	if err == nil {
		t.Error("scaffold journey without --id should return error")
	}
	err = runScaffold(context.Background(), []string{"journey", "--id=test"})
	if err == nil {
		t.Error("scaffold journey without --goal should return error")
	}
	err = runScaffold(context.Background(), []string{"journey", "--id=test", "--goal=test"})
	if err == nil {
		t.Error("scaffold journey without --team should return error")
	}
	err = runScaffold(context.Background(), []string{"journey", "--id=test", "--goal=test", "--team=team"})
	if err == nil {
		t.Error("scaffold journey without --cells should return error")
	}
}

func TestRunGenerateNoArgs(t *testing.T) {
	err := runGenerate(context.Background(), []string{})
	if err == nil {
		t.Error("generate with no args should return error")
	}
}

func TestRunGenerateUnknownType(t *testing.T) {
	err := runGenerate(context.Background(), []string{"unknown"})
	if err == nil {
		t.Error("generate unknown type should return error")
	}
}

// TestRunGenerateIndexesRemoved pins B2-X-05: `generate indexes` is a
// removed V2.1 concept (superseded by `gocell export` / `gocell graph`).
// It must behave exactly like any unregistered type — the unknown-type
// error listing the real registry, with no "not implemented" placeholder
// and no "indexes" entry in the help surface.
func TestRunGenerateIndexesRemoved(t *testing.T) {
	err := runGenerate(context.Background(), []string{"indexes"})
	if err == nil {
		t.Fatal("generate indexes must return the unknown-type error")
	}
	if !strings.Contains(err.Error(), "unknown generate type: indexes") {
		t.Errorf("want unknown-type error, got: %v", err)
	}
	if strings.Contains(err.Error(), "not implemented") {
		t.Errorf("removed type must not report 'not implemented': %v", err)
	}
	help := captureStdout(t, func() {
		_ = runGenerate(context.Background(), []string{"-h"})
	})
	if strings.Contains(help, "indexes") {
		t.Errorf("generate -h must not list removed 'indexes':\n%s", help)
	}
}

func TestRunGenerateAssemblyMissingID(t *testing.T) {
	err := runGenerate(context.Background(), []string{"assembly"})
	if err == nil {
		t.Error("generate assembly without --id should return error")
	}
}

func TestRunGenerateAssemblyBoundaryOnlyRejected(t *testing.T) {
	err := runGenerate(context.Background(), []string{"assembly", "--id=corebundle", "--boundary-only"})
	if err == nil {
		t.Error("generate assembly --boundary-only should be rejected")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined: -boundary-only") {
		t.Fatalf("generate assembly --boundary-only error = %v", err)
	}
}

func TestRunGenerateMetricsSchemaMissingID(t *testing.T) {
	err := runGenerate(context.Background(), []string{"metrics-schema"})
	if err == nil {
		t.Error("generate metrics-schema without --id should return error")
	}
}

func TestWriteGeneratedFileRejectsHandwrittenExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cmd", "fixture", "main.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := writeGeneratedFile(root, path, []byte("// Code generated by gocell generate assembly. DO NOT EDIT.\n"), "fixture")
	if err == nil {
		t.Fatal("writeGeneratedFile should reject overwriting a handwritten file")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite non-generated file") {
		t.Fatalf("writeGeneratedFile error = %v", err)
	}
}

func TestWriteGeneratedFileAllowsGeneratedExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cmd", "fixture", "main.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("// Code generated by gocell generate assembly. DO NOT EDIT.\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := writeGeneratedFile(root, path, []byte("// Code generated by gocell generate assembly. DO NOT EDIT.\npackage main\n"), "fixture")
	if err != nil {
		t.Fatalf("writeGeneratedFile should allow generated overwrite: %v", err)
	}
}

func TestRunCheckNoArgs(t *testing.T) {
	err := runCheck(context.Background(), []string{})
	if err == nil {
		t.Error("check with no args should return error")
	}
}

func TestRunCheckUnknownType(t *testing.T) {
	err := runCheck(context.Background(), []string{"unknown"})
	if err == nil {
		t.Error("check unknown type should return error")
	}
}

func TestRunCheckContractHealth(t *testing.T) {
	err := runCheck(context.Background(), []string{"contract-health"})
	// Should succeed (may find 0 or N contracts).
	if err != nil {
		t.Errorf("check contract-health should succeed, got: %v", err)
	}
}

func TestRunCheckUnconditionalSkip(t *testing.T) {
	// PR-CFG-D wired the real analyzer; the repo is clean so it must succeed.
	err := runCheck(context.Background(), []string{"unconditional-skip"})
	if err != nil {
		t.Errorf("check unconditional-skip should succeed on clean repo, got: %v", err)
	}
}

func TestRunVerifyNoArgs(t *testing.T) {
	err := runVerify(context.Background(), []string{})
	if err == nil {
		t.Error("verify with no args should return error")
	}
}

func TestRunVerifyUnknownType(t *testing.T) {
	err := runVerify(context.Background(), []string{"unknown"})
	if err == nil {
		t.Error("verify unknown type should return error")
	}
}

func TestRunVerifySliceMissingID(t *testing.T) {
	err := runVerify(context.Background(), []string{"slice"})
	if err == nil {
		t.Error("verify slice without --id should return error")
	}
}

func TestRunVerifyCellMissingID(t *testing.T) {
	err := runVerify(context.Background(), []string{"cell"})
	if err == nil {
		t.Error("verify cell without --id should return error")
	}
}

func TestRunVerifyJourneyMissingID(t *testing.T) {
	err := runVerify(context.Background(), []string{"journey"})
	if err == nil {
		t.Error("verify journey without --id should return error")
	}
}

func TestRunVerifyTargetsMissingFiles(t *testing.T) {
	err := runVerify(context.Background(), []string{"targets"})
	if err == nil {
		t.Error("verify targets without --files should return error")
	}
}

func TestRunVerifyTargets(t *testing.T) {
	// Provide a file path; the result depends on project metadata.
	err := runVerify(context.Background(), []string{"targets", "--files=cells/accesscore/cell.yaml"})
	if err != nil {
		t.Errorf("verify targets should succeed, got: %v", err)
	}
}

func TestRunVerifySliceNotFound(t *testing.T) {
	err := runVerify(context.Background(), []string{"slice", "--id=nonexistent/slice"})
	if err == nil {
		t.Error("verify slice with nonexistent ID should return error")
	}
}

func TestRunVerifyCellNotFound(t *testing.T) {
	err := runVerify(context.Background(), []string{"cell", "--id=nonexistent"})
	if err == nil {
		t.Error("verify cell with nonexistent ID should return error")
	}
}

func TestRunVerifyJourneyNotFound(t *testing.T) {
	err := runVerify(context.Background(), []string{"journey", "--id=nonexistent"})
	if err == nil {
		t.Error("verify journey with nonexistent ID should return error")
	}
}

func TestPrintTargetList(t *testing.T) {
	// Should not panic with empty or non-empty lists.
	printTargetList("Test", nil)
	printTargetList("Test", []string{})
	printTargetList("Test", []string{"a", "b"})
}

// TestIsWithinRoot / TestEvalExistingPrefix previously lived here as a copy
// of kernel/governance's tests. Now that cmd/gocell/app delegates to the
// exported governance.IsWithinRoot / EvalExistingPrefix, coverage lives in
// kernel/governance/validate_test.go — no duplication here.

// TestPrintResult and the file:line:col / scope rendering tests previously
// lived here as direct callers of printResult. They moved to
// cmd/gocell/app/printers/printer_test.go (TestGolden_Text + TextPrinter
// unit tests) when the renderer migrated to the printers package.

// captureStdout runs fn with os.Stdout redirected into a string.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	stdoutCaptureMu.Lock()
	defer stdoutCaptureMu.Unlock()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	<-done
	return buf.String()
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	stderrCaptureMu.Lock()
	defer stderrCaptureMu.Unlock()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	<-done
	return buf.String()
}
