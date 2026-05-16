// Package configreadinternal implements the internal control-plane
// config-read slice: GET a config entry under /internal/v1/config, mounted
// on the InternalListener where service-token + caller-cell auth is enforced.
// It shares the read logic with the public configread slice via
// cells/configcore/internal/configreader. Public and internal HTTP surfaces
// are kept in separate slices per governance rule
// SLICE-HTTP-VISIBILITY-SEGREGATION-01 (FMT-33).
package configreadinternal

import "github.com/ghbvf/gocell/cells/configcore/internal/configreader"

// Service is the slice service type; the read logic is shared with the
// public slice via cells/configcore/internal/configreader.
type Service = configreader.Service
