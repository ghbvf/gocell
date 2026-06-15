//go:build archtest

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
//     runtime/http/idempotency.IdempotencyKey, minted only by its sanctioned
//     constructors — DeriveKey (HTTP record key, from tenantID/subject/method/
//     path/idemKey) and DeriveCommandKey (#1669 command dedup key, from
//     tenantID/subject/commandID). Both are single-sourced in
//     idempotencyKeyConstructors below, the one table every β prong iterates.
//     Sealing makes the node-agnostic property structural on two directions: no
//     OTHER package can mint a key and splice in a pod/listener/cell id (sealed
//     construction), and Store.Claim takes IdempotencyKey (not raw strings) so a
//     hand-built (ns,key) pair is inexpressible at the store boundary. The
//     require-isolation-tuple half (every dimension must FLOW into the key) stays
//     an AST taint walk on each constructor (Go cannot make body-flow type-Hard).
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
//     cannot express "a constructor's body consumes all its params into the key",
//     and the same-type string params could be transposed at the (single,
//     reviewed) callsite. The AST taint walk on each constructor (DeriveKey,
//     DeriveCommandKey) is the Medium backstop. Same ceiling family as
//     #851/#893/#1282/#1552. NOT typestate-upgradeable (a builder enforces
//     presence-of-setters, which positional params already give; it does not make
//     body-flow Hard).
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
//     function-typed var), exported OR unexported, → sole-producer diff;
//     reverting Store.Claim to raw strings → param-type freeze; both fail loud
//     via the visited-guard if the package path moves.
//   - β prong-1b/2 method blind spot: checkIdempotencyKeyMethods only scans
//     methods of IdempotencyKey itself (via reflect.PointerTo), and the go/types
//     sole-producer scan skips methods (Recv != nil) — so a method on ANOTHER
//     package type that returns IdempotencyKey (e.g. func (b keyBuilder) Build()
//     IdempotencyKey) is caught by neither. It is bounded by the
//     upstream-external Hard seal: such a method still cannot populate the
//     unexported fields except via the sanctioned constructors. Known in-package
//     Medium blind spot.
//   - β prong-3 taint walk blind spots: an extra constructor param (node-id
//     vector) → signature freeze; dropping any isolation dimension from the body,
//     INCLUDING a dummy read `_ = commandID` that references the input but never
//     flows it into the key → checkConstructorRequiredUse (taint flow, not
//     presence). The taint model is lightweight ("a param name appearing ⇒ its
//     value flows"); its soundness precondition is enforced by checkFlatComposition
//     (#1699 F1), which rejects the two laundering vectors that keep a param's name
//     while losing its value — a CALL/foreign SELECTOR (`key: launder(subject,
//     commandID)`) and a param REASSIGN (`commandID = ""; key: …`). Proven
//     non-vacuous by inline malformed DeriveKey AND DeriveCommandKey fixtures
//     (missing / dummy-read / helper-drop / param-rebind). RESIDUAL (NOT closed,
//     Medium ceiling gh #1650): param TRANSPOSITION (`key: commandID + subject`
//     swaps two like-typed params, both still flow). Note: flat-string params
//     remove the old *auth.Principal selector/alias blind spots entirely. Known
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
	"github.com/ghbvf/gocell/framework/runtime/http/idempotency"
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
	deriveCommandKeyFnName = "DeriveCommandKey"
	deriveKeyFile          = "key.go"
	storeIfaceName         = "Store"
	storeClaimMethodName   = "Claim"
)

// keyField identifies which field of the returned IdempotencyKey an isolation
// param must flow into: the ns field (tenant) or the key field (everything else).
type keyField uint8

const (
	fieldNS keyField = iota
	fieldKey
)

// paramSpec pins one positional string param of a key constructor to the
// IdempotencyKey field its value MUST reach (the taint sink).
type paramSpec struct {
	label string   // human label used in the violation message
	sink  keyField // ns or key
}

// constructorSpec is one sanctioned IdempotencyKey constructor: its top-level
// func name + the ordered isolation params (each pinned to a sink). The signature
// freeze + taint walk run identically over every spec; only the param count and
// the param→sink mapping differ.
type constructorSpec struct {
	fnName string
	params []paramSpec
}

// idempotencyKeyConstructors is the SINGLE source of sanctioned IdempotencyKey
// constructors. Both the sole-producer allowlist (deriveKeyProducerAllowed,
// derived below) AND the per-constructor signature-freeze + taint-walk
// (TestHTTPIdempotencyKeyNodeAgnostic01_RequiredUse) iterate this one table, so a
// new producer CANNOT be added to one gate while skipping the other: adding a row
// here grants all three checks at once (sole-producer + signature freeze + taint
// walk). Two separate lists would let a 3rd producer pass the sole-producer scan
// yet escape the signature/taint gate (a node-id extra param undetected) — the
// "two truth sources" hazard ai-robust.md bans. DeriveKey derives the HTTP record
// key from (tenant, subject, method, path, idemKey); DeriveCommandKey (#1669, the
// #1610 cross-cell same-slot mapping primitive) derives the command dedup key
// from (tenant, subject, command_id).
var idempotencyKeyConstructors = []constructorSpec{
	{
		fnName: deriveKeyFnName,
		params: []paramSpec{
			{"tenantID", fieldNS},
			{"subject", fieldKey},
			{"method", fieldKey},
			{"path", fieldKey},
			{"idemKey", fieldKey},
		},
	},
	{
		fnName: deriveCommandKeyFnName,
		params: []paramSpec{
			{"tenantID", fieldNS},
			{"subject", fieldKey},
			{"commandID", fieldKey},
		},
	},
}

// deriveKeyProducerAllowed is the exhaustive set of package-level surfaces in
// runtime/http/idempotency that may PRODUCE an IdempotencyKey value, derived
// single-source from idempotencyKeyConstructors. A producer not in this set (a
// FromStrings / a function-typed var / an Unmarshal) would launder an arbitrary
// (ns,key) — possibly carrying a node id — into a sealed key, defeating the seal.
var deriveKeyProducerAllowed = func() map[string]struct{} {
	m := make(map[string]struct{}, len(idempotencyKeyConstructors))
	for _, c := range idempotencyKeyConstructors {
		m[c.fnName] = struct{}{}
	}
	return m
}()

// idempotencyKeyForbiddenMethods are deserialization entries that would let an
// external package populate a sealed IdempotencyKey from bytes, bypassing the
// sanctioned constructors. They return error (not IdempotencyKey), so a
// returns-IdempotencyKey scan never catches them — banned by name (#1488 F4 lesson).
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
					"from bytes, bypassing the sanctioned constructors (idempotencyKeyConstructors).", idempotencyKeyTypeName, m.Name))
			continue
		}
		mt := m.Func.Type() // receiver is in[0]; method results are the outs
		for o := 0; o < mt.NumOut(); o++ {
			if mt.Out(o) == dt {
				v = append(v, fmt.Sprintf(
					"%s method %q returns a new %s (builder laundering) — only the sanctioned constructors (idempotencyKeyConstructors) may produce one.",
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
// (1) the sanctioned constructors {DeriveKey, DeriveCommandKey} as the ONLY package-level producers of an IdempotencyKey
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
		[]string{"./framework/runtime/http/idempotency/..."}),
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

			// (1) sole producers: package-level funcs AND function-typed vars whose
			// signature returns IdempotencyKey must be exactly the sanctioned set
			// {DeriveKey, DeriveCommandKey} (deriveKeyProducerAllowed, single-sourced
			// from idempotencyKeyConstructors).
			producers := idempotencyKeyProducerNames(scope, ikType)
			diags = append(diags, diffHTTPIdempotencyExpectedSet(
				"package-level surfaces producing IdempotencyKey",
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

// idempotencyKeyProducerNames returns package-level func / function-typed var
// names whose signature returns IdempotencyKey.
func idempotencyKeyProducerNames(scope *types.Scope, ikType types.Type) []string {
	var producers []string
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
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
	return producers
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
// a node id" inexpressible, but Go CANNOT make "a sanctioned constructor's body
// consumes all its isolation dimensions into the key" type-system-Hard. This AST
// taint walk over every constructor in idempotencyKeyConstructors (DeriveKey:
// tenantID→ns, subject/method/path/idemKey→key; DeriveCommandKey: tenantID→ns,
// subject/commandID→key) is the Medium backstop: each dimension must FLOW (not
// merely be referenced) into the returned IdempotencyKey literal; a dropped
// dimension, a `_ = commandID` dummy read, or a laundering call / param-rebind
// (rejected by checkFlatComposition) is caught. An extra parameter (a node-id
// injection vector) trips the signature freeze.
func TestHTTPIdempotencyKeyNodeAgnostic01_RequiredUse(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	path := filepath.Join(root, "framework", "runtime", "http", "idempotency", deriveKeyFile)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err, "parse %s", path)

	for _, spec := range idempotencyKeyConstructors {
		fn, ok := findTopLevelFuncDecl(file, spec.fnName)
		require.Truef(t, ok, "%s: %s not found in %s", ruleHTTPIdemKeyNodeAgnostic01, spec.fnName, deriveKeyFile)

		for _, vio := range checkConstructorSignature(spec, fn) {
			t.Errorf("%s (%s signature): %s. The constructor must take exactly %d string isolation params and "+
				"return IdempotencyKey — an extra param is the node-id injection vector. If intentional, update "+
				"ADR 202606051000-1449 + this gate in the same PR.",
				ruleHTTPIdemKeyNodeAgnostic01, spec.fnName, vio, len(spec.params))
		}
		for _, vio := range checkConstructorRequiredUse(spec, fn) {
			t.Errorf("%s (%s required-use): %s. Every isolation dimension MUST flow into the key (#1650 Medium "+
				"backstop); dropping one collapses cross-tenant/-subject/-endpoint isolation.",
				ruleHTTPIdemKeyNodeAgnostic01, spec.fnName, vio)
		}
	}
}

// checkConstructorSignature pins a key constructor to exactly len(spec.params)
// string params and a single IdempotencyKey result. Generalizes the former
// per-DeriveKey freeze over every constructor in idempotencyKeyConstructors.
func checkConstructorSignature(spec constructorSpec, fn *ast.FuncDecl) []string {
	var v []string
	want := len(spec.params)
	params := flattenFieldList(fn.Type.Params)
	if len(params) != want {
		v = append(v, fmt.Sprintf(
			"param count = %d, want exactly %d (a node/listener/cell param is the node-id injection vector)",
			len(params), want))
		return v // positional taint resolution below assumes the exact count
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

// checkConstructorRequiredUse taint-tracks each positional param of spec from its
// source to the ns/key field of the returned IdempotencyKey literal, per the
// spec's param→sink mapping. Flow (not mere presence) is tracked so a dummy read
// `_ = commandID` does not count. Generalizes the former DeriveKey-only walk; the
// taint fixpoint engine is unchanged — only the source map (one positional bit
// per param) and the final per-param sink check are spec-driven.
//
// Soundness precondition (#1699 F1): the fixpoint uses a deliberately lightweight
// "a param NAME appearing in a sink-reachable expression ⇒ its VALUE flows" model.
// That is sound ONLY if the body is a flat composition with no value laundering —
// which the constructors are by contract (their godoc: "the result is derived ONLY
// from its parameters", a NUL-joined concat). checkFlatComposition enforces that
// precondition structurally, rejecting the two laundering vectors that would keep a
// param's name while losing its value: (a) a CALL or foreign SELECTOR that could
// drop/transform the value (`key: launder(subject, commandID)`), and (b) REASSIGNING
// a param so its name later denotes a different value (`commandID = ""; key: …`).
// With those banned, "name appears ⇒ value flows" holds. Rating stays Medium (AST
// taint): the residual is param TRANSPOSITION (`key: commandID + subject` swaps two
// like-typed params, both still flow) — the documented gh #1650 ceiling, NOT closed
// here. The Hard form that would make a dropped/transposed dimension inexpressible is
// a genuine Go ceiling — see gh #1650.
func checkConstructorRequiredUse(spec constructorSpec, fn *ast.FuncDecl) []string {
	if fn.Body == nil {
		return []string{"missing body"}
	}
	params := flattenFieldList(fn.Type.Params)
	if len(params) != len(spec.params) {
		return nil // the signature freeze reports the shape
	}

	// Enforce the flat-composition precondition before trusting the taint walk; a
	// laundering body makes the lightweight model unsound, so report THAT instead.
	if v := checkFlatComposition(spec, params, fn.Body); len(v) != 0 {
		return v
	}

	// One unique token bit per positional param, keyed to its source name. Safe:
	// an isolation tuple has a handful of params (DeriveKey=5, DeriveCommandKey=3),
	// far below uint's bit width — `1 << i` cannot overflow. A constructor with
	// ≥63 params would break this and is absurd for an isolation tuple; if one ever
	// appears, switch the token set to math/big.
	src := map[uint]string{}
	for i, p := range params {
		src[uint(i)] = p.name
	}

	// Sinks are the ns/key fields of the returned IdempotencyKey composite
	// literal. The synthetic sink names cannot collide with Go identifiers.
	const nsSink, keySink = "$ns", "$key"
	nsExpr, keyExpr, found := returnedKeyFields(fn.Body)
	if !found {
		return []string{spec.fnName + " must end by returning an IdempotencyKey{ns: …, key: …} composite " +
			"literal (the taint sinks); a different construction form is a deliberate review checkpoint"}
	}

	taint := map[string]uint{}
	tokensOf := func(expr ast.Expr) uint {
		var bits uint
		EachInSubtree[ast.Ident](expr, func(id *ast.Ident) {
			for bit, name := range src {
				if name != "" && id.Name == name {
					bits |= 1 << bit
				}
			}
			bits |= taint[id.Name]
		})
		return bits
	}

	type edge struct {
		sink string
		rhs  ast.Expr
	}
	var edges []edge
	EachInSubtree[ast.AssignStmt](fn.Body, func(as *ast.AssignStmt) {
		if len(as.Lhs) != len(as.Rhs) {
			return
		}
		EachInChildren[ast.Ident](as, func(id *ast.Ident) {
			if id.Pos() > as.TokPos {
				return
			}
			for i, lhs := range as.Lhs {
				if lhs.Pos() == id.Pos() {
					edges = append(edges, edge{sink: id.Name, rhs: as.Rhs[i]})
					return
				}
			}
		})
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
	for i, p := range spec.params {
		bit := uint(1) << uint(i)
		sinkName, sinkLabel := keySink, "key"
		if p.sink == fieldNS {
			sinkName, sinkLabel = nsSink, "ns"
		}
		if taint[sinkName]&bit == 0 {
			v = append(v, fmt.Sprintf(
				"%s never flows into the %s field — isolation dropped (dummy read `_ = %s` does not count)",
				p.label, sinkLabel, p.label))
		}
	}
	return v
}

// checkFlatComposition is the soundness precondition for checkConstructorRequiredUse's
// lightweight taint model (#1699 F1): the constructor body must derive the key by
// flat composition over its params + the noTenantSentinel const, with NO value
// laundering. It rejects two vectors that keep a param's name while losing its value:
//
//   - a CallExpr or foreign SelectorExpr anywhere in the body — a helper/method can
//     drop or transform the value (`key: launder(subject, commandID)`) while the
//     param names still appear in the arg list, fooling the "name appears ⇒ flow"
//     model. The legit bodies contain neither (the derivation is pure concat).
//   - REASSIGNING a constructor param (`commandID = ""`) — rebinding the name to a
//     different value defeats the model (the legit bodies assign only `ns`, a local).
//
// `ns := tenantID` (a DEFINE of a new local) and `ns = noTenantSentinel` (assign to
// that local) are fine — they do not touch a param.
func checkFlatComposition(spec constructorSpec, params []flatParam, body *ast.BlockStmt) []string {
	paramNames := map[string]bool{}
	for _, p := range params {
		if p.name != "" {
			paramNames[p.name] = true
		}
	}
	var v []string
	EachInSubtree[ast.CallExpr](body, func(*ast.CallExpr) {
		v = append(v, spec.fnName+" body contains a call expression — a helper can launder/drop a "+
			"param's value while its name still appears, defeating the taint model. The derivation "+
			"must be a flat concatenation of the params (no calls).")
	})
	EachInSubtree[ast.SelectorExpr](body, func(*ast.SelectorExpr) {
		v = append(v, spec.fnName+" body contains a selector expression — a foreign field/method "+
			"access can launder a param's value. Use only the params + the noTenantSentinel const.")
	})
	EachInSubtree[ast.AssignStmt](body, func(as *ast.AssignStmt) {
		EachInChildren[ast.Ident](as, func(id *ast.Ident) {
			if id.Pos() > as.TokPos || !paramNames[id.Name] {
				return
			}
			v = append(v, fmt.Sprintf("%s reassigns param %q — rebinding a param name to a "+
				"different value defeats the taint model (the name keeps appearing but the original "+
				"value is lost).", spec.fnName, id.Name))
		})
	})
	return v
}

// returnedKeyFields extracts the ns/key field value expressions from the
// IdempotencyKey composite literal in the function's return statement.
func returnedKeyFields(body *ast.BlockStmt) (nsExpr, keyExpr ast.Expr, found bool) {
	_, found = FindFirstInSubtree[ast.ReturnStmt](body, func(ret *ast.ReturnStmt) bool {
		if len(ret.Results) != 1 {
			return false
		}
		cl, ok := ret.Results[0].(*ast.CompositeLit)
		if !ok {
			return false
		}
		if id, ok := cl.Type.(*ast.Ident); !ok || id.Name != idempotencyKeyTypeName {
			return false
		}
		EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				return
			}
			switch key.Name {
			case "ns":
				nsExpr = kv.Value
			case "key":
				keyExpr = kv.Value
			}
		})
		return nsExpr != nil && keyExpr != nil
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
	if got := reverseSoleProducerFixtureNames(); !containsString(got, "hiddenProducer") {
		t.Errorf("%s self-test: sole-producer detector missed unexported producer hiddenProducer (got %v)",
			ruleHTTPIdemKeyNodeAgnostic01, got)
	}

	// Taint reverse fixtures (inline source). For each sanctioned constructor the
	// conforming body yields zero; each malformed variant yields ≥1 from the
	// signature freeze or the taint walk. Resolve specs by name (not by index) so
	// reordering idempotencyKeyConstructors cannot silently pair a spec with the
	// wrong fixture family.
	keySpec := constructorSpecByName(t, deriveKeyFnName)
	cmdSpec := constructorSpecByName(t, deriveCommandKeyFnName)
	if sv, uv := runConstructorDetectors(t, keySpec, deriveKeyGood); len(sv) != 0 || len(uv) != 0 {
		t.Errorf("%s self-test: detectors flagged the conforming DeriveKey (vacuous-pass): sig=%v use=%v",
			ruleHTTPIdemKeyNodeAgnostic01, sv, uv)
	}
	for name, src := range deriveKeyBad {
		if sv, uv := runConstructorDetectors(t, keySpec, src); len(sv) == 0 && len(uv) == 0 {
			t.Errorf("%s self-test: detectors passed malformed DeriveKey %q (blind spot)",
				ruleHTTPIdemKeyNodeAgnostic01, name)
		}
	}
	if sv, uv := runConstructorDetectors(t, cmdSpec, deriveCommandKeyGood); len(sv) != 0 || len(uv) != 0 {
		t.Errorf("%s self-test: detectors flagged the conforming DeriveCommandKey (vacuous-pass): sig=%v use=%v",
			ruleHTTPIdemKeyNodeAgnostic01, sv, uv)
	}
	for name, src := range deriveCommandKeyBad {
		if sv, uv := runConstructorDetectors(t, cmdSpec, src); len(sv) == 0 && len(uv) == 0 {
			t.Errorf("%s self-test: detectors passed malformed DeriveCommandKey %q (blind spot)",
				ruleHTTPIdemKeyNodeAgnostic01, name)
		}
	}
}

// reverseSoleProducerFixtureNames builds a synthetic package scope with an
// unexported package-level producer. The production detector must include it:
// same-package helpers can populate IdempotencyKey's unexported fields, so
// exported-only scanning does not prove the sanctioned set is the only producer.
func reverseSoleProducerFixtureNames() []string {
	pkg := types.NewPackage("example.com/reverse/idem", "idem")
	scope := pkg.Scope()
	ikObj := types.NewTypeName(token.NoPos, pkg, idempotencyKeyTypeName, nil)
	ikType := types.NewNamed(ikObj, types.NewStruct(nil, nil), nil)
	scope.Insert(ikObj)

	result := types.NewTuple(types.NewVar(token.NoPos, pkg, "", ikType))
	producerSig := types.NewSignatureType(nil, nil, nil, types.NewTuple(), result, false)
	scope.Insert(types.NewFunc(token.NoPos, pkg, "hiddenProducer", producerSig))
	return idempotencyKeyProducerNames(scope, ikType)
}

func containsString(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
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
var deriveKeyBad = map[string]string{ //nolint:gochecknoglobals // intentional package-level RED-case fixture map
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
	// helper-drop: every param NAME appears (so a syntactic "name appears ⇒ flow"
	// model passes), but the value is laundered through a call that could drop it.
	// Caught by the no-CallExpr structural guard (#1699 F1).
	"helper-drop": `package p
func DeriveKey(tenantID, subject, method, path, idemKey string) IdempotencyKey {
	return IdempotencyKey{ns: tenantID, key: launder(subject, method, path, idemKey)}
}`,
	// param-rebind: a param is reassigned to a different value, then its NAME is used
	// in the key — the syntactic model sees the name and passes, but the original
	// value was killed. Caught by the no-param-reassign structural guard (#1699 F1).
	"param-rebind": `package p
func DeriveKey(tenantID, subject, method, path, idemKey string) IdempotencyKey {
	subject = ""
	return IdempotencyKey{ns: tenantID, key: subject + method + path + idemKey}
}`,
}

const deriveCommandKeyGood = `package p
func DeriveCommandKey(tenantID, subject, commandID string) IdempotencyKey {
	ns := tenantID
	if ns == "" {
		ns = noTenantSentinel
	}
	return IdempotencyKey{
		ns:  ns,
		key: subject + "\x00" + commandID,
	}
}`

// deriveCommandKeyBad: each fixture drops one isolation dimension (caught by the
// taint walk) or adds a node-id param (caught by the signature freeze). The
// dummy-read fixture references commandID but flows only subject into the key.
var deriveCommandKeyBad = map[string]string{ //nolint:gochecknoglobals // intentional package-level RED-case fixture map
	"node-id-param": `package p
func DeriveCommandKey(tenantID, subject, commandID, podID string) IdempotencyKey {
	return IdempotencyKey{ns: tenantID + podID, key: subject + commandID}
}`,
	"missing-tenant": `package p
func DeriveCommandKey(tenantID, subject, commandID string) IdempotencyKey {
	return IdempotencyKey{ns: noTenantSentinel, key: subject + commandID}
}`,
	"missing-subject": `package p
func DeriveCommandKey(tenantID, subject, commandID string) IdempotencyKey {
	return IdempotencyKey{ns: tenantID, key: commandID}
}`,
	"missing-commandid": `package p
func DeriveCommandKey(tenantID, subject, commandID string) IdempotencyKey {
	return IdempotencyKey{ns: tenantID, key: subject}
}`,
	"dummy-read-bypass": `package p
func DeriveCommandKey(tenantID, subject, commandID string) IdempotencyKey {
	_ = commandID
	return IdempotencyKey{ns: tenantID, key: subject}
}`,
	// helper-drop / param-rebind: same two laundering vectors as deriveKeyBad,
	// covering the 3-param constructor. Caught by the structural guards (#1699 F1).
	"helper-drop": `package p
func DeriveCommandKey(tenantID, subject, commandID string) IdempotencyKey {
	return IdempotencyKey{ns: tenantID, key: launder(subject, commandID)}
}`,
	"param-rebind": `package p
func DeriveCommandKey(tenantID, subject, commandID string) IdempotencyKey {
	commandID = ""
	return IdempotencyKey{ns: tenantID, key: subject + commandID}
}`,
}

// constructorSpecByName returns the sanctioned constructor spec with the given
// func name from idempotencyKeyConstructors, failing the test if absent. Used by
// the reverse self-check to pair a spec with its fixture family by name rather
// than by a fragile slice index.
func constructorSpecByName(t *testing.T, fnName string) constructorSpec {
	t.Helper()
	for _, c := range idempotencyKeyConstructors {
		if c.fnName == fnName {
			return c
		}
	}
	require.FailNowf(t, "constructor spec not found", "no constructorSpec %q in idempotencyKeyConstructors", fnName)
	return constructorSpec{}
}

// runConstructorDetectors parses an inline constructor source and runs the
// signature freeze + the required-use taint walk for the given spec.
func runConstructorDetectors(t *testing.T, spec constructorSpec, src string) (sig, use []string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "inline.go", src, 0)
	require.NoErrorf(t, err, "parse inline %s fixture", spec.fnName)
	fn, ok := findTopLevelFuncDecl(f, spec.fnName)
	require.Truef(t, ok, "inline fixture missing %s", spec.fnName)
	return checkConstructorSignature(spec, fn), checkConstructorRequiredUse(spec, fn)
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
