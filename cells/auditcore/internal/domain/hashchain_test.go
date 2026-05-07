package domain

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// 32-byte test key reused by Append/Verify tests; meets RFC 2104 §3 minimum.
var testHMACKey32 = []byte("test-hmac-key-32bytes-long!!!!!!!")

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
			hc, err := NewHashChain(testHMACKey32)
			require.NoError(t, err)
			entries := make([]*AuditEntry, 0, tt.appendN)

			for i := 0; i < tt.appendN; i++ {
				e := hc.Append("evt-id", "login", "actor-1", []byte(`{"ip":"10.0.0.1"}`), time.Now())
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
	hc, err := NewHashChain(testHMACKey32)
	require.NoError(t, err)

	// Build a chain of 5 entries.
	entries := make([]*AuditEntry, 0, 5)
	for i := range 5 {
		e := hc.Append("evt-"+string(rune('A'+i)), "access.login", "actor-1", []byte(`{}`), time.Now())
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
	hc1, err := NewHashChain([]byte("key-alpha-padded-to-32bytes-len!!"))
	require.NoError(t, err)
	hc2, err := NewHashChain([]byte("key-beta-padded-to-32bytes-len!!!"))
	require.NoError(t, err)

	now := time.Now()
	e1 := hc1.Append("evt-1", "login", "actor", []byte(`{}`), now)
	e2 := hc2.Append("evt-1", "login", "actor", []byte(`{}`), now)

	// Same input but different HMAC keys must produce different hashes.
	// Timestamps will differ so hashes will differ anyway, but this confirms
	// the key is part of the computation.
	require.NotEqual(t, e1.Hash, e2.Hash)
}

// TestNewHashChain_KeyLength locks in the RFC 2104 §3 / NIST SP 800-107
// minimum-key-length contract: HMAC-SHA256 keys must be at least 32 bytes
// (the underlying hash output size). Shorter keys are rejected with a
// public errcode.ErrValidationFailed carrying minimumBytes / actualBytes
// details — the key bytes themselves never appear in the error.
func TestNewHashChain_KeyLength(t *testing.T) {
	tests := []struct {
		name      string
		keyLen    int
		wantOK    bool
		wantBytes int // expected actualBytes detail when rejected
	}{
		{name: "nil key rejected", keyLen: 0, wantOK: false, wantBytes: 0},
		{name: "1 byte rejected", keyLen: 1, wantOK: false, wantBytes: 1},
		{name: "31 bytes rejected", keyLen: 31, wantOK: false, wantBytes: 31},
		{name: "32 bytes accepted (RFC 2104 §3 minimum)", keyLen: 32, wantOK: true},
		{name: "33 bytes accepted", keyLen: 33, wantOK: true},
		{name: "64 bytes accepted", keyLen: 64, wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var key []byte
			if tt.keyLen > 0 {
				key = make([]byte, tt.keyLen)
				for i := range key {
					key[i] = 'a'
				}
			}

			hc, err := NewHashChain(key)
			if tt.wantOK {
				require.NoError(t, err)
				require.NotNil(t, hc)
				return
			}

			require.Error(t, err)
			require.Nil(t, hc)

			var ec *errcode.Error
			require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
			assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
			assert.Contains(t, ec.Message, "audit hmac key too short")

			// minimumBytes / actualBytes details surface only the lengths,
			// never the key bytes themselves.
			var sawMin, sawActual bool
			for _, attr := range ec.Details {
				switch attr.Key {
				case "minimumBytes":
					sawMin = true
					assert.Equal(t, slog.KindInt64, attr.Value.Kind())
					assert.Equal(t, int64(32), attr.Value.Int64())
				case "actualBytes":
					sawActual = true
					assert.Equal(t, slog.KindInt64, attr.Value.Kind())
					assert.Equal(t, int64(tt.wantBytes), attr.Value.Int64())
				}
			}
			assert.True(t, sawMin, "details must include minimumBytes")
			assert.True(t, sawActual, "details must include actualBytes")
		})
	}
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
