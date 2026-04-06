// Package s3archive provides an S3-backed implementation of the audit-core
// ArchiveStore port. It accepts an ObjectUploader interface, so any
// S3-compatible client (e.g., aws-sdk-go-v2) can be wired in at bootstrap.
package s3archive

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	// ErrArchiveUpload indicates a failure uploading the archive.
	ErrArchiveUpload errcode.Code = "ERR_ARCHIVE_UPLOAD"
	// ErrArchiveMarshal indicates a failure serializing entries.
	ErrArchiveMarshal errcode.Code = "ERR_ARCHIVE_MARSHAL"
)

// ObjectUploader abstracts the upload operation for S3-compatible storage.
// This interface decouples the ArchiveStore from the concrete s3.Client.
type ObjectUploader interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) error
}

// Compile-time interface check.
var _ ports.ArchiveStore = (*ArchiveStore)(nil)

// ArchiveStore archives audit entries to S3-compatible object storage.
type ArchiveStore struct {
	uploader  ObjectUploader
	keyPrefix string
}

// NewArchiveStore creates an ArchiveStore that uses the given ObjectUploader.
// keyPrefix is prepended to all object keys (e.g., "audit/archive/").
func NewArchiveStore(uploader ObjectUploader, keyPrefix string) *ArchiveStore {
	return &ArchiveStore{
		uploader:  uploader,
		keyPrefix: keyPrefix,
	}
}

// Archive serializes the entries to JSON and uploads them as a single object.
// The object key includes a timestamp for chronological ordering.
func (s *ArchiveStore) Archive(ctx context.Context, entries []*domain.AuditEntry) error {
	if len(entries) == 0 {
		return nil
	}

	data, err := json.Marshal(entries)
	if err != nil {
		return errcode.Wrap(ErrArchiveMarshal, "s3archive: failed to marshal entries", err)
	}

	key := fmt.Sprintf("%s%s-%d.json",
		s.keyPrefix,
		time.Now().UTC().Format("2006/01/02/150405"),
		len(entries),
	)

	if err := s.uploader.Upload(ctx, key, data, "application/json"); err != nil {
		return errcode.Wrap(ErrArchiveUpload,
			fmt.Sprintf("s3archive: upload failed for key %s", key), err)
	}

	return nil
}
