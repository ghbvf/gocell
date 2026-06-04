// client_ip_hash_funnel_test.go — closes the SINK side of the client-IP PII
// funnel (#1488): the producer DTO physically cannot carry a plaintext IP.
//
//   - INVARIANT: CLIENT-IP-HASH-FUNNEL-01
//
// # What this guards
//
// #1488 replaced the plaintext clientIp in event.auth.bootstrap-failed.v1 with a
// keyed, non-reversible hash so PII never crosses the replayable outbox/broker/
// DLX boundary. The Hard mechanism is a sealed type: redaction.IPHash is a struct
// with a single unexported field, constructible only via redaction.HashIP, and
// the producer DTO field is that sealed type — so assigning a raw string is a
// compile error. This archtest freezes both halves of that mechanism against
// in-package drift that the Go type system allows but which would break the seal.
//
// Two prongs (both go/types, NOT reflect — the producer DTO lives in
// cells/accesscore/internal/dto, which tools/archtest cannot import):
//
//   - Prong A (pkg/redaction) — IPHash shape + sole-constructor freeze:
//     IPHash must be a struct with exactly one field, named "v", of type string,
//     unexported (the seal). AND the only producer of an IPHash value may be the
//     package func HashIP — no other package func and no IPHash method may return
//     an IPHash (a re-export / builder / FromString would launder a plaintext
//     into a populated IPHash, defeating the seal; charter "sealed construction —
//     任意名 re-export 是闭环必查点").
//
//   - Prong B (cells/accesscore/internal/dto) — DTO field type freeze:
//     BootstrapAuthFailedEvent.ClientIPHash must be exactly redaction.IPHash.
//     Reverting it to string (or any other type) re-opens plaintext assignment
//     and fails CI.
//
// # AI-robust rating (charter §"Hard 范本目录")
//
// "sealed construction" + "reflect schema freeze" templates, realized through
// go/types (the field set, field visibility, type identity, and result-type
// identity are objective structural facts, not string anchors). Downstream Hard:
// the producer cannot assign a plaintext string to the sealed field (compile
// error), and this archtest freezes the field set / constructor set / DTO field
// type so any drift is a CI failure. Upstream Hard: the unexported field makes a
// populated IPHash unconstructable outside pkg/redaction.
//
// Coverage note: this funnel covers the ONE client-IP PII field that exists in
// any replayable payload today (verified: the other 10 replayable event payloads
// carry only IDs/usernames). A generic "scan every payload schema for PII field
// names" rule would be Soft (string-name anchor) and is rejected per charter;
// future PII fields plug into the same sealed-type + HashX pattern (ADR
// docs/architecture/...-1488-adr-replayable-payload-pii-hash-funnel.md). The
// generic Hard mechanism (codegen-derived typed PII field) is tracked at gh #1605.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - A bare-string laundering via json.Unmarshal is structurally avoided:
//     IPHash deliberately has no UnmarshalJSON, so it is not a decode target;
//     consumers read the hash as a plain string in their own DTOs. Not scanned
//     here (no constructor surface), guaranteed by absence + the no-UnmarshalJSON
//     freeze is covered by the method-result scan (no method returns IPHash).
//   - The anti-vacuity guard (both target packages must be visited) forbids the
//     rule silently passing if a package path is renamed/moved.
//   - The constructor-set scan covers package funcs AND IPHash methods. Method
//     coverage uses named.NumMethods(), which in go/types enumerates every method
//     declared with the named type as receiver — BOTH value (func (h IPHash)) and
//     pointer (func (h *IPHash)) receivers — so a pointer-receiver IPHash-returning
//     method is NOT a blind spot.
package archtest

import (
	"fmt"
	"go/types"
	"testing"
)

// redactionPkgPath is declared in health_verbose_invariants_test.go (same
// package); reused here for the IPHash freeze. accessDTOPkgPath is anchored to
// PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01: no bare module literals).
const (
	accessDTOPkgPath  = PlatformModulePath + "/cells/accesscore/internal/dto"
	ipHashTypeName    = "IPHash"
	ipHashCtorName    = "HashIP"
	bootstrapEventDTO = "BootstrapAuthFailedEvent"
	clientIPHashField = "ClientIPHash"
)

// TestClientIPHashFunnel01 freezes the sealed IPHash type (shape + sole
// constructor) and the producer DTO field type.
func TestClientIPHashFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	visited := map[string]bool{}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg == nil {
			return nil
		}
		switch p.Pkg.Path() {
		case redactionPkgPath:
			visited["redaction"] = true
			return checkIPHashSealed(p)
		case accessDTOPkgPath:
			visited["dto"] = true
			return checkBootstrapDTOFieldType(p)
		}
		return nil
	})

	if !visited["redaction"] {
		diags = append(diags, Diagnostic{Message: "CLIENT-IP-HASH-FUNNEL-01: pkg/redaction not scanned — " +
			"the sealed IPHash freeze (prong A) did not run; package path may have moved."})
	}
	if !visited["dto"] {
		diags = append(diags, Diagnostic{Message: "CLIENT-IP-HASH-FUNNEL-01: cells/accesscore/internal/dto not " +
			"scanned — the DTO field-type freeze (prong B) did not run; package path may have moved."})
	}

	Report(t, "CLIENT-IP-HASH-FUNNEL-01", diags)
}

// checkIPHashSealed freezes redaction.IPHash: single unexported string field "v",
// and HashIP as the sole producer of an IPHash value.
func checkIPHashSealed(p *Pass) []Diagnostic {
	obj := p.Pkg.Scope().Lookup(ipHashTypeName)
	if obj == nil {
		return []Diagnostic{{Message: "CLIENT-IP-HASH-FUNNEL-01: pkg/redaction must declare type IPHash (the sealed client-IP hash)."}}
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return []Diagnostic{{Message: "CLIENT-IP-HASH-FUNNEL-01: redaction.IPHash must be a named struct type."}}
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return []Diagnostic{{Message: "CLIENT-IP-HASH-FUNNEL-01: redaction.IPHash must be a struct (sealed construction)."}}
	}

	var d []Diagnostic
	if st.NumFields() != 1 {
		d = append(d, Diagnostic{Message: fmt.Sprintf(
			"CLIENT-IP-HASH-FUNNEL-01: redaction.IPHash must have exactly 1 field (the sealed digest), got %d. "+
				"Extra/renamed fields can break the seal or the wire encoding.", st.NumFields())})
	} else {
		f := st.Field(0)
		if f.Name() != "v" {
			d = append(d, Diagnostic{Message: fmt.Sprintf(
				"CLIENT-IP-HASH-FUNNEL-01: redaction.IPHash field must be named \"v\", got %q.", f.Name())})
		}
		if f.Exported() {
			d = append(d, Diagnostic{Message: "CLIENT-IP-HASH-FUNNEL-01: redaction.IPHash field must be UNEXPORTED — " +
				"an exported field lets any package construct a populated IPHash (plaintext laundering)."})
		}
		if b, ok := f.Type().(*types.Basic); !ok || b.Kind() != types.String {
			d = append(d, Diagnostic{Message: fmt.Sprintf(
				"CLIENT-IP-HASH-FUNNEL-01: redaction.IPHash field must be of type string, got %s.", f.Type())})
		}
	}

	// Sole-constructor freeze: only HashIP may produce an IPHash value.
	for _, name := range p.Pkg.Scope().Names() {
		fn, ok := p.Pkg.Scope().Lookup(name).(*types.Func)
		if !ok {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig.Recv() != nil {
			continue
		}
		if signatureReturnsNamed(sig, named) && fn.Name() != ipHashCtorName {
			d = append(d, Diagnostic{Message: fmt.Sprintf(
				"CLIENT-IP-HASH-FUNNEL-01: package func %q returns redaction.IPHash, but HashIP must be the SOLE "+
					"constructor. A second producer (re-export / FromString / builder) can launder a plaintext "+
					"value into a populated IPHash, defeating the seal.", fn.Name())})
		}
	}
	for i := 0; i < named.NumMethods(); i++ {
		m := named.Method(i)
		sig, ok := m.Type().(*types.Signature)
		if !ok {
			continue
		}
		if signatureReturnsNamed(sig, named) {
			d = append(d, Diagnostic{Message: fmt.Sprintf(
				"CLIENT-IP-HASH-FUNNEL-01: IPHash method %q returns redaction.IPHash — methods must not produce a "+
					"new IPHash (builder laundering). HashIP is the sole constructor.", m.Name())})
		}
	}
	return d
}

// checkBootstrapDTOFieldType freezes BootstrapAuthFailedEvent.ClientIPHash to be
// exactly redaction.IPHash.
func checkBootstrapDTOFieldType(p *Pass) []Diagnostic {
	obj := p.Pkg.Scope().Lookup(bootstrapEventDTO)
	if obj == nil {
		return []Diagnostic{{Message: "CLIENT-IP-HASH-FUNNEL-01: cells/accesscore/internal/dto must declare " +
			bootstrapEventDTO + " (the bootstrap-failed event payload)."}}
	}
	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return []Diagnostic{{Message: "CLIENT-IP-HASH-FUNNEL-01: " + bootstrapEventDTO + " must be a struct."}}
	}
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if f.Name() != clientIPHashField {
			continue
		}
		named, ok := f.Type().(*types.Named)
		if !ok || named.Obj().Pkg() == nil ||
			named.Obj().Pkg().Path() != redactionPkgPath || named.Obj().Name() != ipHashTypeName {
			return []Diagnostic{{Message: fmt.Sprintf(
				"CLIENT-IP-HASH-FUNNEL-01: %s.%s must be of type redaction.IPHash (the sealed hash), got %s. "+
					"Reverting to string re-opens plaintext-IP assignment into the replayable payload (#1488).",
				bootstrapEventDTO, clientIPHashField, f.Type())}}
		}
		return nil
	}
	return []Diagnostic{{Message: fmt.Sprintf(
		"CLIENT-IP-HASH-FUNNEL-01: %s must declare field %s of type redaction.IPHash.",
		bootstrapEventDTO, clientIPHashField)}}
}

// signatureReturnsNamed reports whether any result of sig is the named type
// (by type identity).
func signatureReturnsNamed(sig *types.Signature, named *types.Named) bool {
	res := sig.Results()
	for i := 0; i < res.Len(); i++ {
		if types.Identical(res.At(i).Type(), named) {
			return true
		}
	}
	return false
}
