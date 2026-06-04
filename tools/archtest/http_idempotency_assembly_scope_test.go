package archtest

// http_idempotency_assembly_scope_test.go — structural gates for the
// full-assembly HTTP idempotency scope governance decision (#1449).
//
//   - INVARIANT: HTTP-IDEMPOTENCY-STORE-STATELESS-FROZEN-01
//   - INVARIANT: HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01
//
// # Why these two gates exist (governance: ADR 202606051000-1449)
//
// Issue #1449 codifies that the framework HTTP idempotency replay store is
// **assembly-wide**: every pod in an assembly that shares one Redis backend
// deduplicates the same logical request, because (a) the replay namespace
// "_runtime" carries no per-cell/pod dimension, and (b) the per-request
// idempotency (ns,key) is derived only from request + principal data — never
// from the serving node/listener/cell. The ADR's design promise rests on two
// structural facts, frozen here so they cannot silently regress:
//
//   - α HTTP-IDEMPOTENCY-STORE-STATELESS-FROZEN-01: the Redis store holds NO
//     in-memory replay state (all state lives in Redis), so two store instances
//     on the same Redis are interchangeable ⇒ cross-pod replay works. The
//     adapters/redis.HTTPIdempotencyStore struct is frozen to exactly
//     {rdb cmdable, ns KeyNamespace}. An added in-memory cache field would break
//     the "any pod sees the same state" guarantee and is caught here.
//
//   - β HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01: the request key derivation
//     runtime/http/idempotency.buildNamespaceKey takes ONLY
//     (*auth.Principal, method, path, idemKey) and derives the (ns,key) pair
//     purely from those — it has no pod/listener/cell input and makes no calls
//     that could read node-local state. A pod/listener/cell parameter, or a
//     call to a node-identity source (os.Hostname, getenv, a provider), would
//     make the key node-specific and break assembly-wide dedup; both are caught.
//
// The A-layer cross-pod integration test
// (adapters/redis/http_idempotency_assembly_scope_test.go) is the behavioral
// proof that two store instances on one Redis replay each other's records;
// α makes that proof's premise ("no in-memory state") structurally true, and
// β guarantees the key both instances derive for the same logical request is
// identical regardless of serving node. The behavioral test + these two
// structural gates together make the full-assembly claim airtight.
//
// # AI-robust rating (.claude/rules/gocell/ai-robust.md)
//
//   - α: Hard — reflect schema freeze (field count + per-field name/type/exported
//     tuple). A field-set drift is inexpressible without a CI-visible failure.
//     Sibling范本: PEER-IDENTITY-FIELDS-FROZEN-01 / MODULE-PROVIDE-NO-VALUE-HANDOFF-01.
//   - β: Medium — AST signature + body form-lock (no string anchor; the param
//     type list and the body's reference set are pinned). Go cannot make the
//     ABSENCE of a future node-identity parameter type-system-Hard; the Hard
//     upgrade path is a sealed typed IdempotencyKey constructor funnel (Option C
//     in the #1449 plan, deliberately out of scope for this PR), tracked as the
//     cross-cell follow-up issue #1610. Same ceiling family as #851/#893/#1282.
//
// # Blind spots + reverse self-checks (ai-robust mandate)
//
//   - α reflect freeze blind spots (embedding / alias re-shape / wrong type /
//     unexported drift) are each covered by checkHTTPIdemStoreShape and proven
//     non-vacuous by TestHTTPIdempotencyStoreStatelessFrozen01_ReverseBlindSpot.
//   - β AST freeze blind spots: (1) adding a node-identity PARAM → caught by the
//     param-type freeze; (2) referencing a package-level node-identity source or
//     calling os/net/runtime → caught by the body free-reference + no-call check;
//     (3) leaking extra principal fields into the key → caught by the
//     selector-allowlist {TenantID,Subject}. Non-vacuity proven by
//     TestHTTPIdempotencyKeyNodeAgnostic01_ReverseBlindSpot against inline
//     malformed source. Known tightness (documented, intentional): the body must
//     be call-free, so a future benign refactor introducing any call (even
//     strings.Join) trips the gate — that is the deliberate review checkpoint for
//     changes to key derivation, not a defect.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
)

// ---------------------------------------------------------------------------
// α — HTTP-IDEMPOTENCY-STORE-STATELESS-FROZEN-01 (reflect field freeze)
// ---------------------------------------------------------------------------

const ruleHTTPIdemStoreStatelessFrozen01 = "HTTP-IDEMPOTENCY-STORE-STATELESS-FROZEN-01"

// httpIdemStoreWantFields is the frozen field set of
// adapters/redis.HTTPIdempotencyStore: name → reflect type string. Both fields
// are unexported. The store MUST hold only its Redis handle (rdb) and its owner
// namespace (ns) — any additional field (most dangerously an in-memory replay
// cache) would break the cross-pod equivalence the full-assembly scope relies
// on. Changing this set is an assembly-scope contract change that must be made
// together with ADR 202606051000-1449 + the cross-pod integration test.
var httpIdemStoreWantFields = map[string]string{
	"rdb": "redis.cmdable",
	"ns":  "redis.KeyNamespace",
}

// checkHTTPIdemStoreShape returns violation messages for dt against the frozen
// HTTPIdempotencyStore field set; empty means conforming. Extracted so the
// reverse self-check can prove the detector flags malformed shapes.
func checkHTTPIdemStoreShape(dt reflect.Type) []string {
	if dt.Kind() != reflect.Struct {
		return []string{fmt.Sprintf("Kind = %s, want struct", dt.Kind())}
	}
	var violations []string
	if dt.NumField() != len(httpIdemStoreWantFields) {
		violations = append(violations, fmt.Sprintf(
			"NumField = %d, want exactly %d (an in-memory replay-state field breaks cross-pod equivalence)",
			dt.NumField(), len(httpIdemStoreWantFields),
		))
	}
	for i := 0; i < dt.NumField(); i++ {
		f := dt.Field(i)
		if f.Anonymous {
			violations = append(violations, fmt.Sprintf(
				"field %q is embedded (embedding can smuggle in-memory state past a name-keyed freeze)", f.Name,
			))
			continue
		}
		if f.IsExported() {
			violations = append(violations, fmt.Sprintf(
				"field %q is exported (the store's fields are private composition wiring, not a public surface)", f.Name,
			))
		}
		wantType, ok := httpIdemStoreWantFields[f.Name]
		if !ok {
			violations = append(violations, fmt.Sprintf("unexpected field %q (%s)", f.Name, f.Type.String()))
			continue
		}
		if ts := f.Type.String(); ts != wantType {
			violations = append(violations, fmt.Sprintf("field %q type = %q, want %q", f.Name, ts, wantType))
		}
	}
	return violations
}

// TestHTTPIdempotencyStoreStatelessFrozen01 freezes the HTTPIdempotencyStore
// field set so the store stays purely Redis-backed (no in-memory replay state),
// which is the structural premise of full-assembly cross-pod replay.
func TestHTTPIdempotencyStoreStatelessFrozen01(t *testing.T) {
	t.Parallel()
	violations := checkHTTPIdemStoreShape(reflect.TypeOf(adapterredis.HTTPIdempotencyStore{}))
	for _, v := range violations {
		t.Errorf("%s: %s. The frozen field set is %v; if this change is intentional, update "+
			"ADR 202606051000-1449 (assembly-scope) + this golden in the same PR, and confirm the cross-pod "+
			"replay integration test still holds.",
			ruleHTTPIdemStoreStatelessFrozen01, v, httpIdemStoreWantFields)
	}
}

// TestHTTPIdempotencyStoreStatelessFrozen01_ReverseBlindSpot proves the detector
// is non-vacuous: the real struct yields zero violations, and each
// deliberately-malformed local shape yields at least one.
func TestHTTPIdempotencyStoreStatelessFrozen01_ReverseBlindSpot(t *testing.T) {
	t.Parallel()

	if v := checkHTTPIdemStoreShape(reflect.TypeOf(adapterredis.HTTPIdempotencyStore{})); len(v) != 0 {
		t.Errorf("%s self-test: detector flagged the real conforming struct (vacuous-pass risk): %v",
			ruleHTTPIdemStoreStatelessFrozen01, v)
	}

	// Local malformed shapes. Field types differ from "redis.cmdable" /
	// "redis.KeyNamespace" (which are unnameable here), so each trips a violation.
	// Keyed composite literals reference each field so the `unused` linter does
	// not flag these reflect-only fixtures.
	type wrongType struct {
		rdb int
		ns  int
	}
	type extraInMemoryState struct {
		rdb   int
		ns    int
		cache int // a smuggled in-memory replay cache
	}
	type renamed struct {
		db        int
		namespace int
	}
	type embedded struct {
		any // embedded — re-opens the surface
		rdb int
		ns  int
	}
	bad := map[string]reflect.Type{
		"wrong-field-type":      reflect.TypeOf(wrongType{rdb: 0, ns: 0}),
		"extra-in-memory-state": reflect.TypeOf(extraInMemoryState{rdb: 0, ns: 0, cache: 0}),
		"renamed-fields":        reflect.TypeOf(renamed{db: 0, namespace: 0}),
		"embedded-field":        reflect.TypeOf(embedded{any: nil, rdb: 0, ns: 0}),
	}
	for name, dt := range bad {
		if v := checkHTTPIdemStoreShape(dt); len(v) == 0 {
			t.Errorf("%s self-test: detector passed malformed shape %q (blind spot): expected ≥1 violation",
				ruleHTTPIdemStoreStatelessFrozen01, name)
		}
	}
}

// ---------------------------------------------------------------------------
// β — HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01 (AST signature + body freeze)
// ---------------------------------------------------------------------------

const ruleHTTPIdemKeyNodeAgnostic01 = "HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01"

// buildNamespaceKeyFnName is the frozen key-derivation function in
// runtime/http/idempotency/middleware.go.
const buildNamespaceKeyFnName = "buildNamespaceKey"

// principalSelectorAllowlist is the set of *auth.Principal fields the key
// derivation may read. Only tenant + subject identity may flow into the key;
// nothing node-local exists on Principal, and adding any other selector here
// would be a deliberate, reviewed change.
var principalSelectorAllowlist = map[string]struct{}{"TenantID": {}, "Subject": {}}

// TestHTTPIdempotencyKeyNodeAgnostic01 freezes buildNamespaceKey's signature and
// body so the idempotency key carries no per-node/listener/cell identity.
func TestHTTPIdempotencyKeyNodeAgnostic01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	path := filepath.Join(root, "runtime", "http", "idempotency", "middleware.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	require.NoError(t, err, "parse %s", path)

	fn, ok := findTopLevelFuncDecl(file, buildNamespaceKeyFnName)
	require.Truef(t, ok, "%s: %s not found in middleware.go", ruleHTTPIdemKeyNodeAgnostic01, buildNamespaceKeyFnName)

	for _, v := range checkBuildNamespaceKeySignature(fn) {
		t.Errorf("%s (signature): %s. The key derivation must take exactly "+
			"(*auth.Principal, method, path, idemKey string) — a pod/listener/cell parameter would make "+
			"the idempotency key node-specific and break assembly-wide dedup. If intentional, update "+
			"ADR 202606051000-1449 + this gate + the cross-pod test in the same PR.", ruleHTTPIdemKeyNodeAgnostic01, v)
	}
	for _, v := range checkBuildNamespaceKeyBody(fn) {
		t.Errorf("%s (body): %s. The key must be a pure derivation of (principal tenant/subject + "+
			"method/path/idemKey) — no calls and no references to node-local state. If intentional, "+
			"update ADR 202606051000-1449 + this gate in the same PR.", ruleHTTPIdemKeyNodeAgnostic01, v)
	}
}

// checkBuildNamespaceKeySignature pins the parameter list to exactly
// (*auth.Principal, string, string, string) and the results to (string, string).
func checkBuildNamespaceKeySignature(fn *ast.FuncDecl) []string {
	var violations []string
	params := flattenFieldList(fn.Type.Params)
	if len(params) != 4 {
		violations = append(violations, fmt.Sprintf(
			"param count = %d, want exactly 4 (*auth.Principal, method, path, idemKey string)", len(params)))
		return violations // positional checks below assume 4
	}
	if !isPrincipalPtr(params[0].typ) {
		violations = append(violations, fmt.Sprintf(
			"param[0] type = %s, want *auth.Principal (the only identity source allowed into the key)",
			exprString(params[0].typ)))
	}
	for i := 1; i < 4; i++ {
		if !isStringIdent(params[i].typ) {
			violations = append(violations, fmt.Sprintf(
				"param[%d] type = %s, want string", i, exprString(params[i].typ)))
		}
	}
	results := flattenFieldList(fn.Type.Results)
	if len(results) != 2 {
		violations = append(violations, fmt.Sprintf("result count = %d, want exactly 2 (ns, key string)", len(results)))
	} else {
		for i := 0; i < 2; i++ {
			if !isStringIdent(results[i].typ) {
				violations = append(violations, fmt.Sprintf("result[%d] type = %s, want string", i, exprString(results[i].typ)))
			}
		}
	}
	return violations
}

// checkBuildNamespaceKeyBody asserts the body is a pure data derivation of its
// sanctioned inputs, so nothing node-local can enter the key:
//   - no function calls (no os.Hostname / getenv / node-identity provider);
//   - every selector reads *auth.Principal via the param `p` and only its
//     allowlisted identity fields {TenantID, Subject} — any pkg.X / other
//     selector base is an external reference and is rejected;
//   - every bare value identifier is either declared in the function (params,
//     named results, locals) or the single sanctioned const noTenantSentinel —
//     a free reference to any other package-level symbol (e.g. a process node-id
//     var) is rejected. This is the free-identifier closure that makes the
//     "no node-local state" claim hold without a param or a call.
func checkBuildNamespaceKeyBody(fn *ast.FuncDecl) []string {
	if fn.Body == nil {
		return []string{"missing body"}
	}
	// Resolve the principal parameter name (param[0]); default "p".
	principalName := "p"
	if params := flattenFieldList(fn.Type.Params); len(params) > 0 && params[0].name != "" {
		principalName = params[0].name
	}

	declared := collectDeclaredNames(fn)
	// Selector field names (.Sel) are not free variables; collect them so the
	// free-identifier pass skips them.
	selFields := map[*ast.Ident]struct{}{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			selFields[sel.Sel] = struct{}{}
		}
		return true
	})

	var violations []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			violations = append(violations, fmt.Sprintf(
				"contains a function call %q (key derivation must be call-free so no node-local state can enter the key)",
				exprString(x.Fun)))
		case *ast.SelectorExpr:
			base, ok := x.X.(*ast.Ident)
			if !ok || base.Name != principalName {
				violations = append(violations, fmt.Sprintf(
					"selector %s reads a non-principal source (only %s.<field> is allowed)",
					exprString(x), principalName))
				return true
			}
			if _, allowed := principalSelectorAllowlist[x.Sel.Name]; !allowed {
				violations = append(violations, fmt.Sprintf(
					"selector %s.%s reads a principal field outside the allowlist {TenantID, Subject}",
					principalName, x.Sel.Name))
			}
		case *ast.Ident:
			if _, isField := selFields[x]; isField {
				return true // a selector field name, not a free variable
			}
			if isPredeclaredIdent(x.Name) {
				return true
			}
			if _, ok := declared[x.Name]; ok {
				return true
			}
			if x.Name == noTenantSentinelConst {
				return true // the single sanctioned package-level const
			}
			violations = append(violations, fmt.Sprintf(
				"free reference %q (a package-level symbol other than %s may be node-local/external state)",
				x.Name, noTenantSentinelConst))
		}
		return true
	})
	return violations
}

// noTenantSentinelConst is the only package-level identifier the key derivation
// may reference (the empty-tenant namespace sentinel in middleware.go).
const noTenantSentinelConst = "noTenantSentinel"

// collectDeclaredNames returns the identifiers declared inside fn: parameters,
// named results, and locals introduced by `:=`, `var`, and `range`. These are
// the non-free identifiers the body may reference.
func collectDeclaredNames(fn *ast.FuncDecl) map[string]struct{} {
	declared := map[string]struct{}{}
	for _, p := range flattenFieldList(fn.Type.Params) {
		if p.name != "" {
			declared[p.name] = struct{}{}
		}
	}
	for _, r := range flattenFieldList(fn.Type.Results) {
		if r.name != "" {
			declared[r.name] = struct{}{}
		}
	}
	if fn.Body == nil {
		return declared
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE {
				for _, lhs := range x.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						declared[id.Name] = struct{}{}
					}
				}
			}
		case *ast.ValueSpec:
			for _, id := range x.Names {
				declared[id.Name] = struct{}{}
			}
		case *ast.RangeStmt:
			if id, ok := x.Key.(*ast.Ident); ok {
				declared[id.Name] = struct{}{}
			}
			if id, ok := x.Value.(*ast.Ident); ok {
				declared[id.Name] = struct{}{}
			}
		}
		return true
	})
	return declared
}

// isPredeclaredIdent reports whether name is a Go predeclared identifier
// (builtins, basic types, constants, blank). These are not free package-level
// references for the purposes of the node-agnostic body check.
func isPredeclaredIdent(name string) bool {
	switch name {
	case "_", "true", "false", "nil", "iota",
		"bool", "byte", "rune", "string", "error", "any", "comparable",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128",
		"len", "cap", "make", "new", "append", "copy", "delete",
		"close", "panic", "recover", "print", "println", "min", "max", "clear":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// β reverse self-check (non-vacuity)
// ---------------------------------------------------------------------------

// TestHTTPIdempotencyKeyNodeAgnostic01_ReverseBlindSpot proves the β detectors
// flag drift: a conforming inline function yields zero violations, and each
// malformed variant (node-identity param, external call, package-var reference,
// extra principal field) yields ≥1.
func TestHTTPIdempotencyKeyNodeAgnostic01_ReverseBlindSpot(t *testing.T) {
	t.Parallel()

	good := `package p
func buildNamespaceKey(p *auth.Principal, method, path, idemKey string) (ns, key string) {
	ns = p.TenantID
	if ns == "" {
		ns = noTenantSentinel
	}
	key = p.Subject + "\x00" + method + "\x00" + path + "\x00" + idemKey
	return
}`
	if sv, bv := runBuildKeyDetectors(t, good); len(sv) != 0 || len(bv) != 0 {
		t.Errorf("%s self-test: detectors flagged the conforming form (vacuous-pass risk): sig=%v body=%v",
			ruleHTTPIdemKeyNodeAgnostic01, sv, bv)
	}

	bad := map[string]string{
		"node-identity-param": `package p
func buildNamespaceKey(p *auth.Principal, method, path, idemKey, podID string) (ns, key string) {
	key = p.Subject + podID
	return
}`,
		"external-call": `package p
func buildNamespaceKey(p *auth.Principal, method, path, idemKey string) (ns, key string) {
	host, _ := os.Hostname()
	key = p.Subject + host
	return
}`,
		"package-var-reference": `package p
func buildNamespaceKey(p *auth.Principal, method, path, idemKey string) (ns, key string) {
	key = p.Subject + processNodeID
	return
}`,
		"extra-principal-field": `package p
func buildNamespaceKey(p *auth.Principal, method, path, idemKey string) (ns, key string) {
	key = p.Subject + p.Region
	return
}`,
	}
	for name, src := range bad {
		sv, bv := runBuildKeyDetectors(t, src)
		if len(sv) == 0 && len(bv) == 0 {
			t.Errorf("%s self-test: detectors passed malformed form %q (blind spot): expected ≥1 violation",
				ruleHTTPIdemKeyNodeAgnostic01, name)
		}
	}
}

// runBuildKeyDetectors parses an inline buildNamespaceKey source and runs both
// β detectors against it. Each malformed fixture trips ≥1 detector: a node-id
// param trips the signature freeze; an os.Hostname call trips the no-call body
// rule; a bare process-node-id reference trips the free-identifier closure; an
// extra principal field trips the selector allowlist.
func runBuildKeyDetectors(t *testing.T, src string) (sig, body []string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "inline.go", src, 0)
	require.NoError(t, err, "parse inline fixture")
	fn, ok := findTopLevelFuncDecl(f, buildNamespaceKeyFnName)
	require.True(t, ok, "inline fixture missing buildNamespaceKey")
	return checkBuildNamespaceKeySignature(fn), checkBuildNamespaceKeyBody(fn)
}

// ---------------------------------------------------------------------------
// Shared AST helpers
// ---------------------------------------------------------------------------

// flatParam is one positional parameter/result after flattening grouped fields.
type flatParam struct {
	name string
	typ  ast.Expr
}

// flattenFieldList expands grouped fields (e.g. `method, path, idemKey string`)
// into one entry per positional name. Unnamed fields contribute a single entry
// with an empty name.
func flattenFieldList(fl *ast.FieldList) []flatParam {
	if fl == nil {
		return nil
	}
	var out []flatParam
	for _, field := range fl.List {
		if len(field.Names) == 0 {
			out = append(out, flatParam{name: "", typ: field.Type})
			continue
		}
		for _, n := range field.Names {
			out = append(out, flatParam{name: n.Name, typ: field.Type})
		}
	}
	return out
}

func isStringIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "string"
}

// isPrincipalPtr reports whether e is `*<pkg>.Principal` (the auth.Principal
// pointer). Selector-name match is alias-robust enough for a Medium gate; the
// Hard form would be a sealed key constructor (out of scope, #1449 follow-up).
func isPrincipalPtr(e ast.Expr) bool {
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Principal"
}
