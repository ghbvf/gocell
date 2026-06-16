package softca

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// Ledger is softca's pluggable issuance-record store. It records every issued
// certificate (deriving the monotonic per-scope renewal epoch) and the
// revocation state over those records. ONE Ledger backs both the [Signer]
// (issuance + epoch) and the [RevocationStore] (revoke / list / tidy): revocation
// needs the issued certificate's notAfter (for [RevocationStore.Tidy]) and its
// scope (for cross-scope isolation), which are issuance facts the certsigning
// seam does not carry — so a single issuance-record store serves both rather
// than duplicating the data. The default [MemLedger] is in-memory (a dev CA
// resets on restart); a PG-backed Ledger can be injected to persist epochs and
// revocations across restarts with no API change.
type Ledger interface {
	// Record persists an issuance (scope, serial, notAfter) and returns the
	// renewal epoch: 0 for the first certificate issued under scope, prior+1 for
	// each subsequent renewal. Serials are unique by construction (random 159-bit);
	// a duplicate serial within scope MUST fail closed in a persistent
	// implementation — [MemLedger] overwrites it (safe only because a dev CA's
	// state is ephemeral). A PG-backed Ledger should reject the collision.
	Record(ctx context.Context, scope certsigning.CertScope, serial certsigning.Serial, notAfter time.Time) (epoch uint64, err error)

	// Revoke marks an issued serial revoked within scope, at instant at. A serial
	// not issued within scope fails closed (a cross-scope or unknown serial is
	// treated as not found — 绝不凭裸 serial 跨隔离域). Revocation is TERMINAL: every
	// reason (including RFC 5280 certificateHold / removeFromCRL) marks the serial
	// revoked — softca does NOT model the hold→un-hold lifecycle, so removeFromCRL
	// does not lift a hold. Modeling held-vs-revoked state (without enabling
	// un-revocation of a compromised key) is deferred to a later PR.
	Revoke(
		ctx context.Context,
		scope certsigning.CertScope,
		serial certsigning.Serial,
		reason certsigning.RevocationReason,
		at time.Time,
	) error

	// Revoked returns the revoked certificates within scope (RevokedAt in UTC),
	// ordered by serial for deterministic CRL output.
	Revoked(ctx context.Context, scope certsigning.CertScope) ([]certsigning.RevokedCertificate, error)

	// Tidy drops the whole record of any revoked certificate within scope whose
	// notAfter is before the given instant — an expired certificate no longer needs
	// to appear on a CRL (RFC 5280 §5.2.6). Non-revoked records are untouched. (The
	// epoch counter is independent of the record set, so dropping expired records
	// does not perturb renewal epochs.)
	Tidy(ctx context.Context, scope certsigning.CertScope, before time.Time) error

	// NextCRLNumber returns the next strictly-monotonic CRL Number for scope (RFC
	// 5280 §5.2.3: a CRL Number MUST be greater than every prior one for the same
	// CRL, else a strict cache ignores the newer list → stale revocation). The
	// counter lives in the Ledger — the persistence boundary — so a PG-backed Ledger
	// keeps numbers climbing across restarts; [MemLedger] resets on restart (a dev CA
	// is ephemeral anyway). Numbers are per-scope (each scope publishes its own CRL)
	// and need not be gap-free: a burned number from a CRL that later failed to sign
	// is fine, only monotonicity matters.
	NextCRLNumber(ctx context.Context, scope certsigning.CertScope) (uint64, error)
}

// MemLedger is the in-memory [Ledger] — softca's dev/test default. It loses all
// state on restart; inject a persistent Ledger for multi-restart deployments.
// CertScope has only comparable fields, so it is used directly as the map key
// (no string-join key, no collision surface).
type MemLedger struct {
	mu     sync.Mutex
	scopes map[certsigning.CertScope]*scopeRecords
}

// scopeRecords holds one isolation scope's issuance count (the next renewal
// epoch), its monotonic CRL Number, and its per-serial certificate records.
type scopeRecords struct {
	issued    uint64
	crlNumber uint64
	certs     map[certsigning.Serial]*certRecord
}

// certRecord is one issued certificate's ledger entry: its expiry plus the
// revocation state layered over it.
type certRecord struct {
	notAfter  time.Time
	revoked   bool
	reason    certsigning.RevocationReason
	revokedAt time.Time
}

// NewMemLedger returns an empty in-memory Ledger.
func NewMemLedger() *MemLedger {
	return &MemLedger{scopes: make(map[certsigning.CertScope]*scopeRecords)}
}

// scope returns the records for scope, creating them if absent. Caller holds mu.
func (l *MemLedger) scope(s certsigning.CertScope) *scopeRecords {
	sr := l.scopes[s]
	if sr == nil {
		sr = &scopeRecords{certs: make(map[certsigning.Serial]*certRecord)}
		l.scopes[s] = sr
	}
	return sr
}

// Record implements [Ledger].
func (l *MemLedger) Record(_ context.Context, scope certsigning.CertScope, serial certsigning.Serial, notAfter time.Time) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	sr := l.scope(scope)
	epoch := sr.issued
	sr.issued++
	sr.certs[serial] = &certRecord{notAfter: notAfter}
	return epoch, nil
}

// Revoke implements [Ledger]. A serial not issued within scope fails closed.
func (l *MemLedger) Revoke(
	_ context.Context,
	scope certsigning.CertScope,
	serial certsigning.Serial,
	reason certsigning.RevocationReason,
	at time.Time,
) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	sr := l.scopes[scope]
	if sr == nil {
		return errRevokeNotFound("scope has no issued certificates")
	}
	rec := sr.certs[serial]
	if rec == nil {
		return errRevokeNotFound("serial not issued within scope")
	}
	rec.revoked = true
	rec.reason = reason
	rec.revokedAt = at.UTC()
	return nil
}

// Revoked implements [Ledger].
func (l *MemLedger) Revoked(_ context.Context, scope certsigning.CertScope) ([]certsigning.RevokedCertificate, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	sr := l.scopes[scope]
	if sr == nil {
		return nil, nil
	}
	out := make([]certsigning.RevokedCertificate, 0, len(sr.certs))
	for serial, rec := range sr.certs {
		if !rec.revoked {
			continue
		}
		out = append(out, certsigning.RevokedCertificate{
			Serial:    serial,
			Reason:    rec.reason,
			RevokedAt: rec.revokedAt.UTC(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Serial.String() < out[j].Serial.String() })
	return out, nil
}

// NextCRLNumber implements [Ledger]: a per-scope counter incremented under the
// ledger lock, so two CRLs minted within the same instant (or after a clock
// step-back) never share or regress a Number. It resets on restart with the
// in-memory state — a persistent Ledger seeds it from durable storage instead.
func (l *MemLedger) NextCRLNumber(_ context.Context, scope certsigning.CertScope) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	sr := l.scope(scope)
	sr.crlNumber++
	return sr.crlNumber, nil
}

// Tidy implements [Ledger]: drops revoked records whose certificate expired
// before the given instant.
func (l *MemLedger) Tidy(_ context.Context, scope certsigning.CertScope, before time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	sr := l.scopes[scope]
	if sr == nil {
		return nil
	}
	for serial, rec := range sr.certs {
		if rec.revoked && rec.notAfter.Before(before) {
			delete(sr.certs, serial)
		}
	}
	return nil
}
