package authorizationdecide

// attributes_test.go — unit tests for attributeResolver.resolveResource and
// resolveSubject, specifically the resource.id identity key and subject UUID
// canonicalization added in #1977 Batch B and the F2 review fix.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TestAttributeResolver_ResourceID pins the resource.id identity key behavior:
//   - non-empty resourceID → resolve(SourceResource, "id") returns ([id], true).
//   - empty resourceID → resolve(SourceResource, "id") returns (nil/[], false).
//   - a non-"id" resource key still resolves from the resourceAttrs map.
func TestAttributeResolver_ResourceID(t *testing.T) {
	const resourceID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

	t.Run("non-empty resourceID → found=true", func(t *testing.T) {
		r := attributeResolver{resourceID: resourceID}
		vals, found := r.resolve(abac.SourceResource, "id")
		assert.True(t, found, "resolve(SourceResource, 'id') must return found=true for non-empty resourceID")
		assert.Equal(t, []string{resourceID}, vals)
	})

	t.Run("empty resourceID → found=false", func(t *testing.T) {
		r := attributeResolver{resourceID: ""}
		vals, found := r.resolve(abac.SourceResource, "id")
		assert.False(t, found, "resolve(SourceResource, 'id') must return found=false for empty resourceID")
		assert.Empty(t, vals)
	})

	t.Run("non-id resource key resolves from resourceAttrs map", func(t *testing.T) {
		r := attributeResolver{
			resourceID:    resourceID,
			resourceAttrs: map[string][]string{"owner": {"other-owner-id"}},
		}
		vals, found := r.resolve(abac.SourceResource, "owner")
		assert.True(t, found, "non-id key 'owner' must be resolved from resourceAttrs map")
		assert.Equal(t, []string{"other-owner-id"}, vals)
	})

	t.Run("non-id resource key absent from map → found=false", func(t *testing.T) {
		r := attributeResolver{resourceID: resourceID, resourceAttrs: nil}
		vals, found := r.resolve(abac.SourceResource, "owner")
		assert.False(t, found, "absent key in resourceAttrs must return found=false")
		assert.Empty(t, vals)
	})

	t.Run("id key takes priority over resourceAttrs entry", func(t *testing.T) {
		// Even if resourceAttrs has an "id" key, the resourceID field wins.
		r := attributeResolver{
			resourceID:    resourceID,
			resourceAttrs: map[string][]string{"id": {"should-be-ignored"}},
		}
		vals, found := r.resolve(abac.SourceResource, "id")
		assert.True(t, found)
		assert.Equal(t, []string{resourceID}, vals,
			"resourceID field must take priority over resourceAttrs['id']")
	})
}

// TestAttributeResolver_SubjectCanonicalization pins the F2 fix: the "sub"
// attribute canonicalizes the principal's Subject UUID before returning, so
// `subject.sub == resource.id` comparisons are robust to UUID case/format.
// Non-UUID subjects (plain strings) pass through unchanged.
func TestAttributeResolver_SubjectCanonicalization(t *testing.T) {
	const canonicalUUID = "11111111-1111-1111-1111-111111111111"

	t.Run("non-canonical-cased UUID resolves to canonical form", func(t *testing.T) {
		upperSubject := strings.ToUpper(canonicalUUID)
		r := attributeResolver{
			principal: &auth.Principal{Kind: auth.PrincipalUser, Subject: upperSubject},
		}
		vals, found := r.resolve(abac.SourceSubject, "sub")
		assert.True(t, found)
		assert.Equal(t, []string{canonicalUUID}, vals,
			"UPPERCASE UUID subject must be canonicalized to lowercase")
	})

	t.Run("already-canonical UUID passes through as-is", func(t *testing.T) {
		r := attributeResolver{
			principal: &auth.Principal{Kind: auth.PrincipalUser, Subject: canonicalUUID},
		}
		vals, found := r.resolve(abac.SourceSubject, "sub")
		assert.True(t, found)
		assert.Equal(t, []string{canonicalUUID}, vals)
	})

	t.Run("non-UUID subject passes through unchanged", func(t *testing.T) {
		// Service accounts or custom subjects that are not UUIDs must not be
		// mangled — they pass through as-is (httputil.ParseCanonicalUUID returns
		// ok=false for non-UUID strings, so the fallback path is taken).
		r := attributeResolver{
			principal: &auth.Principal{Kind: auth.PrincipalUser, Subject: "u"},
		}
		vals, found := r.resolve(abac.SourceSubject, "sub")
		assert.True(t, found)
		assert.Equal(t, []string{"u"}, vals,
			"non-UUID subject must pass through unchanged")
	})

	t.Run("empty subject → found=false", func(t *testing.T) {
		r := attributeResolver{
			principal: &auth.Principal{Kind: auth.PrincipalUser, Subject: ""},
		}
		vals, found := r.resolve(abac.SourceSubject, "sub")
		assert.False(t, found)
		assert.Empty(t, vals)
	})

	t.Run("subject key alias resolves the same way", func(t *testing.T) {
		upperSubject := strings.ToUpper(canonicalUUID)
		r := attributeResolver{
			principal: &auth.Principal{Kind: auth.PrincipalUser, Subject: upperSubject},
		}
		vals, found := r.resolve(abac.SourceSubject, "subject")
		assert.True(t, found)
		assert.Equal(t, []string{canonicalUUID}, vals,
			"'subject' alias must also canonicalize UUID")
	})
}
