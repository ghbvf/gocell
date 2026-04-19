// Package refresh provides the server-side opaque refresh token store interface
// and in-memory implementation used by the access-core session slices.
//
// Design origin: F2 Refresh Token Opaque + Server-side Store from
// docs/plans/202604191515-auth-federated-whistle.md §F2.
//
// ref: dexidp/dex storage/storage.go RefreshToken (ObsoleteToken + LastUsed)
// ref: dexidp/dex server/refreshhandlers.go (reuseInterval + AllowedToReuse)
package refresh

import "time"

// Token is the persisted refresh token state.
//
// ID is the opaque string presented to clients (base64url(rand32), 43 chars).
// ObsoleteToken holds the previous generation after a Rotate; non-empty values
// may be presented within Policy.ReuseInterval for idempotent client retries.
//
// ref: dexidp/dex storage/storage.go RefreshToken.Token + ObsoleteToken
type Token struct {
	ID            string
	ObsoleteToken string
	SessionID     string
	SubjectID     string
	CreatedAt     time.Time
	LastUsed      time.Time
	ExpiresAt     time.Time
}

// Policy controls Rotate semantics and token lifetime.
//
// ReuseInterval is the grace window during which the obsolete token of the
// previous generation remains a valid client retry (not a reuse attack).
// MaxAge bounds Token.ExpiresAt - Token.CreatedAt.
//
// Defaults: ReuseInterval = 2 * time.Second (Hydra Fosite GracePeriod default),
// MaxAge = 7 * 24 * time.Hour. The zero value is invalid.
//
// ref: dexidp/dex server/refreshhandlers.go AllowedToReuse reuseInterval
// ref: Hydra Fosite GracePeriod (default 2s)
type Policy struct {
	ReuseInterval time.Duration
	MaxAge        time.Duration
}

// Clock abstracts time.Now for deterministic testing.
type Clock interface {
	Now() time.Time
}
