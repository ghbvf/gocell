// Package s3archive implements the audit-core ArchiveStore by uploading
// serialised audit entries to S3-compatible object storage.
//
// Instead of importing adapters/s3 directly (which would violate the
// dependency rule: cells/ must not depend on adapters/), this package defines
// an ObjectUploader interface. The assembly layer injects the concrete
// adapters/s3 client at wiring time.
package s3archive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ObjectUploader abstracts the S3 upload operation. The concrete
// implementation (e.g. adapters/s3.Client) is injected by the assembly layer.
type ObjectUploader interface {
	Upload(ctx context.Context, bucket, key string, body io.Reader) error
}

// Compile-time interface check.
var _ ports.ArchiveStore = (*ArchiveStore)(nil)

// Config holds tuneable parameters for the ArchiveStore.
type Config struct {
	Bucket    string
	KeyPrefix string // e.g. "audit/archive/"
}

// ArchiveStore implements ports.ArchiveStore by serialising audit entries to
// JSON and uploading them to an S3-compatible object store via the
// ObjectUploader interface.
type ArchiveStore struct {
	uploader  ObjectUploader
	bucket    string
	keyPrefix string
}

// NewArchiveStore creates an ArchiveStore backed by the given ObjectUploader.
func NewArchiveStore(uploader ObjectUploader, cfg Config) *ArchiveStore {
	return &ArchiveStore{
		uploader:  uploader,
		bucket:    cfg.Bucket,
		keyPrefix: cfg.KeyPrefix,
	}
}

// Archive serialises the provided audit entries to JSON and uploads them as a
// single object. The object key is derived from the current timestamp and the
// first entry's ID for uniqueness.
func (s *ArchiveStore) Archive(ctx context.Context, entries []*domain.AuditEntry) error {
	if len(entries) == 0 {
		return nil
	}

	data, err := json.Marshal(entries)
	if err != nil {
		return errcode.Wrap(errcode.ErrInternal, "s3archive: marshal entries", err)
	}

	key := s.objectKey(entries[0])
	if err := s.uploader.Upload(ctx, s.bucket, key, bytes.NewReader(data)); err != nil {
		return errcode.Wrap(errcode.ErrInternal,
			fmt.Sprintf("s3archive: upload %s/%s", s.bucket, key), err)
	}
	return nil
}

// objectKey builds a deterministic key from the timestamp and first entry ID.
// Format: {prefix}{date}/{firstEntryID}.json
func (s *ArchiveStore) objectKey(first *domain.AuditEntry) string {
	ts := first.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	date := ts.Format("2006/01/02")
	return fmt.Sprintf("%s%s/%s.json", s.keyPrefix, date, first.ID)
}
