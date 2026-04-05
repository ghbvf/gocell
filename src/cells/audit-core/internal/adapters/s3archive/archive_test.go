package s3archive

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArchiveStore_Archive(t *testing.T) {
	uploader := &mockUploader{}
	store := NewArchiveStore(uploader, "audit/archive/")

	entries := []*domain.AuditEntry{
		{
			ID:        "ae-1",
			EventID:   "evt-1",
			EventType: "session.created",
			ActorID:   "usr-1",
			Payload:   []byte(`{"action":"login"}`),
			Hash:      "h1",
		},
		{
			ID:        "ae-2",
			EventID:   "evt-2",
			EventType: "session.logout",
			ActorID:   "usr-1",
			Payload:   []byte(`{"action":"logout"}`),
			PrevHash:  "h1",
			Hash:      "h2",
		},
	}

	err := store.Archive(context.Background(), entries)
	require.NoError(t, err)

	require.Len(t, uploader.uploads, 1)
	upload := uploader.uploads[0]

	assert.Contains(t, upload.key, "audit/archive/")
	assert.Contains(t, upload.key, "-2.json")
	assert.Equal(t, "application/json", upload.contentType)

	// Verify the payload is valid JSON containing the entries.
	var parsed []*domain.AuditEntry
	require.NoError(t, json.Unmarshal(upload.data, &parsed))
	assert.Len(t, parsed, 2)
	assert.Equal(t, "ae-1", parsed[0].ID)
	assert.Equal(t, "ae-2", parsed[1].ID)
}

func TestArchiveStore_Archive_Empty(t *testing.T) {
	uploader := &mockUploader{}
	store := NewArchiveStore(uploader, "prefix/")

	err := store.Archive(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, uploader.uploads)

	err = store.Archive(context.Background(), []*domain.AuditEntry{})
	require.NoError(t, err)
	assert.Empty(t, uploader.uploads)
}

func TestArchiveStore_Archive_UploadError(t *testing.T) {
	uploader := &mockUploader{uploadErr: assert.AnError}
	store := NewArchiveStore(uploader, "prefix/")

	entries := []*domain.AuditEntry{{ID: "ae-1"}}
	err := store.Archive(context.Background(), entries)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrArchiveUpload, ec.Code)
}

// --- mock ---

type uploadRecord struct {
	key         string
	data        []byte
	contentType string
}

type mockUploader struct {
	mu        sync.Mutex
	uploads   []uploadRecord
	uploadErr error
}

func (m *mockUploader) Upload(_ context.Context, key string, data []byte, contentType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.uploadErr != nil {
		return m.uploadErr
	}
	m.uploads = append(m.uploads, uploadRecord{
		key:         key,
		data:        data,
		contentType: contentType,
	})
	return nil
}
