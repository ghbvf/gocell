package authorizationdecide

// attributes_test.go — unit tests for attributeResolver.resolveResource,
// specifically the resource.id identity key added in #1977 Batch B.

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
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
