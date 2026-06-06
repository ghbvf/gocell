// INVARIANT: SESSION-CACHE-EPOCH-NOT-CACHED-01
//
// This file freezes the field set of [sessionCacheEntry], the on-wire Redis
// shape used by [CachingSessionStore]. The invariant it protects is:
//
//	The live users.authz_epoch is NEVER cached in Redis.
//
// [CachingSessionStore.RevokeForSubject] delegates to inner with no cache
// operation.  Its fail-closed security floor depends on sessionvalidate
// comparing the LIVE PG users.authz_epoch against the cached
// AuthzEpochAtIssue snapshot (mismatch → 401).  If the live epoch were ever
// added to sessionCacheEntry, that comparison would use a stale value, silently
// breaking the revocation guarantee.
//
// # Two test functions
//
//   - [TestSessionCacheEntryFieldsFrozen01]: reflect-schema freeze — pins
//     NumField == 5 and an exact ordered tuple of (name, json-tag, Go-type)
//     for all five fields.  Any drift in field count, name, tag, or type fails
//     the comparison, forcing the change to land on an explicit frozen-list
//     review checkpoint.
//
//   - [TestSessionCacheEntryNoLiveAuthzEpoch01]: security-specific defense-in-depth
//     guard with non-vacuity — asserts that NO field's name equals exactly
//     "authzepoch" (case-insensitive), and no field's JSON key (stripped of
//     modifiers) is exactly "authzepoch" or "authz_epoch".  These exact-equality
//     checks catch the specific bare-AuthzEpoch / authz_epoch mistake; the
//     primary gate for ANY new field (including name variants) is the
//     NumField()==5 freeze in [TestSessionCacheEntryFieldsFrozen01].  Positive
//     control (non-vacuity): asserts that AuthzEpochAtIssue IS present, so the
//     test fails loudly if the snapshot field is renamed — proving the scan is live.
//
// # AI-robust grade: Hard
//
// Per .claude/rules/gocell/ai-robust.md §Hard 范本目录 "reflect schema freeze":
//
//	Hard comes from the fact that reflect enumerates field count, per-field
//	name, json-tag, and Go type as objective runtime structural facts — not
//	string anchors, not comments.  Any drift (add / rename / retype / reorder)
//	necessarily fails the tuple comparison, forcing the change to land on an
//	explicit frozen-list review checkpoint.
//
// Note: issue #1616 initially estimated "Medium", but per the ai-robust
// "reflect schema freeze" template this is Hard.  We grade it Hard.
//
// # Blind-spot self-check
//
// reflect.TypeOf enumerates ALL fields (including unexported fields), so there
// is no AST blind spot for field-set drift.  The one thing structural freeze
// alone cannot catch is a *semantic* rename that keeps the same type and tag
// while introducing a field that does carry live-epoch data under a different
// name.  [TestSessionCacheEntryNoLiveAuthzEpoch01] is the defense-in-depth
// guard: its exact-equality checks catch the specific bare-AuthzEpoch /
// authz_epoch mistake.  The PRIMARY protection against ANY new field
// (including variants such as AuthzEpochLive or AuthzEpochCurrent) is the
// NumField()==5 + ordered-tuple freeze in [TestSessionCacheEntryFieldsFrozen01]:
// adding any field — regardless of name — forces an explicit update to the
// frozen list under reviewer attention.
package redis

import (
	"reflect"
	"strings"
	"testing"
)

// sessionCacheEntryField is one frozen (name, jsonTag, goType) triple for
// a field of [sessionCacheEntry].  Reordering the slice changes the
// semantic reading of the on-wire JSON — encoding/json emits struct fields in
// source-declaration order — so the ORDER of this slice is part of the lock.
type sessionCacheEntryField struct {
	Name    string
	JSONTag string
	GoType  string
}

// expectedSessionCacheEntryFields is the authoritative 5-field frozen schema
// for sessionCacheEntry.  Changing any entry requires updating this list under
// explicit reviewer attention AND ensuring that the live users.authz_epoch is
// still not being cached (which would break the RevokeForSubject fail-closed
// security floor described in the file-level godoc).
//
// TenantID (#1337 PR-3b) is the fifth field: it is an authentication-decision
// carrier (the RLS tenant scope source) that sessionvalidate/sessionrefresh read
// off the cached view, so it MUST be part of the cached projection — a cache HIT
// that dropped it would scope the downstream read to a zero tenant.
var expectedSessionCacheEntryFields = []sessionCacheEntryField{
	{Name: "ID", JSONTag: "id", GoType: "string"},
	{Name: "SubjectID", JSONTag: "subjectId", GoType: "string"},
	{Name: "TenantID", JSONTag: "tenantId", GoType: "tenant.TenantID"},
	{Name: "RevokedAt", JSONTag: "revokedAt,omitempty", GoType: "*time.Time"},
	{Name: "AuthzEpochAtIssue", JSONTag: "authzEpochAtIssue", GoType: "int64"},
}

// TestSessionCacheEntryFieldsFrozen01 reflects over [sessionCacheEntry] and
// asserts that the field set has not drifted from [expectedSessionCacheEntryFields].
//
// The two assertions are:
//  1. NumField() == 5: adding a 6th field re-opens the live-epoch-not-cached
//     invariant (the new field might carry the live epoch); update this frozen
//     list AND the godoc / issue #1616 deliberately.
//  2. Exact ordered tuple comparison: name, json-tag, and Go-type for each
//     field in source-declaration order.
func TestSessionCacheEntryFieldsFrozen01(t *testing.T) {
	t.Parallel()

	st := reflect.TypeOf(sessionCacheEntry{})
	if st.Kind() != reflect.Struct {
		t.Fatalf("SESSION-CACHE-EPOCH-NOT-CACHED-01: sessionCacheEntry is not a struct (kind=%s)", st.Kind())
	}

	if got, want := st.NumField(), len(expectedSessionCacheEntryFields); got != want {
		t.Fatalf(
			"SESSION-CACHE-EPOCH-NOT-CACHED-01: sessionCacheEntry has %d fields, frozen set has %d.\n"+
				"Adding a field re-opens the live-epoch-not-cached security invariant.\n"+
				"If intentional, update expectedSessionCacheEntryFields + the file-level godoc deliberately.",
			got, want,
		)
	}

	var actual []sessionCacheEntryField
	for i := range st.NumField() {
		f := st.Field(i)
		actual = append(actual, sessionCacheEntryField{
			Name:    f.Name,
			JSONTag: f.Tag.Get("json"),
			GoType:  f.Type.String(),
		})
	}

	if !reflect.DeepEqual(actual, expectedSessionCacheEntryFields) {
		t.Errorf(
			"SESSION-CACHE-EPOCH-NOT-CACHED-01: sessionCacheEntry schema drifted.\n"+
				"  got  %+v\n"+
				"  want %+v\n"+
				"Drift in name / json-tag / type breaks the on-wire Redis schema.\n"+
				"Update expectedSessionCacheEntryFields deliberately.",
			actual, expectedSessionCacheEntryFields,
		)
	}
}

// TestSessionCacheEntryNoLiveAuthzEpoch01 is the security-specific guard with
// non-vacuity proof.
//
// It asserts:
//   - NO field has a name equal (case-insensitive) to "AuthzEpoch".
//   - NO field has a JSON tag key (stripped of modifiers) that is "authzepoch"
//     or "authz_epoch".
//
// These two assertions detect any attempt to add a field that carries the live
// users.authz_epoch (the prohibited form) rather than the at-issue snapshot.
//
// Positive control (non-vacuity): asserts that a field named AuthzEpochAtIssue
// with json tag "authzEpochAtIssue" IS present, so the test fails loudly if
// the snapshot field is renamed/removed, proving the scan is live and not
// passing vacuously.
func TestSessionCacheEntryNoLiveAuthzEpoch01(t *testing.T) {
	t.Parallel()

	st := reflect.TypeOf(sessionCacheEntry{})

	var foundSnapshot bool
	for i := range st.NumField() {
		f := st.Field(i)
		nameLower := strings.ToLower(f.Name)

		// Detect the bare "AuthzEpoch" field name (exact equality, case-insensitive).
		// Defense-in-depth for that specific mistake; primary gate for all name variants
		// is the NumField()+tuple freeze in TestSessionCacheEntryFieldsFrozen01.
		if nameLower == "authzepoch" {
			t.Errorf(
				"SESSION-CACHE-EPOCH-NOT-CACHED-01: field %q has name equal to 'authzepoch' (case-insensitive).\n"+
					"The live users.authz_epoch MUST NOT be cached — only AuthzEpochAtIssue is permitted.\n"+
					"Caching the live epoch breaks the RevokeForSubject fail-closed security floor.",
				f.Name,
			)
		}

		// Detect the exact JSON tag keys "authzepoch" and "authz_epoch" (exact equality).
		// Exact equality avoids false-positives on the legitimate "authzEpochAtIssue" key.
		rawTag := f.Tag.Get("json")
		jsonKey := strings.ToLower(strings.SplitN(rawTag, ",", 2)[0])
		if jsonKey == "authzepoch" || jsonKey == "authz_epoch" {
			t.Errorf(
				"SESSION-CACHE-EPOCH-NOT-CACHED-01: field %q has json key %q which resembles the live authz_epoch.\n"+
					"Only 'authzEpochAtIssue' (the at-issue snapshot) is permitted in sessionCacheEntry.",
				f.Name, jsonKey,
			)
		}

		// Positive control: the snapshot field must still be present.
		if f.Name == "AuthzEpochAtIssue" && rawTag == "authzEpochAtIssue" {
			foundSnapshot = true
		}
	}

	if !foundSnapshot {
		t.Error(
			"SESSION-CACHE-EPOCH-NOT-CACHED-01 non-vacuity: field AuthzEpochAtIssue with json tag " +
				"'authzEpochAtIssue' not found in sessionCacheEntry.\n" +
				"The at-issue snapshot field has been renamed or removed — update the frozen list AND " +
				"ensure sessionvalidate still has a snapshot field to compare against the live PG epoch.",
		)
	}
}
