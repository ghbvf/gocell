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
)

const (
	ruleCtxCancelLocalImplBan = "CTXCANCEL-LOCAL-IMPL-BAN-01"
	ruleRepoLogKeyIDRedact    = "REPO-LOG-KEY-ID-REDACT-01"

	// bannedWrapMethodPrefix targets receiver methods that forward to
	// ctxcancel.Wrap behind an extra layer of indirection. The canonical
	// pattern across cells/*/internal/adapters is to call ctxcancel.Wrap
	// directly at the IO boundary; any "wrapCtx*" receiver method is dead
	// weight by definition.
	bannedWrapMethodPrefix = "wrapCtx"
)

// bannedTypeNameRegex matches names that look like a re-implementation of the
// canonical ctxcancel error type. Pattern intent: optional "local" or "Local"
// prefix, then "ctx" or "context" stem, then a Cancel/Canceled/Cancellation
// root, then "Error" suffix. This is intentionally broad — any new local
// cancel-error type variant should be caught and either renamed or replaced
// with the canonical helper (pkg/ctxcancel.Wrap).
var bannedTypeNameRegex = regexp.MustCompile(`^(?:[Ll]ocal)?[Cc](?:tx|ontext)[Cc]ancel(?:ed|lation)?Error$`)

// bannedLogAttrLiterals enumerates the cryptographic-identifier names
// that must never appear as a string literal in a log call's attribute
// list anywhere under cells/. Key IDs belong on Prometheus metric labels
// (low-cardinality, controlled fan-out), not on the log plane.
var bannedLogAttrLiterals = map[string]struct{}{
	"key_id":         {},
	"keyID":          {},
	"key-id":         {},
	"stored_key_id":  {},
	"current_key_id": {},
}

// slogLevelMethods is the closed set of slog level method names recognised by
// this rule. Using exact-match rather than prefix matching avoids false
// positives on names that happen to start with a level keyword but are not
// log emitters (e.g. ErrorList, WarnCounter, InfoBox).
//
// The set covers the standard slog API surface:
//   - plain:    Warn / Error / Info / Debug
//   - formatted: Warnf / Errorf / Infof / Debugf  (popular wrapper conventions)
//   - line:     Warnln / Errorln / Infoln / Debugln (popular wrapper conventions)
//   - context:  WarnContext / ErrorContext / InfoContext / DebugContext
var slogLevelMethods = map[string]struct{}{
	"Warn": {}, "Warnf": {}, "Warnln": {}, "WarnContext": {},
	"Error": {}, "Errorf": {}, "Errorln": {}, "ErrorContext": {},
	"Info": {}, "Infof": {}, "Infoln": {}, "InfoContext": {},
	"Debug": {}, "Debugf": {}, "Debugln": {}, "DebugContext": {},
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
		return nil, fmt.Errorf("parse: %w", err)
	}
	return scanCtxCancelLocalImplAST(fset, file, path), nil
}

func scanCtxCancelLocalImplAST(fset *token.FileSet, file *ast.File, path string) []repoErrViolation {
	var out []repoErrViolation

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if bannedTypeNameRegex.MatchString(ts.Name.Name) {
					out = append(out, repoErrViolation{
						Rule:    ruleCtxCancelLocalImplBan,
						File:    path,
						Line:    fset.Position(ts.Pos()).Line,
						Message: fmt.Sprintf("local ctx-cancel type %q (use pkg/ctxcancel.Wrap)", ts.Name.Name),
					})
				}
			}
		case *ast.FuncDecl:
			if d.Recv != nil && strings.HasPrefix(d.Name.Name, bannedWrapMethodPrefix) {
				out = append(out, repoErrViolation{
					Rule:    ruleCtxCancelLocalImplBan,
					File:    path,
					Line:    fset.Position(d.Pos()).Line,
					Message: fmt.Sprintf("local wrap method %q (call pkg/ctxcancel.Wrap directly)", d.Name.Name),
				})
			}
			if d.Body != nil {
				ast.Inspect(d.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if !isErrorsIsCall(call) || len(call.Args) < 2 {
						return true
					}
					if !isContextCancelSelector(call.Args[1]) {
						return true
					}
					out = append(out, repoErrViolation{
						Rule:    ruleCtxCancelLocalImplBan,
						File:    path,
						Line:    fset.Position(call.Pos()).Line,
						Message: "errors.Is(err, context.Canceled|DeadlineExceeded) — call pkg/ctxcancel.Wrap (it detects internally)",
					})
					return true
				})
			}
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

func isContextCancelSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if pkg.Name != "context" {
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
		"detects_wrap_method": {
			src: `package fixture
type Repo struct{}
func (r *Repo) wrapCtxCancel(err error) error { return err }
`,
			wantMatch: true,
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
		tc := tc
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
		return nil, fmt.Errorf("parse: %w", err)
	}
	return scanRepoLogKeyIDRedactAST(fset, file, path), nil
}

func scanRepoLogKeyIDRedactAST(fset *token.FileSet, file *ast.File, path string) []repoErrViolation {
	var out []repoErrViolation
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isLogLevelCall(call) {
			return true
		}
		for i := 1; i < len(call.Args); i++ {
			for _, lit := range collectStringLiterals(call.Args[i]) {
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				if _, banned := bannedLogAttrLiterals[s]; banned {
					out = append(out, repoErrViolation{
						Rule:    ruleRepoLogKeyIDRedact,
						File:    path,
						Line:    fset.Position(lit.Pos()).Line,
						Message: fmt.Sprintf("banned log attr literal %q (key IDs belong on metric labels)", s),
					})
				}
			}
		}
		return true
	})
	return out
}

// isLogLevelCall reports whether call is a method call whose Sel.Name is in
// the closed slogLevelMethods set. Covers both `slog.Warn(...)` package-level
// calls and `r.logger.WarnContext(...)` receiver calls without binding to the
// slog package import path.
//
// Exact-match (not prefix) is used to avoid false positives on method names
// like ErrorList or WarnCounter that start with a level keyword but are not
// log emitters.
func isLogLevelCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	_, ok = slogLevelMethods[sel.Sel.Name]
	return ok
}

// collectStringLiterals walks expr's AST sub-tree and returns every
// *ast.BasicLit Kind=STRING. Walks recursively so that
// `slog.String("key_id", x)` (a CallExpr with a literal arg) is reached
// even though it is itself an argument of the outer log call.
func collectStringLiterals(expr ast.Expr) []*ast.BasicLit {
	var out []*ast.BasicLit
	ast.Inspect(expr, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok {
			return true
		}
		if lit.Kind == token.STRING {
			out = append(out, lit)
		}
		return true
	})
	return out
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
		// Negative fixtures validating that exact-match (not prefix) is used for
		// isLogLevelCall: methods starting with a level keyword but not in the
		// closed slogLevelMethods set must NOT be flagged.
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
		tc := tc
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
