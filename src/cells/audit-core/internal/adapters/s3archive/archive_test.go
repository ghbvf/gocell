package s3archive

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----- test doubles -----

// fakeUploader records Upload calls for assertion.
type fakeUploader struct {
	bucket string
	key    string
	body   []byte
	err    error
}

func (f *fakeUploader) Upload(_ context.Context, bucket, key string, body io.Reader) error {
	f.bucket = bucket
	f.key = key
	if body != nil {
		data, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		f.body = data
	}
	return f.err
}

// ----- tests -----

func TestArchiveStore_Archive_Success(t *testing.T) {
	uploader := &fakeUploader{}
	store := NewArchiveStore(uploader, Config{
		Bucket:    "audit-bucket",
		KeyPrefix: "audit/archive/",
	})

	ts := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)
	entries := []*domain.AuditEntry{
		{
			ID:        "ae-001",
			EventID:   "evt-001",
			EventType: "config.changed",
			ActorID:   "usr-001",
			Timestamp: ts,
			Payload:   []byte(`{"key":"val"}`),
			PrevHash:  "abc",
			Hash:      "def",
		},
		{
			ID:        "ae-002",
			EventID:   "evt-002",
			EventType: "session.created",
			ActorID:   "usr-002",
			Timestamp: ts.Add(time.Minute),
			Payload:   []byte(`{}`),
			PrevHash:  "def",
			Hash:      "ghi",
		},
	}

	err := store.Archive(context.Background(), entries)
	require.NoError(t, err)

	assert.Equal(t, "audit-bucket", uploader.bucket)
	assert.Equal(t, "audit/archive/2026/04/05/ae-001.json", uploader.key)

	// Verify the body is valid JSON containing our entries.
	var decoded []*domain.AuditEntry
	err = json.Unmarshal(uploader.body, &decoded)
	require.NoError(t, err)
	assert.Len(t, decoded, 2)
	assert.Equal(t, "ae-001", decoded[0].ID)
	assert.Equal(t, "ae-002", decoded[1].ID)
}

func TestArchiveStore_Archive_Empty(t *testing.T) {
	uploader := &fakeUploader{}
	store := NewArchiveStore(uploader, Config{Bucket: "b", KeyPrefix: "p/"})

	err := store.Archive(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, uploader.bucket, "should not call upload for empty entries")

	err = store.Archive(context.Background(), []*domain.AuditEntry{})
	require.NoError(t, err)
	assert.Empty(t, uploader.bucket)
}

func TestArchiveStore_Archive_UploadError(t *testing.T) {
	uploader := &fakeUploader{err: assert.AnError}
	store := NewArchiveStore(uploader, Config{Bucket: "b", KeyPrefix: "p/"})

	entries := []*domain.AuditEntry{
		{
			ID:        "ae-001",
			Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	err := store.Archive(context.Background(), entries)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_INTERNAL")
	assert.Contains(t, err.Error(), "s3archive: upload")
}

func TestArchiveStore_ObjectKey(t *testing.T) {
	store := &ArchiveStore{keyPrefix: "audit/"}

	tests := []struct {
		name    string
		entry   *domain.AuditEntry
		wantKey string
	}{
		{
			name: "normal timestamp",
			entry: &domain.AuditEntry{
				ID:        "ae-100",
				Timestamp: time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC),
			},
			wantKey: "audit/2026/12/31/ae-100.json",
		},
		{
			name: "zero timestamp uses now",
			entry: &domain.AuditEntry{
				ID: "ae-200",
			},
			// We cannot assert exact key because it uses time.Now(), but we
			// can verify the format.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := store.objectKey(tt.entry)
			assert.Contains(t, key, "audit/")
			assert.Contains(t, key, tt.entry.ID+".json")
			if tt.wantKey != "" {
				assert.Equal(t, tt.wantKey, key)
			}
		})
	}
}

func TestObjectUploader_Interface(t *testing.T) {
	// Verify that fakeUploader satisfies ObjectUploader.
	var _ ObjectUploader = (*fakeUploader)(nil)
}

func TestArchiveStore_MarshalledPayloadIsValid(t *testing.T) {
	uploader := &fakeUploader{}
	store := NewArchiveStore(uploader, Config{Bucket: "b", KeyPrefix: ""})

	entries := []*domain.AuditEntry{
		{
			ID:        "ae-010",
			Timestamp: time.Date(2026, 6, 15, 10, 30, 0, 0, time.UTC),
			Payload:   []byte(`{"nested":{"deep":true}}`),
		},
	}

	err := store.Archive(context.Background(), entries)
	require.NoError(t, err)

	// Verify the uploaded body is parseable JSON.
	assert.True(t, json.Valid(uploader.body))

	// Verify it round-trips.
	reader := bytes.NewReader(uploader.body)
	var roundtrip []*domain.AuditEntry
	err = json.NewDecoder(reader).Decode(&roundtrip)
	require.NoError(t, err)
	assert.Equal(t, entries[0].ID, roundtrip[0].ID)
}
