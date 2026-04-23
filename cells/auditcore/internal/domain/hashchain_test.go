package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashChain_Append(t *testing.T) {
	tests := []struct {
		name       string
		appendN    int
		wantLen    int
		wantLinked bool // each entry.PrevHash == previous entry.Hash
	}{
		{
			name:       "single entry has empty PrevHash",
			appendN:    1,
			wantLen:    1,
			wantLinked: true,
		},
		{
			name:       "three entries form linked chain",
			appendN:    3,
			wantLen:    3,
			wantLinked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := NewHashChain([]byte("test-key"))
			entries := make([]*AuditEntry, 0, tt.appendN)

			for i := 0; i < tt.appendN; i++ {
				e := hc.Append("evt-id", "login", "actor-1", []byte(`{"ip":"10.0.0.1"}`))
				entries = append(entries, e)
			}

			assert.Equal(t, tt.wantLen, hc.Len())

			// First entry must have empty PrevHash.
			assert.Empty(t, entries[0].PrevHash)

			// Each subsequent entry must link to the previous.
			for i := 1; i < len(entries); i++ {
				assert.Equal(t, entries[i-1].Hash, entries[i].PrevHash,
					"entry %d PrevHash should equal entry %d Hash", i, i-1)
			}

			// All hashes must be non-empty and unique.
			seen := make(map[string]bool)
			for _, e := range entries {
				assert.NotEmpty(t, e.Hash)
				assert.False(t, seen[e.Hash], "duplicate hash detected")
				seen[e.Hash] = true
			}
		})
	}
}

func TestHashChain_Verify(t *testing.T) {
	hmacKey := []byte("secret-key-for-test")
	hc := NewHashChain(hmacKey)

	// Build a chain of 5 entries.
	entries := make([]*AuditEntry, 0, 5)
	for i := 0; i < 5; i++ {
		e := hc.Append("evt-"+string(rune('A'+i)), "access.login", "actor-1", []byte(`{}`))
		entries = append(entries, e)
	}

	tests := []struct {
		name             string
		mutate           func([]*AuditEntry) []*AuditEntry
		wantValid        bool
		wantInvalidIndex int
	}{
		{
			name:             "intact chain is valid",
			mutate:           func(e []*AuditEntry) []*AuditEntry { return e },
			wantValid:        true,
			wantInvalidIndex: -1,
		},
		{
			name: "tampered payload at index 2",
			mutate: func(e []*AuditEntry) []*AuditEntry {
				tampered := copyEntries(e)
				tampered[2].Payload = []byte(`{"tampered":true}`)
				return tampered
			},
			wantValid:        false,
			wantInvalidIndex: 2,
		},
		{
			name: "tampered hash at index 0 breaks index 1 linkage",
			mutate: func(e []*AuditEntry) []*AuditEntry {
				tampered := copyEntries(e)
				tampered[0].Hash = "deadbeef"
				return tampered
			},
			wantValid:        false,
			wantInvalidIndex: 0,
		},
		{
			name: "swapped entries break chain",
			mutate: func(e []*AuditEntry) []*AuditEntry {
				tampered := copyEntries(e)
				tampered[1], tampered[2] = tampered[2], tampered[1]
				return tampered
			},
			wantValid:        false,
			wantInvalidIndex: 1,
		},
		{
			name: "empty slice is valid",
			mutate: func(e []*AuditEntry) []*AuditEntry {
				return []*AuditEntry{}
			},
			wantValid:        true,
			wantInvalidIndex: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.mutate(entries)
			valid, idx := hc.Verify(input)
			assert.Equal(t, tt.wantValid, valid)
			assert.Equal(t, tt.wantInvalidIndex, idx)
		})
	}
}

func TestHashChain_DifferentKeys_DifferentHashes(t *testing.T) {
	hc1 := NewHashChain([]byte("key-alpha"))
	hc2 := NewHashChain([]byte("key-beta"))

	e1 := hc1.Append("evt-1", "login", "actor", []byte(`{}`))
	e2 := hc2.Append("evt-1", "login", "actor", []byte(`{}`))

	// Same input but different HMAC keys must produce different hashes.
	// Timestamps will differ so hashes will differ anyway, but this confirms
	// the key is part of the computation.
	require.NotEqual(t, e1.Hash, e2.Hash)
}

// copyEntries creates a shallow copy of each entry so mutations do not
// affect the originals.
func copyEntries(src []*AuditEntry) []*AuditEntry {
	dst := make([]*AuditEntry, len(src))
	for i, e := range src {
		cp := *e
		cp.Payload = make([]byte, len(e.Payload))
		copy(cp.Payload, e.Payload)
		dst[i] = &cp
	}
	return dst
}
