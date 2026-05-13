package accesscore

import (
	"time"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// Refresh policy defaults for accesscore — the canonical numbers used by both
// the mem refresh store (composition root demo path + tests) and the PG
// refresh store. Centralizing the literals here keeps a single source of
// truth for ReuseInterval / MaxAge / MaxIdle / GraceMaxReuses across backends
// and satisfies PROD-DURATION-CONST-01 / TEST-TIME-LITERAL-01.
const (
	// DefaultRefreshReuseInterval is the maximum window during which a parent
	// refresh token may be re-presented after rotation before triggering reuse
	// detection. 2s mirrors the original WithInMemoryDefaults value and is
	// short enough that legitimate client retries fit while attackers replaying
	// outside the window get caught.
	DefaultRefreshReuseInterval = 2 * time.Second

	// DefaultRefreshMaxAge is the absolute lifetime of a refresh chain root.
	// 7 days matches the long-lived refresh token expectation set by S2/S3.
	DefaultRefreshMaxAge = 7 * 24 * time.Hour
)

// DefaultRefreshPolicy returns the canonical refresh.Policy used by both the
// in-memory and PG refresh stores. composition root + integration tests both
// call this so any policy bump lives in one place.
func DefaultRefreshPolicy() refresh.Policy {
	return refresh.Policy{
		ReuseInterval:  DefaultRefreshReuseInterval,
		MaxAge:         DefaultRefreshMaxAge,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
}
