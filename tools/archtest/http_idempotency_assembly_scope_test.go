package archtest

// http_idempotency_assembly_scope_test.go — structural gates for the
// full-assembly HTTP idempotency scope governance decision (#1449), with the
// β gate upgraded to the sealed typed IdempotencyKey funnel (#1610 funnel slice).
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
//   - β HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01: the request key is a SEALED typed
//     runtime/http/idempotency.IdempotencyKey, minted only by DeriveKey from the
//     isolation tuple (tenantID, subject, method, path, idemKey). Sealing makes
//     the node-agnostic property structural on two directions: no OTHER package
//     can mint a key and splice in a pod/listener/cell id (sealed construction),
//     and Store.Claim takes IdempotencyKey (not raw strings) so a hand-built
//     (ns,key) pair is inexpressible at the store boundary. The require-isolation-
//     tuple half (every dimension must FLOW into the key) stays an AST taint walk
//     on DeriveKey (Go cannot make body-flow type-Hard).
//
// The A-layer cross-pod integration test
// (adapters/redis/http_idempotency_assembly_scope_test.go) is the behavioral
// proof that two store instances on one Redis replay each other's records;
// α makes that proof's premise ("no in-memory state") structurally true, and
// β guarantees the key both instances derive for the same logical request is
// identical regardless of serving node. The behavioral test + these two
// structural gates together make the full-assembly claim airtight.
//
// # AI-robust rating (.claude/rules/gocell/ai-robust.md) — honest, NOT flat Hard
//
//   - α: Hard — reflect schema freeze (field count + per-field name/type/exported
//     tuple). A field-set drift is inexpressible without a CI-visible failure.
//     Sibling范本: PEER-IDENTITY-FIELDS-FROZEN-01 / MODULE-PROVIDE-NO-VALUE-HANDOFF-01.
//   - β node-agnostic / ban-external-source — 3-axis split (sealed construction
//     范本; same shape as CellLabel / holder-seal #893, NOT flat Hard):
//       · downstream Hard: Store.Claim takes IdempotencyKey, so a raw (ns,key)
//         string pair is inexpressible at the boundary (type system).
//       · upstream-external Hard: IdempotencyKey{ns,key} fields are unexported ⇒
//         no OTHER package can mint a key.
//       · upstream-in-package Medium: the compiler does NOT stop an in-package
//         populated literal or a 2nd in-package producer; the prong-1 reflect
//         field freeze + go/types sole-producer scan are the only backstop there.
//   - β require-isolation-tuple — Medium, genuine Go ceiling (gh #1650): Go
//     cannot express "DeriveKey's body consumes all five params into the key",
//     and the five same-type string params could be transposed at the (single,
//     reviewed) callsite. The AST taint walk on DeriveKey is the Medium backstop.
//     Same ceiling family as #851/#893/#1282/#1552. NOT typestate-upgradeable
//     (a builder enforces presence-of-setters, which positional params already
//     give; it does not make body-flow Hard).
//
// # Blind spots + reverse self-checks (ai-robust mandate)
//
//   - α reflect freeze blind spots (embedding / alias re-shape / wrong type /
//     unexported drift) are each covered by checkHTTPIdemStoreShape and proven
//     non-vacuous by TestHTTPIdempotencyStoreStatelessFrozen01_ReverseBlindSpot.
//   - β prong-1 reflect freeze blind spots (extra field / exported field / rename
//     / wrong type / embedding) → checkIdempotencyKeyShape; deserialization
//     backdoor (UnmarshalJSON/Text/Binary/Scan — they return error, NOT
//     IdempotencyKey, so a returns-IdempotencyKey scan never catches them, #1488
//     F4 lesson) + builder laundering (a method returning IdempotencyKey) →
//     checkIdempotencyKeyMethods. Both proven non-vacuous by the
//     ReverseBlindSpot test (synthetic reflect shapes + a package-level
//     method-bearing fixture, since Go forbids method decls inside a func).
//   - β prong-1b/2 go/types blind spots: a 2nd package-level producer (func or
//     function-typed var) → sole-producer diff (scan is exported-only — an
//     unexported package-level func/var returning IdempotencyKey is NOT caught;
//     the upstream-external Hard construction seal via unexported fields is the
//     backstop there); reverting Store.Claim to raw strings → param-type freeze;
//     both fail loud via the visited-guard if the package path moves.
//   - β prong-1b/2 method blind spot: checkIdempotencyKeyMethods only scans
//     methods of IdempotencyKey itself (via reflect.PointerTo), and the go/types
//     sole-producer scan skips methods (Recv != nil) — so a method on ANOTHER
//     package type that returns IdempotencyKey (e.g. func (b keyBuilder) Build()
//     IdempotencyKey) is caught by neither. It is bounded by the
//     upstream-external Hard seal: such a method still cannot populate the
//     unexported fields except via DeriveKey. Known in-package Medium blind spot.
//   - β prong-3 taint walk blind spots: a 6th DeriveKey param (node-id vector) →
//     signature freeze; dropping any isolation dimension from the body, INCLUDING
//     a dummy read `_ = method` that references the input but never flows it into
//     the key → checkDeriveKeyRequiredUse (taint flow, not presence). Proven
//     non-vacuous by inline malformed DeriveKey fixtures. Note: flat-string params
//     remove the old *auth.Principal selector/alias blind spots entirely (no more
//     extra-principal-field / chained-selector / type-alias residual). Known
//     tightness: a construction form other than `return IdempotencyKey{ns:…,key:…}`
//     trips the taint sink lookup — a deliberate review checkpoint, not a defect.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/runtime/http/idempotency"
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
//
// The type values are reflect.Type.String() forms, not reflect.Type identities:
// `rdb` is the package-unexported interface `cmdable`, which cannot be named from
// outside adapters/redis, so a `f.Type == reflect.TypeOf(...)` identity check is
// not expressible here — the string form is the strongest check available for an
// unexported field type. reflect.Type.String() is "<pkgname>.<TypeName>"; since
// this archtest imports adapters/redis directly (no alias), the "redis." prefix
// does not drift. If a future Go release changed the String() format, the primary
// test would fail loudly with a "field type = X, want Y" message — a visible
// review checkpoint, not a silent pass.
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
// β — HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01 (sealed IdempotencyKey funnel)
// ---------------------------------------------------------------------------

const ruleHTTPIdemKeyNodeAgnostic01 = "HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01"

const (
	idempotencyKeyTypeName = "IdempotencyKey"
	deriveKeyFnName        = "DeriveKey"
	deriveKeyFile          = "key.go"
	storeIfaceName         = "Store"
	storeClaimMethodName   = "Claim"
)

// deriveKeyProducerAllowed is the exhaustive set of exported package-level
// surfaces in runtime/http/idempotency that may PRODUCE an IdempotencyKey value.
// DeriveKey is the sole constructor; a second producer (FromStrings / a
// function-typed var / an Unmarshal) would launder an arbitrary (ns,key) —
// possibly carrying a node id — into a sealed key, defeating the seal. (A future
// cross-cell DeriveCommandKey, #1610, would be added here in the same PR.)
var deriveKeyProducerAllowed = map[string]struct{}{deriveKeyFnName: {}}

// idempotencyKeyForbiddenMethods are deserialization entries that would let an
// external package populate a sealed IdempotencyKey from bytes, bypassing
// DeriveKey. They return error (not IdempotencyKey), so a returns-IdempotencyKey
// scan never catches them — they are banned by name (#1488 F4 lesson).
var idempotencyKeyForbiddenMethods = map[string]bool{
	"UnmarshalJSON":   true,
	"UnmarshalText":   true,
	"UnmarshalBinary": true,
	"Scan":            true,
}

// checkIdempotencyKeyShape freezes the IdempotencyKey field set to exactly two
// unexported string fields {ns,key}. Pure so the reverse self-check can feed it
// synthetic malformed shapes (mirrors the α detector).
func checkIdempotencyKeyShape(dt reflect.Type) []string {
	if dt.Kind() != reflect.Struct {
		return []string{fmt.Sprintf("Kind = %s, want struct (sealed construction)", dt.Kind())}
	}
	want := map[string]struct{}{"ns": {}, "key": {}}
	var v []string
	if dt.NumField() != len(want) {
		v = append(v, fmt.Sprintf(
			"NumField = %d, want exactly %d ({ns,key}). An extra field (e.g. a pod/listener id) could "+
				"ride node-local state into the key.", dt.NumField(), len(want)))
	}
	for i := 0; i < dt.NumField(); i++ {
		f := dt.Field(i)
		if f.Anonymous {
			v = append(v, fmt.Sprintf("field %q is embedded (embedding can re-open external construction)", f.Name))
			continue
		}
		if f.IsExported() {
			v = append(v, fmt.Sprintf(
				"field %q is EXPORTED — an exported field lets any package build a populated "+
					"IdempotencyKey{...} literal and splice in a node id, breaking sealed construction.", f.Name))
		}
		if _, ok := want[f.Name]; !ok {
			v = append(v, fmt.Sprintf("unexpected field %q (%s) — the frozen set is {ns,key}", f.Name, f.Type))
			continue
		}
		if f.Type.Kind() != reflect.String {
			v = append(v, fmt.Sprintf("field %q type = %s, want string", f.Name, f.Type))
		}
	}
	return v
}

// checkIdempotencyKeyMethods bans deserialization backdoors and builder
// laundering on IdempotencyKey. Pure so the reverse self-check can feed it a
// synthetic method-bearing type (Go forbids method decls inside a func, so the
// reverse fixture is a package-level type).
func checkIdempotencyKeyMethods(dt reflect.Type) []string {
	var v []string
	pt := reflect.PointerTo(dt)
	for i := 0; i < pt.NumMethod(); i++ {
		m := pt.Method(i)
		if idempotencyKeyForbiddenMethods[m.Name] {
			v = append(v, fmt.Sprintf(
				"%s must not declare %q — a deserialization entry lets an external package build a key "+
					"from bytes, bypassing the sole constructor DeriveKey.", idempotencyKeyTypeName, m.Name))
			continue
		}
		mt := m.Func.Type() // receiver is in[0]; method results are the outs
		for o := 0; o < mt.NumOut(); o++ {
			if mt.Out(o) == dt {
				v = append(v, fmt.Sprintf(
					"%s method %q returns a new %s (builder laundering) — DeriveKey is the sole constructor.",
					idempotencyKeyTypeName, m.Name, idempotencyKeyTypeName))
			}
		}
	}
	return v
}

// TestHTTPIdempotencyKeyNodeAgnostic01_KeySealed freezes the sealed IdempotencyKey
// type: exactly two unexported string fields and no deserialization/builder
// backdoor. This is the upstream-external Hard gate of the node-agnostic
// invariant — no other package can mint a key, so none can splice a node id in.
func TestHTTPIdempotencyKeyNodeAgnostic01_KeySealed(t *testing.T) {
	t.Parallel()
	dt := reflect.TypeOf(idempotency.IdempotencyKey{})
	violations := append(checkIdempotencyKeyShape(dt), checkIdempotencyKeyMethods(dt)...)
	for _, vio := range violations {
		t.Errorf("%s (key sealed): %s. If a key-shape change is intentional, update ADR "+
			"202606051000-1449 + this freeze in the same PR.", ruleHTTPIdemKeyNodeAgnostic01, vio)
	}
}

// TestHTTPIdempotencyKeyNodeAgnostic01_SoleProducerAndSink pins, via go/types,
// (1) DeriveKey as the SOLE exported package-level producer of an IdempotencyKey
// value (a second producer would launder an arbitrary key), and (2) Store.Claim's
// post-ctx parameter as IdempotencyKey (the downstream Hard sink — a raw (ns,key)
// string pair is inexpressible at the boundary).
func TestHTTPIdempotencyKeyNodeAgnostic01_SoleProducerAndSink(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	visited := false
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./runtime/http/idempotency/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != httpIdempotencyPkgPath {
				return nil
			}
			visited = true
			scope := p.Pkg.Scope()

			ikObj := scope.Lookup(idempotencyKeyTypeName)
			if ikObj == nil {
				return []Diagnostic{{Message: ruleHTTPIdemKeyNodeAgnostic01 +
					"/SoleProducerAndSink: type IdempotencyKey not found in " + httpIdempotencyPkgPath}}
			}
			ikType := ikObj.Type()

			// (1) sole producer: exported package-level funcs AND function-typed
			// vars whose signature returns IdempotencyKey must be exactly {DeriveKey}.
			var producers []string
			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				if !obj.Exported() {
					continue
				}
				var sig *types.Signature
				switch o := obj.(type) {
				case *types.Func:
					if s, ok := o.Type().(*types.Signature); ok && s.Recv() == nil {
						sig = s
					}
				case *types.Var:
					if s, ok := o.Type().(*types.Signature); ok {
						sig = s
					}
				}
				if sig != nil && sigReturnsType(sig, ikType) {
					producers = append(producers, name)
				}
			}
			diags = append(diags, diffHTTPIdempotencyExpectedSet(
				"exported package-level surfaces producing IdempotencyKey",
				deriveKeyProducerAllowed, producers)...)

			// (2) Store.Claim sink: the post-ctx parameter must be IdempotencyKey.
			diags = append(diags, checkStoreClaimParamType(scope, ikType)...)
			return nil
		})

	if !visited {
		diags = append(diags, Diagnostic{Message: ruleHTTPIdemKeyNodeAgnostic01 +
			"/SoleProducerAndSink: " + httpIdempotencyPkgPath + " not scanned — the funnel freeze did not run."})
	}
	Report(t, ruleHTTPIdemKeyNodeAgnostic01+"/SoleProducerAndSink", diags)
}

// checkStoreClaimParamType asserts Store.Claim's parameter after ctx is exactly
// IdempotencyKey — the typed sink that makes a raw (ns,key) string pair
// inexpressible at the store boundary.
func checkStoreClaimParamType(scope *types.Scope, ikType types.Type) []Diagnostic {
	storeObj := scope.Lookup(storeIfaceName)
	if storeObj == nil {
		return []Diagnostic{{Message: ruleHTTPIdemKeyNodeAgnostic01 + "/SoleProducerAndSink: interface Store not found"}}
	}
	iface, ok := storeObj.Type().Underlying().(*types.Interface)
	if !ok {
		return []Diagnostic{{Message: ruleHTTPIdemKeyNodeAgnostic01 + "/SoleProducerAndSink: Store is not an interface"}}
	}
	for i := 0; i < iface.NumMethods(); i++ {
		m := iface.Method(i)
		if m.Name() != storeClaimMethodName {
			continue
		}
		sig, ok := m.Type().(*types.Signature)
		if !ok {
			continue
		}
		params := sig.Params()
		if params.Len() < 2 {
			return []Diagnostic{{Message: fmt.Sprintf(
				"%s/SoleProducerAndSink: Store.Claim has %d params, want (ctx, IdempotencyKey, …)",
				ruleHTTPIdemKeyNodeAgnostic01, params.Len())}}
		}
		if got := params.At(1).Type(); !types.Identical(got, ikType) {
			return []Diagnostic{{Message: fmt.Sprintf(
				"%s/SoleProducerAndSink: Store.Claim param[1] = %s, want IdempotencyKey. Reverting to a raw "+
					"(ns,key) string pair re-opens hand-built keys at the store boundary (the downstream Hard sink).",
				ruleHTTPIdemKeyNodeAgnostic01, got)}}
		}
		return nil
	}
	return []Diagnostic{{Message: ruleHTTPIdemKeyNodeAgnostic01 + "/SoleProducerAndSink: Store.Claim method not found"}}
}

// TestHTTPIdempotencyKeyNodeAgnostic01_RequiredUse is the require-isolation-tuple
// half (Medium residual, gh #1650): the sealed type makes "external code injects
// a node id" inexpressible, but Go CANNOT make "DeriveKey's body consumes all
// five isolation dimensions into the key" type-system-Hard. This AST taint walk on
// DeriveKey is the Medium backstop: each of (tenantID→ns, subject/method/path/
// idemKey→key) must FLOW (not merely be referenced) into the returned
// IdempotencyKey literal; a dropped dimension or a `_ = method` dummy read is
// caught. A 6th parameter (a node-id injection vector) trips the signature freeze.
func TestHTTPIdempotencyKeyNodeAgnostic01_RequiredUse(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	path := filepath.Join(root, "runtime", "http", "idempotency", deriveKeyFile)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err, "parse %s", path)

	fn, ok := findTopLevelFuncDecl(file, deriveKeyFnName)
	require.Truef(t, ok, "%s: %s not found in %s", ruleHTTPIdemKeyNodeAgnostic01, deriveKeyFnName, deriveKeyFile)

	for _, vio := range checkDeriveKeySignature(fn) {
		t.Errorf("%s (signature): %s. DeriveKey must take exactly five string isolation params and return "+
			"IdempotencyKey — a 6th param is the node-id injection vector. If intentional, update ADR "+
			"202606051000-1449 + this gate in the same PR.", ruleHTTPIdemKeyNodeAgnostic01, vio)
	}
	for _, vio := range checkDeriveKeyRequiredUse(fn) {
		t.Errorf("%s (required-use): %s. Every isolation dimension MUST flow into the key (#1650 Medium "+
			"backstop); dropping one collapses cross-tenant/-user/-endpoint isolation.", ruleHTTPIdemKeyNodeAgnostic01, vio)
	}
}

// checkDeriveKeySignature pins DeriveKey to exactly five string params and a
// single IdempotencyKey result.
func checkDeriveKeySignature(fn *ast.FuncDecl) []string {
	var v []string
	params := flattenFieldList(fn.Type.Params)
	if len(params) != 5 {
		v = append(v, fmt.Sprintf(
			"param count = %d, want exactly 5 (tenantID, subject, method, path, idemKey string)", len(params)))
		return v // positional taint resolution below assumes 5
	}
	for i, p := range params {
		if !isStringIdent(p.typ) {
			v = append(v, fmt.Sprintf("param[%d] type = %s, want string", i, exprString(p.typ)))
		}
	}
	results := flattenFieldList(fn.Type.Results)
	if len(results) != 1 {
		v = append(v, fmt.Sprintf("result count = %d, want exactly 1 (IdempotencyKey)", len(results)))
	} else if id, ok := results[0].typ.(*ast.Ident); !ok || id.Name != idempotencyKeyTypeName {
		v = append(v, fmt.Sprintf("result type = %s, want IdempotencyKey", exprString(results[0].typ)))
	}
	return v
}

// isolationToken is a bit in the set of sanctioned isolation inputs that must
// flow into the returned key.
type isolationToken uint8

const (
	tokTenant  isolationToken = 1 << iota // tenantID → must reach the ns field
	tokSubject                            // subject  → must reach the key field
	tokMethod                             // method   → must reach the key field
	tokPath                               // path     → must reach the key field
	tokIdemKey                            // idemKey  → must reach the key field
)

// checkDeriveKeyRequiredUse taint-tracks each of the five params from its source
// to the ns/key fields of the returned IdempotencyKey literal. tenantID must
// reach the ns field; subject/method/path/idemKey must reach the key field. Flow
// (not mere presence) is tracked so a dummy read `_ = method` does not count.
//
// Completeness: the body is a flat composition of assignments + binary
// concatenation over the five params + the noTenantSentinel const (no calls,
// closures, or foreign selectors are needed to express the derivation), so the
// per-variable token fixpoint follows every value path. Rating is Medium (AST
// taint); the Hard form that would make a dropped dimension inexpressible is a
// genuine Go ceiling — see gh #1650.
func checkDeriveKeyRequiredUse(fn *ast.FuncDecl) []string {
	if fn.Body == nil {
		return []string{"missing body"}
	}
	params := flattenFieldList(fn.Type.Params)
	if len(params) != 5 {
		return nil // the signature freeze reports the shape
	}
	src := map[isolationToken]string{
		tokTenant: params[0].name, tokSubject: params[1].name, tokMethod: params[2].name,
		tokPath: params[3].name, tokIdemKey: params[4].name,
	}

	// Sinks are the ns/key fields of the returned IdempotencyKey composite
	// literal. The synthetic sink names cannot collide with Go identifiers.
	const nsSink, keySink = "$ns", "$key"
	nsExpr, keyExpr, found := returnedKeyFields(fn.Body)
	if !found {
		return []string{"DeriveKey must end by returning an IdempotencyKey{ns: …, key: …} composite literal " +
			"(the taint sinks); a different construction form is a deliberate review checkpoint"}
	}

	taint := map[string]isolationToken{}
	tokensOf := func(expr ast.Expr) isolationToken {
		var bits isolationToken
		ast.Inspect(expr, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				for tok, name := range src {
					if name != "" && id.Name == name {
						bits |= tok
					}
				}
				bits |= taint[id.Name]
			}
			return true
		})
		return bits
	}

	type edge struct {
		sink string
		rhs  ast.Expr
	}
	var edges []edge
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
			for i, lhs := range as.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					edges = append(edges, edge{sink: id.Name, rhs: as.Rhs[i]})
				}
			}
		}
		return true
	})
	edges = append(edges, edge{sink: nsSink, rhs: nsExpr}, edge{sink: keySink, rhs: keyExpr})

	for changed := true; changed; {
		changed = false
		for _, e := range edges {
			if after := taint[e.sink] | tokensOf(e.rhs); after != taint[e.sink] {
				taint[e.sink] = after
				changed = true
			}
		}
	}

	var v []string
	if taint[nsSink]&tokTenant == 0 {
		v = append(v, "tenantID never flows into the ns field — cross-tenant replay")
	}
	for _, want := range []struct {
		tok   isolationToken
		label string
	}{{tokSubject, "subject"}, {tokMethod, "method"}, {tokPath, "path"}, {tokIdemKey, "idemKey"}} {
		if taint[keySink]&want.tok == 0 {
			v = append(v, fmt.Sprintf(
				"%s never flows into the key field — isolation dropped (dummy read `_ = %s` does not count)",
				want.label, want.label))
		}
	}
	return v
}

// returnedKeyFields extracts the ns/key field value expressions from the
// IdempotencyKey composite literal in the function's return statement.
func returnedKeyFields(body *ast.BlockStmt) (nsExpr, keyExpr ast.Expr, found bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		cl, ok := ret.Results[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		if id, ok := cl.Type.(*ast.Ident); !ok || id.Name != idempotencyKeyTypeName {
			return true
		}
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "ns":
				nsExpr = kv.Value
			case "key":
				keyExpr = kv.Value
			}
		}
		if nsExpr != nil && keyExpr != nil {
			found = true
			return false
		}
		return true
	})
	return
}

// ---------------------------------------------------------------------------
// β reverse self-checks (non-vacuity)
// ---------------------------------------------------------------------------

// ikMethodFixture is a package-level reverse-self-check fixture for the METHOD
// detectors: a type carrying a deserialization backdoor (UnmarshalJSON) and a
// builder-laundering method (Clone returns itself). Go forbids method
// declarations inside a func, so it must be package-level; the field set is
// irrelevant to checkIdempotencyKeyMethods, so it is empty.
type ikMethodFixture struct{}

func (ikMethodFixture) UnmarshalJSON([]byte) error { return nil }
func (i ikMethodFixture) Clone() ikMethodFixture   { return i }

// TestHTTPIdempotencyKeyNodeAgnostic01_ReverseBlindSpot proves the β detectors
// flag drift: the real type/constructor yield zero violations, and each malformed
// variant yields ≥1.
func TestHTTPIdempotencyKeyNodeAgnostic01_ReverseBlindSpot(t *testing.T) {
	t.Parallel()

	// Non-vacuity: the real sealed type passes both reflect detectors.
	realKey := reflect.TypeOf(idempotency.IdempotencyKey{})
	if v := append(checkIdempotencyKeyShape(realKey), checkIdempotencyKeyMethods(realKey)...); len(v) != 0 {
		t.Errorf("%s self-test: detectors flagged the real conforming IdempotencyKey (vacuous-pass risk): %v",
			ruleHTTPIdemKeyNodeAgnostic01, v)
	}

	// Shape reverse fixtures (synthetic reflect types). Keyed composite literals
	// reference each field so the `unused` linter does not flag them.
	type wrongType struct {
		ns  int
		key int
	}
	type extraField struct {
		ns  string
		key string
		pod string // a smuggled node id
	}
	type renamed struct {
		namespace string
		k         string
	}
	type exported struct {
		Ns  string // re-opens external populated-literal construction
		key string
	}
	type embedded struct {
		any
		ns  string
		key string
	}
	badShapes := map[string]reflect.Type{
		"wrong-field-type": reflect.TypeOf(wrongType{ns: 0, key: 0}),
		"extra-field":      reflect.TypeOf(extraField{ns: "", key: "", pod: ""}),
		"renamed-fields":   reflect.TypeOf(renamed{namespace: "", k: ""}),
		"exported-field":   reflect.TypeOf(exported{Ns: "", key: ""}),
		"embedded-field":   reflect.TypeOf(embedded{any: nil, ns: "", key: ""}),
	}
	for name, dt := range badShapes {
		if v := checkIdempotencyKeyShape(dt); len(v) == 0 {
			t.Errorf("%s self-test: shape detector passed malformed %q (blind spot)", ruleHTTPIdemKeyNodeAgnostic01, name)
		}
	}

	// Method reverse fixture: UnmarshalJSON (forbidden) + Clone (returns self).
	if v := checkIdempotencyKeyMethods(reflect.TypeOf(ikMethodFixture{})); len(v) < 2 {
		t.Errorf("%s self-test: method detector missed the backdoor/builder fixture (got %v)",
			ruleHTTPIdemKeyNodeAgnostic01, v)
	}

	// Taint reverse fixtures (inline DeriveKey source). The conforming body yields
	// zero; each malformed variant yields ≥1 from the signature freeze or taint walk.
	if sv, uv := runDeriveKeyDetectors(t, deriveKeyGood); len(sv) != 0 || len(uv) != 0 {
		t.Errorf("%s self-test: detectors flagged the conforming DeriveKey (vacuous-pass): sig=%v use=%v",
			ruleHTTPIdemKeyNodeAgnostic01, sv, uv)
	}
	for name, src := range deriveKeyBad {
		if sv, uv := runDeriveKeyDetectors(t, src); len(sv) == 0 && len(uv) == 0 {
			t.Errorf("%s self-test: detectors passed malformed DeriveKey %q (blind spot)",
				ruleHTTPIdemKeyNodeAgnostic01, name)
		}
	}
}

const deriveKeyGood = `package p
func DeriveKey(tenantID, subject, method, path, idemKey string) IdempotencyKey {
	ns := tenantID
	if ns == "" {
		ns = noTenantSentinel
	}
	return IdempotencyKey{
		ns:  ns,
		key: subject + "\x00" + method + "\x00" + path + "\x00" + idemKey,
	}
}`

// deriveKeyBad: each fixture drops one isolation dimension (caught by the taint
// walk) or adds a node-id param (caught by the signature freeze). The dummy-read
// fixture references every input but flows only subject into the key.
var deriveKeyBad = map[string]string{ //nolint:gosec,gochecknoglobals // G101 false positive: values are Go source fixtures, not credentials
	"node-id-param": `package p
func DeriveKey(tenantID, subject, method, path, idemKey, podID string) IdempotencyKey {
	return IdempotencyKey{ns: tenantID + podID, key: subject + method + path + idemKey}
}`,
	"missing-tenant": `package p
func DeriveKey(tenantID, subject, method, path, idemKey string) IdempotencyKey {
	return IdempotencyKey{ns: noTenantSentinel, key: subject + method + path + idemKey}
}`,
	"missing-subject": `package p
func DeriveKey(tenantID, subject, method, path, idemKey string) IdempotencyKey {
	return IdempotencyKey{ns: tenantID, key: method + path + idemKey}
}`,
	"missing-method": `package p
func DeriveKey(tenantID, subject, method, path, idemKey string) IdempotencyKey {
	return IdempotencyKey{ns: tenantID, key: subject + path + idemKey}
}`,
	"missing-path": `package p
func DeriveKey(tenantID, subject, method, path, idemKey string) IdempotencyKey {
	return IdempotencyKey{ns: tenantID, key: subject + method + idemKey}
}`,
	"missing-idemkey": `package p
func DeriveKey(tenantID, subject, method, path, idemKey string) IdempotencyKey {
	return IdempotencyKey{ns: tenantID, key: subject + method + path}
}`,
	"dummy-read-bypass": `package p
func DeriveKey(tenantID, subject, method, path, idemKey string) IdempotencyKey {
	_ = method
	_ = path
	_ = idemKey
	return IdempotencyKey{ns: tenantID, key: subject}
}`,
}

// runDeriveKeyDetectors parses an inline DeriveKey source and runs the signature
// freeze + the required-use taint walk against it.
func runDeriveKeyDetectors(t *testing.T, src string) (sig, use []string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "inline.go", src, 0)
	require.NoError(t, err, "parse inline DeriveKey fixture")
	fn, ok := findTopLevelFuncDecl(f, deriveKeyFnName)
	require.True(t, ok, "inline fixture missing DeriveKey")
	return checkDeriveKeySignature(fn), checkDeriveKeyRequiredUse(fn)
}

// ---------------------------------------------------------------------------
// Shared AST helpers (file-local)
// ---------------------------------------------------------------------------

// flatParam is one positional parameter/result after flattening grouped fields.
type flatParam struct {
	name string
	typ  ast.Expr
}

// flattenFieldList expands grouped fields (e.g. `tenantID, subject string`) into
// one entry per positional name. Unnamed fields contribute a single entry with an
// empty name.
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
