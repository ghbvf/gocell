//go:build archtest

// INVARIANT: PRINCIPAL-SEALED-FIELD-FROZEN-01
//
// This file owns ONE invariant: the kernel/outbox.PrincipalMetadata schema is
// frozen at the field-set + JSON-tag + field-type level. It is the sealed
// sibling of the ObservabilityMetadata lock and the audit-side
// AUDIT-HASH-INPUT-FROZEN-01 (whose godoc names this file as its PR-A2
// sibling). The companion lock SAFEID-WIREMESSAGE-USAGE-01 already walks
// PrincipalMetadata (via the safeIDExemptFields "PrincipalMetadata: {}"
// registration) and asserts every exported field is idutil.SafeID-typed; this
// file adds the orthogonal axes that SafeID typing does not cover:
//
//  1. Exact field-name set: ActorID / SubjectID / TenantID / SessionID. A
//     rename (ActorID → Actor) keeps SafeID typing but silently rewires the
//     wire contract for JSON producers/consumers. Reflect over the production
//     type and assert NumField + the exact name set.
//  2. JSON tag stability: actorId / subjectId / tenantId / sessionId. Tag
//     drift (e.g. actorId → actor_id) is a wire-compat break invisible to the
//     compiler and to the SafeID type funnel.
//  3. Field type identity: each field is exactly idutil.SafeID (not a bare
//     string or a look-alike newtype) — redundant with SAFEID but kept here so
//     this lock is self-contained if the SAFEID carve-out is ever refactored.
//
// AI-robust rating (per .claude/rules/gocell/ai-robust.md §Hard 范本目录
// "reflect 字段冻结"):
//
//   - Hard (downstream): the reflect lock leaves no field-name / tag / type
//     drift undetected at PR time; combined with SAFEID-WIREMESSAGE-USAGE-01
//     (SafeID typing) the AST-expressible drift surface is closed.
//   - Hard (upstream): wireMessage is unexported and embeds PrincipalMetadata;
//     SAFEID-UPSTREAM-FUNNEL-HARD-01 forbids any re-export, so package-external
//     code cannot construct an envelope that bypasses MarshalEnvelope's
//     Principal.Validate fail-fast. PrincipalMetadata is exported (so consumers
//     can read entry.Principal()) but exposes no setters and is only populated
//     inside kernel/outbox via NewEntry's ctx injection.
//
// Tool blind spots (per AI-robust §载体决策原则 "强制盲区自检"):
//
//   - reflect.TypeOf does not see method bodies: a Validate that returns nil
//     unconditionally would still pass. Method BEHAVIOR (IsZero / Validate /
//     RestoreToContext / ContextPrincipal / the unexported
//     (*Entry).injectPrincipalFromContext funnel) is covered by
//     kernel/outbox/principal_test.go + the NewEntry construction tests, not
//     here — this lock is the SCHEMA freeze only.
//   - reflect cannot observe the unexported injector method, by design: NewEntry
//     is the sole injection trust boundary (issue #1229 removed any exported
//     producer-facing inject API), so there is intentionally no exported method
//     symbol for this lock to pin.
//   - StructTag.Get("json") returns "" for a malformed tag; the expectation set
//     lists every canonical tag explicitly, so "" never matches a real entry,
//     and a tag-value drift is caught by the exact compare.
package archtest

import (
	"reflect"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// principalCanonicalField is one frozen (name, jsonTag) pair on PrincipalMetadata.
type principalCanonicalField struct {
	name    string
	jsonTag string
}

// principalCanonicalFields is the frozen schema of outbox.PrincipalMetadata.
// Adding / removing / renaming a field, or changing a JSON tag, requires
// editing this list under explicit reviewer attention (deny-by-default).
var principalCanonicalFields = []principalCanonicalField{
	{"ActorID", "actorId,omitempty"},
	{"SubjectID", "subjectId,omitempty"},
	{"TenantID", "tenantId,omitempty"},
	{"SessionID", "sessionId,omitempty"},
}

// TestPrincipalSealedFieldFrozen01 reflectively freezes the PrincipalMetadata
// field-name set, JSON tags, and per-field idutil.SafeID type.
func TestPrincipalSealedFieldFrozen01(t *testing.T) {
	t.Parallel()

	pt := reflect.TypeOf(outbox.PrincipalMetadata{})
	if pt.Kind() != reflect.Struct {
		t.Fatalf("PRINCIPAL-SEALED-FIELD-FROZEN-01: outbox.PrincipalMetadata is not a struct (kind=%s)", pt.Kind())
	}

	if got, want := pt.NumField(), len(principalCanonicalFields); got != want {
		t.Fatalf("PRINCIPAL-SEALED-FIELD-FROZEN-01: PrincipalMetadata has %d fields, frozen set has %d. "+
			"Adding/removing a principal field rewires the wire envelope — update principalCanonicalFields "+
			"and the wire/audit HMAC contract under reviewer attention.", got, want)
	}

	safeIDType := reflect.TypeOf(idutil.SafeID(""))
	for i, want := range principalCanonicalFields {
		f := pt.Field(i)
		if f.Name != want.name {
			t.Errorf("PRINCIPAL-SEALED-FIELD-FROZEN-01: field[%d] name = %q, want %q (field rename breaks wire contract)",
				i, f.Name, want.name)
		}
		if tag := f.Tag.Get("json"); tag != want.jsonTag {
			t.Errorf("PRINCIPAL-SEALED-FIELD-FROZEN-01: field %q json tag = %q, want %q (tag drift breaks wire compat)",
				f.Name, tag, want.jsonTag)
		}
		if f.Type != safeIDType {
			t.Errorf("PRINCIPAL-SEALED-FIELD-FROZEN-01: field %q type = %s, want idutil.SafeID "+
				"(CWE-117 fail-closed UnmarshalJSON requires SafeID)", f.Name, f.Type)
		}
	}
}

// TestPrincipalSealedFieldFrozen01_NegativeControl proves the reflect lock
// fires when a synthetic struct drifts from the frozen schema (blind-spot
// self-check: confirms the comparison is not vacuously passing).
func TestPrincipalSealedFieldFrozen01_NegativeControl(t *testing.T) {
	t.Parallel()

	// A drifted shape: field renamed + tag changed + bare string type.
	type driftedPrincipal struct {
		Actor     string        `json:"actor_id"`
		SubjectID idutil.SafeID `json:"subjectId,omitempty"`
		TenantID  idutil.SafeID `json:"tenantId,omitempty"`
		SessionID idutil.SafeID `json:"sessionId,omitempty"`
	}
	dt := reflect.TypeOf(driftedPrincipal{})
	safeIDType := reflect.TypeOf(idutil.SafeID(""))

	var violations int
	for i, want := range principalCanonicalFields {
		f := dt.Field(i)
		if f.Name != want.name || f.Tag.Get("json") != want.jsonTag || f.Type != safeIDType {
			violations++
		}
	}
	if violations == 0 {
		t.Fatal("PRINCIPAL-SEALED-FIELD-FROZEN-01 negative control: drifted struct produced 0 violations — " +
			"the reflect comparison is vacuous and would not catch a real schema drift")
	}
}
