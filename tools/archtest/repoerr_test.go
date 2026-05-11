// invariants:
//   - INVARIANT: CTXCANCEL-LOCAL-IMPL-BAN-01
//   - INVARIANT: REPO-LOG-KEY-ID-REDACT-01

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	ruleCtxCancelLocalImplBan = "CTXCANCEL-LOCAL-IMPL-BAN-01"
	ruleRepoLogKeyIDRedact    = "REPO-LOG-KEY-ID-REDACT-01"
)

// bannedTypeNameRegex matches names that look like a re-implementation of the
// canonical ctxcancel error type. Pattern intent: optional "local" or "Local"
// prefix, then "ctx" or "context" stem, then a Cancel/Canceled/Cancellation
// root, then "Error" suffix. This is intentionally broad — any new local
// cancel-error type variant should be caught and either renamed or replaced
// with the canonical helper (pkg/ctxcancel.Wrap).
var bannedTypeNameRegex = regexp.MustCompile(`^(?:[Ll]ocal)?[Cc](?:tx|ontext)[Cc]ancel(?:ed|lation)?Error$`)

// bannedLogAttrLiterals enumerates the cryptographic-identifier names that
// must never appear as the *key* slot of a slog attribute anywhere under
// cells/. Key IDs belong on Prometheus metric labels (low-cardinality,
// controlled fan-out), not on the log plane.
//
// The rule scans only key slots — string literals appearing as attribute
// *values* (e.g. `slog.String("description", "stored_key_id")` where the user
// happens to log a banned word as a value) are intentionally not flagged.
var bannedLogAttrLiterals = map[string]struct{}{
	"key_id":         {},
	"keyID":          {},
	"key-id":         {},
	"stored_key_id":  {},
	"current_key_id": {},
}

// slogMethodKeyStart maps every recognized slog level method name to the
// argument index at which keyed attribute pairs begin. Exact-match (not
// prefix) avoids false positives on names like `ErrorList` / `WarnCounter` /
// `InfoBox` that start with a level keyword but are not log emitters.
//
//   - plain (Warn / Error / Info / Debug + f/ln variants): keys start at Args[1]
//     because Args[0] is the message string.
//   - context-aware (WarnContext / ErrorContext / InfoContext / DebugContext):
//     keys start at Args[2] because Args[0]=ctx, Args[1]=msg.
//   - dynamic level (Log, LogAttrs): keys start at Args[3] because
//     Args[0]=ctx, Args[1]=level, Args[2]=msg. Log accepts untyped key/value
//     pairs; LogAttrs takes typed Attr values — both flow through the same
//     key-slot scan logic below.
var slogMethodKeyStart = map[string]int{
	"Warn": 1, "Warnf": 1, "Warnln": 1, "WarnContext": 2,
	"Error": 1, "Errorf": 1, "Errorln": 1, "ErrorContext": 2,
	"Info": 1, "Infof": 1, "Infoln": 1, "InfoContext": 2,
	"Debug": 1, "Debugf": 1, "Debugln": 1, "DebugContext": 2,
	"Log": 3, "LogAttrs": 3,
}

// slogAttrCtors enumerates the slog package-level constructors that produce
// a typed Attr (or Group). Each takes the key as Args[0]; the rest carry the
// value(s). The set is closed against std slog as of Go 1.22.
var slogAttrCtors = map[string]struct{}{
	"String": {}, "Any": {}, "Bool": {}, "Int": {}, "Int64": {}, "Uint64": {},
	"Float64": {}, "Time": {}, "Duration": {}, "Group": {}, "Attr": {},
}

type repoErrViolation struct {
	Rule    string
	File    string
	Line    int
	Message string
}

func (v repoErrViolation) String() string {
	return fmt.Sprintf("%s: %s:%d: %s", v.Rule, v.File, v.Line, v.Message)
}

// =====================================================================
// Rule 1: CTXCANCEL-LOCAL-IMPL-BAN-01
// =====================================================================
//
// Scope: cells/*/internal/adapters/**/*.go production files (excludes
// _test.go). Rejects three patterns that all reinvent pkg/ctxcancel:
//
//   A. Local ctx-cancel error type (e.g. ctxCanceledError, CtxCancelError,
//      localCtxCanceledError) — ctxcancel.Wrap returns a *errcode.Error
//      already carrying the canonical Code/Message/Details.
//   B. Receiver method named with prefix "wrapCtx" — a thin wrapper that
//      only forwards to ctxcancel.Wrap. The forwarder is dead weight;
//      callsites should call ctxcancel.Wrap directly (see audit_repo.go).
//   C. errors.Is(err, context.Canceled|DeadlineExceeded) literal call —
//      ctxcancel.Wrap detects internally and returns nil for non-cancel
//      errors, so callers must not pre-check.
//
// ref: pkg/ctxcancel.Wrap (canonical helper)
// ref: cells/configcore/internal/adapters/postgres/audit_repo.go (range usage)

func TestCtxCancelLocalImplBan(t *testing.T) {
	root := findModuleRoot(t)
	files, err := findCellAdapterProductionGoFiles(root)
	require.NoError(t, err, "failed to enumerate cell adapter Go files")
	require.NotEmpty(t, files, "no cells/*/internal/adapters/**/*.go files found — module root may be wrong")

	var violations []repoErrViolation
	for _, file := range files {
		v, err := scanCtxCancelLocalImpl(file)
		require.NoErrorf(t, err, "scan %s", file)
		violations = append(violations, v...)
	}

	if len(violations) > 0 {
		t.Logf("Found %d %s violation(s):", len(violations), ruleCtxCancelLocalImplBan)
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}
	assert.Empty(t, violations,
		"cells/*/internal/adapters/**/*.go must not declare a local "+
			"ctx-cancel type, a wrapCtx* receiver method, or a bare "+
			"errors.Is(err, context.Canceled|DeadlineExceeded) call. "+
			"Use pkg/ctxcancel.Wrap(err, op, identifier) directly.")
}

func scanCtxCancelLocalImpl(path string) ([]repoErrViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return scanCtxCancelLocalImplAST(fset, file, path), nil
}

func scanCtxCancelLocalImplAST(fset *token.FileSet, file *ast.File, path string) []repoErrViolation {
	ctxAliases := contextImportAliases(file)
	var out []repoErrViolation

	scanner.EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if bannedTypeNameRegex.MatchString(ts.Name.Name) {
			out = append(out, repoErrViolation{
				Rule:    ruleCtxCancelLocalImplBan,
				File:    path,
				Line:    fset.Position(ts.Pos()).Line,
				Message: fmt.Sprintf("local ctx-cancel type %q (use pkg/ctxcancel.Wrap)", ts.Name.Name),
			})
		}
	})
	scanner.EachInSubtree[ast.FuncDecl](file, func(d *ast.FuncDecl) {
		if isThinCtxCancelWrapper(d) {
			out = append(out, repoErrViolation{
				Rule:    ruleCtxCancelLocalImplBan,
				File:    path,
				Line:    fset.Position(d.Pos()).Line,
				Message: fmt.Sprintf("thin ctxcancel.Wrap forwarder %q (inline ctxcancel.Wrap at the callsite)", d.Name.Name),
			})
		}
		if d.Body != nil {
			scanner.EachInSubtree[ast.CallExpr](d.Body, func(call *ast.CallExpr) {
				if !isErrorsIsCall(call) || len(call.Args) < 2 {
					return
				}
				if !isContextCancelSelector(call.Args[1], ctxAliases) {
					return
				}
				out = append(out, repoErrViolation{
					Rule:    ruleCtxCancelLocalImplBan,
					File:    path,
					Line:    fset.Position(call.Pos()).Line,
					Message: "errors.Is(err, context.Canceled|DeadlineExceeded) — call pkg/ctxcancel.Wrap (it detects internally)",
				})
			})
		}
	})
	return out
}

// isThinCtxCancelWrapper reports whether fn is a receiver method whose entire
// body is a single `return ctxcancel.Wrap(...)` statement. Such forwarders are
// dead weight — callsites should call ctxcancel.Wrap directly. Helpers that
// combine ctx-cancel detection with additional non-trivial logic (e.g. mapping
// to a domain-specific errcode envelope) have body length > 1 and pass.
//
// Targets *receiver methods* only: free functions cannot become indirect
// forwarders for the same per-repo callsite (they are inherently package-wide
// API surface), so the rule does not extend there.
func isThinCtxCancelWrapper(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || fn.Body == nil {
		return false
	}
	if len(fn.Body.List) != 1 {
		return false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "ctxcancel" && sel.Sel.Name == "Wrap"
}

// contextImportAliases returns the identifier names through which the
// "context" stdlib package is referenced in file. Default = {"context"};
// an explicit alias `import c "context"` adds {"c"}; a dot-import
// `import . "context"` returns {} (selector-based detection cannot match
// dot-imported symbols, so the rule fails open on that — dot-imports of
// stdlib are rare and warned by other linters).
func contextImportAliases(file *ast.File) map[string]struct{} {
	out := map[string]struct{}{}
	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "context" {
			continue
		}
		switch {
		case imp.Name == nil:
			out["context"] = struct{}{}
		case imp.Name.Name == ".":
			// Dot-import: out of scope (see doc comment).
		case imp.Name.Name == "_":
			// Blank import: package not referenced under any name.
		default:
			out[imp.Name.Name] = struct{}{}
		}
	}
	return out
}

func isErrorsIsCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "errors" && sel.Sel.Name == "Is"
}

func isContextCancelSelector(expr ast.Expr, ctxAliases map[string]struct{}) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if _, ok := ctxAliases[pkg.Name]; !ok {
		return false
	}
	return sel.Sel.Name == "Canceled" || sel.Sel.Name == "DeadlineExceeded"
}

// findCellAdapterProductionGoFiles narrows findCellProductionGoFiles
// (defined in outbox_topic_test.go) to the adapter sub-tree only, since
// the ctx-cancel canonical helper rule applies to repository code that
// translates IO errors — not to slice handlers / domain logic.
func findCellAdapterProductionGoFiles(root string) ([]string, error) {
	all, err := findCellProductionGoFiles(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range all {
		// filepath.ToSlash converts path separators to "/" on Windows so the
		// substring match works consistently across platforms (filepath.WalkDir
		// returns native separators on each OS).
		if strings.Contains(filepath.ToSlash(f), "/internal/adapters/") {
			out = append(out, f)
		}
	}
	return out, nil
}

// =====================================================================
// Rule 1 inline regression fixtures
// =====================================================================
//
// Proves the rule retains teeth — a silent zero-violation run on the
// repo would pass too without exercising the negative path.

func TestCtxCancelLocalImplBan_Fixtures(t *testing.T) {
	fixtures := map[string]struct {
		src       string
		wantMatch bool
	}{
		"detects_local_ctx_canceled_type": {
			src: `package fixture
type ctxCanceledError struct{ msg string }
func (e *ctxCanceledError) Error() string { return e.msg }
`,
			wantMatch: true,
		},
		"detects_local_ctx_cancel_type_camel": {
			src: `package fixture
type CtxCancelError struct{}
func (e *CtxCancelError) Error() string { return "" }
`,
			wantMatch: true,
		},
		"detects_local_ctx_canceled_type_local_prefix": {
			src: `package fixture
type localCtxCanceledError struct{}
func (e *localCtxCanceledError) Error() string { return "" }
`,
			wantMatch: true,
		},
		// Rule 1.B is now semantic: a receiver method whose body is exactly
		// `return ctxcancel.Wrap(...)` is a thin forwarder, regardless of name.
		"detects_thin_wrapper_named_wrapCtxCancel": {
			src: `package fixture
type Repo struct{}
func (r *Repo) wrapCtxCancel(err error, op, id string) error {
	return ctxcancel.Wrap(err, op, id)
}
`,
			wantMatch: true,
		},
		"detects_thin_wrapper_with_unrelated_name": {
			src: `package fixture
type Repo struct{}
// Method name does NOT start with wrapCtx — the prefix is irrelevant under
// semantic detection. The body alone (single return ctxcancel.Wrap) makes it
// a thin forwarder.
func (r *Repo) forward(err error, op string) error {
	return ctxcancel.Wrap(err, op, "")
}
`,
			wantMatch: true,
		},
		"passes_helper_with_fallback_construction": {
			src: `package fixture
type Repo struct{}
type ErrEnvelope struct{ Cause error }
func (e *ErrEnvelope) Error() string { return "" }
// Bundles ctx-cancel detection with additional non-trivial logic — body
// has multiple statements, NOT a thin forwarder.
func (r *Repo) wrapNonScanQueryErr(err error, op string) error {
	if cancelErr := ctxcancel.Wrap(err, op, ""); cancelErr != nil {
		return cancelErr
	}
	return &ErrEnvelope{Cause: err}
}
`,
			wantMatch: false,
		},
		"passes_method_returning_err_directly": {
			src: `package fixture
type Repo struct{}
// Single return stmt but the call is not ctxcancel.Wrap.
func (r *Repo) wrapCtxCancel(err error) error { return err }
`,
			wantMatch: false,
		},
		"detects_errors_is_context_canceled": {
			src: `package fixture
import (
	"context"
	"errors"
)
func Foo(err error) bool {
	return errors.Is(err, context.Canceled)
}
`,
			wantMatch: true,
		},
		"detects_errors_is_deadline_exceeded": {
			src: `package fixture
import (
	"context"
	"errors"
)
func Foo(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}
`,
			wantMatch: true,
		},
		// Import-alias bypass: `import c "context"` then errors.Is(err, c.Canceled)
		// must still trigger Rule 1.C — the rule resolves any alias the file has
		// imported the context package under.
		"detects_errors_is_context_alias_canceled": {
			src: `package fixture
import (
	c "context"
	"errors"
)
func Foo(err error) bool {
	return errors.Is(err, c.Canceled)
}
`,
			wantMatch: true,
		},
		"detects_errors_is_context_alias_deadline": {
			src: `package fixture
import (
	ctx2 "context"
	"errors"
)
func Foo(err error) bool {
	return errors.Is(err, ctx2.DeadlineExceeded)
}
`,
			wantMatch: true,
		},
		"passes_canonical_no_local_impl": {
			src: `package fixture
type errSentinel struct{ s string }
func (e *errSentinel) Error() string { return e.s }
func Wrap() error { return &errSentinel{s: "wrapped"} }
`,
			wantMatch: false,
		},
		"passes_unrelated_errors_is": {
			src: `package fixture
import (
	"errors"
	"io"
)
func Foo(err error) bool {
	return errors.Is(err, io.EOF)
}
`,
			wantMatch: false,
		},
		"passes_unrelated_struct_type": {
			src: `package fixture
type ContextValue struct{ V string }
type ErrorPayload struct{ Cause error }
`,
			wantMatch: false,
		},
		// New variants added to widen bannedTypeNameRegex coverage.
		"detects_context_cancel_error": {
			src: `package fixture
type contextCancelError struct{}
func (e *contextCancelError) Error() string { return "" }
`,
			wantMatch: true,
		},
		"detects_ctx_cancellation_error": {
			src: `package fixture
type ctxCancellationError struct{}
func (e *ctxCancellationError) Error() string { return "" }
`,
			wantMatch: true,
		},
		"passes_unrelated_error_types": {
			src: `package fixture
type ConfigError struct{ msg string }
func (e *ConfigError) Error() string { return e.msg }
type DatabaseError struct{ cause error }
func (e *DatabaseError) Error() string { return e.cause.Error() }
`,
			wantMatch: false,
		},
	}

	for name, tc := range fixtures {
		t.Run(name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, name+".go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err)
			v := scanCtxCancelLocalImplAST(fset, f, name+".go")
			if tc.wantMatch {
				assert.NotEmpty(t, v, "fixture %q should trigger rule, got %v", name, v)
			} else {
				assert.Empty(t, v, "fixture %q should not trigger rule, got %v", name, v)
			}
		})
	}
}

// =====================================================================
// Rule 2: REPO-LOG-KEY-ID-REDACT-01
// =====================================================================
//
// Scope: cells/**/*.go production files (excludes _test.go). slog
// {Warn,Error,Info,Debug}* call args must not name "key_id" / "keyID" /
// "key-id" / "stored_key_id" / "current_key_id" as string literals.
//
// Method names prefix-match {Warn, Error, Info, Debug} — covers Warn,
// Warnf, WarnContext, Warnln on both slog package calls and any
// *.logger.WarnContext receiver methods. Args[0] (the msg) is skipped;
// any *ast.BasicLit STRING in Args[1..] is checked against the banlist.
//
// ref: OpenTelemetry semantic conventions — sensitive attribute redaction.

func TestRepoLogKeyIDRedact(t *testing.T) {
	root := findModuleRoot(t)
	files, err := findCellProductionGoFiles(root)
	require.NoError(t, err, "failed to enumerate cells production Go files")
	require.NotEmpty(t, files, "no cells/**/*.go files found — module root may be wrong")

	var violations []repoErrViolation
	for _, file := range files {
		v, err := scanRepoLogKeyIDRedact(file)
		require.NoErrorf(t, err, "scan %s", file)
		violations = append(violations, v...)
	}

	if len(violations) > 0 {
		t.Logf("Found %d %s violation(s):", len(violations), ruleRepoLogKeyIDRedact)
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}
	assert.Empty(t, violations,
		"cells/**/*.go slog.{Warn,Error,Info,Debug}* attrs must not name "+
			"key_id / keyID / key-id / stored_key_id / current_key_id; "+
			"key IDs belong on metric labels, not the log plane.")
}

func scanRepoLogKeyIDRedact(path string) ([]repoErrViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return scanRepoLogKeyIDRedactAST(fset, file, path), nil
}

func scanRepoLogKeyIDRedactAST(fset *token.FileSet, file *ast.File, path string) []repoErrViolation {
	consts := buildStringConstResolver(file)
	var out []repoErrViolation
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		keyStart, ok := logCallKeyStart(call)
		if !ok {
			return
		}
		out = append(out, scanLogAttrKeys(fset, path, call, keyStart, consts)...)
	})
	return out
}

// scanLogAttrKeys inspects the key slots of a slog level call, walking the
// arg list from keyStart and treating each item as either:
//
//   - a typed Attr constructor `slog.X(key, ...)` — only Args[0] (the key)
//     is checked; the value position is left alone (a banned literal in the
//     value slot is not a log-plane leak).
//   - an untyped key/value pair from an `args ...any` log signature — the
//     current item is the key, the next item is the value, advance by 2.
//
// Keys that resolve via a same-file string `const` declaration (one-hop
// alias folding) are checked as if the literal had been written inline.
func scanLogAttrKeys(fset *token.FileSet, path string, call *ast.CallExpr, keyStart int, consts stringConstResolver) []repoErrViolation {
	var out []repoErrViolation
	args := call.Args
	for i := keyStart; i < len(args); {
		arg := args[i]
		if inner, ok := arg.(*ast.CallExpr); ok && isSlogAttrCtor(inner) {
			if len(inner.Args) >= 1 {
				if hit := checkBannedKey(fset, path, inner.Args[0], consts); hit != nil {
					out = append(out, *hit)
				}
			}
			i++
			continue
		}
		// Untyped pair: this arg is the key, args[i+1] is the value.
		if hit := checkBannedKey(fset, path, arg, consts); hit != nil {
			out = append(out, *hit)
		}
		i += 2
	}
	return out
}

// logCallKeyStart returns the index at which keyed attributes begin for a
// recognized slog level call, or 0 / false when call is not a log emitter.
func logCallKeyStart(call *ast.CallExpr) (int, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}
	idx, ok := slogMethodKeyStart[sel.Sel.Name]
	return idx, ok
}

// isSlogAttrCtor reports whether call is a typed slog attribute constructor
// such as slog.String("k", v) / slog.Any("k", v) / slog.Group("k", attrs...).
// Constrained to the slog package selector so unrelated identically-named
// helpers in user code do not opt into the key-slot scan.
func isSlogAttrCtor(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "slog" {
		return false
	}
	_, ok = slogAttrCtors[sel.Sel.Name]
	return ok
}

// checkBannedKey resolves expr to a string (literal or one-hop string const)
// and returns a violation when the result is in bannedLogAttrLiterals.
func checkBannedKey(fset *token.FileSet, path string, expr ast.Expr, consts stringConstResolver) *repoErrViolation {
	var (
		key string
		pos token.Pos
	)
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return nil
		}
		s, err := strconv.Unquote(e.Value)
		if err != nil {
			return nil
		}
		key, pos = s, e.Pos()
	case *ast.Ident:
		s, ok := consts[e.Name]
		if !ok {
			return nil
		}
		key, pos = s, e.Pos()
	default:
		return nil
	}
	if _, banned := bannedLogAttrLiterals[key]; !banned {
		return nil
	}
	return &repoErrViolation{
		Rule:    ruleRepoLogKeyIDRedact,
		File:    path,
		Line:    fset.Position(pos).Line,
		Message: fmt.Sprintf("banned log attr key %q (key IDs belong on metric labels)", key),
	}
}

// stringConstResolver maps a same-file const identifier to its string value
// for one-hop alias folding. Cross-package and cross-file resolution is
// intentionally out of scope: the goal is to close the trivial bypass
// `const k = "stored_key_id"` without paying the cost of full SSA-grade
// constant propagation.
type stringConstResolver map[string]string

func buildStringConstResolver(file *ast.File) stringConstResolver {
	m := stringConstResolver{}
	scanner.EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
		if gd.Tok != token.CONST {
			return
		}
		scanner.EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if s, err := strconv.Unquote(lit.Value); err == nil {
					m[name.Name] = s
				}
			}
		})
	})
	return m
}

// =====================================================================
// Rule 2 inline regression fixtures
// =====================================================================

func TestRepoLogKeyIDRedact_Fixtures(t *testing.T) {
	fixtures := map[string]struct {
		src       string
		wantMatch bool
	}{
		"detects_stored_key_id_in_warn": {
			src: `package fixture
import "log/slog"
func emit(logger *slog.Logger, x string) {
	logger.Warn("msg", slog.String("stored_key_id", x))
}
`,
			wantMatch: true,
		},
		"detects_keyID_in_error": {
			src: `package fixture
import "log/slog"
func emit(logger *slog.Logger, x string) {
	logger.Error("msg", slog.String("keyID", x))
}
`,
			wantMatch: true,
		},
		"detects_key_id_in_warncontext": {
			src: `package fixture
import (
	"context"
	"log/slog"
)
func emit(ctx context.Context, logger *slog.Logger, x string) {
	logger.WarnContext(ctx, "msg", slog.String("key_id", x))
}
`,
			wantMatch: true,
		},
		"detects_current_key_id_in_info": {
			src: `package fixture
import "log/slog"
func emit(logger *slog.Logger, x string) {
	logger.Info("msg", slog.String("current_key_id", x))
}
`,
			wantMatch: true,
		},
		"detects_slog_pkg_warn": {
			src: `package fixture
import "log/slog"
func emit(x string) {
	slog.Warn("msg", slog.String("stored_key_id", x))
}
`,
			wantMatch: true,
		},
		"passes_business_key_only": {
			src: `package fixture
import "log/slog"
func emit(logger *slog.Logger, k string) {
	logger.Warn("msg", slog.String("key", k))
}
`,
			wantMatch: false,
		},
		"passes_unrelated_call_with_banned_string": {
			src: `package fixture
func mkLabel() string {
	return "stored_key_id"
}
`,
			wantMatch: false,
		},
		"passes_msg_arg_unchecked": {
			src: `package fixture
import "log/slog"
func emit(logger *slog.Logger) {
	logger.Warn("stored_key_id")
}
`,
			wantMatch: false,
		},
		// Banned word in attribute *value* slot is allowed — only the key is
		// scanned. A user logging an error description that happens to contain
		// "stored_key_id" must not trip the rule.
		"passes_banned_in_value_position": {
			src: `package fixture
import "log/slog"
func emit(logger *slog.Logger) {
	logger.Warn("msg", slog.String("description", "stored_key_id"))
}
`,
			wantMatch: false,
		},
		// Const-alias bypass: declaring the banned key as a same-file string
		// const must still be detected via one-hop folding.
		"detects_const_alias_key": {
			src: `package fixture
import "log/slog"
const auditKey = "stored_key_id"
func emit(logger *slog.Logger, x string) {
	logger.Warn("msg", slog.String(auditKey, x))
}
`,
			wantMatch: true,
		},
		// slog.LogAttrs adds typed Attrs starting at Args[3] (ctx, level, msg).
		"detects_logattrs_with_banned_key": {
			src: `package fixture
import (
	"context"
	"log/slog"
)
func emit(ctx context.Context, logger *slog.Logger, x string) {
	logger.LogAttrs(ctx, slog.LevelWarn, "msg", slog.String("stored_key_id", x))
}
`,
			wantMatch: true,
		},
		// slog.Log accepts untyped key/value pairs starting at Args[3].
		"detects_log_method_untyped_pair": {
			src: `package fixture
import (
	"context"
	"log/slog"
)
func emit(ctx context.Context, logger *slog.Logger, x string) {
	logger.Log(ctx, slog.LevelWarn, "msg", "stored_key_id", x)
}
`,
			wantMatch: true,
		},
		// Negative fixtures validating that logCallKeyStart uses exact-match
		// against slogMethodKeyStart: methods starting with a level keyword but
		// not in the closed set must NOT be flagged.
		"passes_errorlist_call_not_a_log_method": {
			src: `package fixture
type Errs interface{ ErrorList(label string) }
func use(e Errs) {
	e.ErrorList("stored_key_id")
}
`,
			wantMatch: false,
		},
		"passes_warncounter_call_not_a_log_method": {
			src: `package fixture
type Metrics interface{ WarnCounter(key string) }
func use(m Metrics) {
	m.WarnCounter("stored_key_id")
}
`,
			wantMatch: false,
		},
		"passes_infobox_call_not_a_log_method": {
			src: `package fixture
type UI interface{ InfoBox(msg string) }
func use(u UI) {
	u.InfoBox("key_id")
}
`,
			wantMatch: false,
		},
	}

	for name, tc := range fixtures {
		t.Run(name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, name+".go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err)
			v := scanRepoLogKeyIDRedactAST(fset, f, name+".go")
			if tc.wantMatch {
				assert.NotEmpty(t, v, "fixture %q should trigger rule, got %v", name, v)
			} else {
				assert.Empty(t, v, "fixture %q should not trigger rule, got %v", name, v)
			}
		})
	}
}
