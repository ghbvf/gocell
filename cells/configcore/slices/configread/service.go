// Package configread implements the public config-read slice: GET + list of
// config entries under /api/v1/config (admin-gated). The internal
// control-plane GET (/internal/v1/config) lives in the sibling
// configreadinternal slice; both share cells/configcore/internal/configreader.
// Keeping public and internal HTTP surfaces in separate slices is enforced by
// governance rule SLICE-HTTP-VISIBILITY-SEGREGATION-01 (FMT-33).
package configread

import "github.com/ghbvf/gocell/cells/configcore/internal/configreader"

// Service is the slice service type; the read logic is shared with the
// internal slice via cells/configcore/internal/configreader.
type Service = configreader.Service
