// Package spiffeid provides a sealed SPIFFE-ID type identifying a GoCell cell at
// the transport (mTLS) layer.
//
// A cell's SPIFFE ID has the canonical form:
//
//	spiffe://<trustDomain>/cell/<cell>
//
// It is carried as a URI SAN on the cell's mTLS leaf certificate. The client
// transport authorizes a peer by matching the server cert's cell SPIFFE ID
// against the expected target cell ([CellID.Equal]); the server's cross-binding
// guard matches the client cert's cell SPIFFE ID against the service-token
// caller cell. See ADR 202606131142-1423 (#2263) and ADR 049.
//
// INVARIANT (string-typed concept funnel, AI-robust Hard 范本): SPIFFE cell IDs
// are NEVER compared as bare strings. [CellID] is sealed (unexported fields, sole
// constructors [ForCell] / [Parse]); comparison goes through [CellID.Equal]. This
// keeps the spiffe:// wire form single-sourced (canonical [CellID.String]) and
// makes a hand-rolled, mis-normalized string compare unrepresentable at call sites.
//
// This package lives under framework/pkg/ (not runtime/http/tlsutil) so that
// kernel/governance gocell-validate rules — which may only import the standard
// library and pkg/ — can reference the cell-SPIFFE-ID shape (layer rule
// go-standards.md §分层依赖).
//
// ref: spiffe/go-spiffe v2/spiffeid — TrustDomain + ID parse/構造 surface; this is
// a minimal, dependency-free subset (ADR 049 decided NOT to vendor go-spiffe).
package spiffeid

import (
	"net/url"
	"strings"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// scheme is the SPIFFE URI scheme.
const scheme = "spiffe"

// cellPathPrefix is the fixed path prefix for a cell workload ID: the remainder
// after it is the cell name. A SPIFFE ID whose path is not exactly
// "/cell/<non-empty>" is not a cell ID.
const cellPathPrefix = "/cell/"

// Error message constants — MESSAGE-CONST-LITERAL-01.
const (
	msgEmptyTrustDomain   = "spiffeid: trust domain must not be empty"
	msgInvalidTrustDomain = "spiffeid: trust domain must be lowercase and contain only [a-z0-9._-] (no scheme, host:port, or path)"
	msgEmptyCell          = "spiffeid: cell must not be empty or whitespace"
	msgInvalidCell        = "spiffeid: cell must not contain '/', whitespace, or control characters"
	msgParseScheme        = "spiffeid: ID must use the spiffe:// scheme with a non-empty trust domain"
	msgParseCellPath      = "spiffeid: ID path must be exactly /cell/<cell>"
	msgAmbiguousURIs      = "spiffeid: certificate presents more than one distinct cell SPIFFE ID"
)

// CellID is the sealed SPIFFE ID of a GoCell cell at the transport layer:
// spiffe://<trustDomain>/cell/<cell>.
//
// Sealed: both parsed parts live in unexported fields; the only constructors are
// [ForCell] and [Parse]. The zero value is invalid ([CellID.IsZero] reports true);
// compare IDs with [CellID.Equal], never the raw spiffe:// strings.
type CellID struct {
	trustDomain string
	cell        string
}

// ForCell constructs the cell SPIFFE ID spiffe://<trustDomain>/cell/<cell>.
// It validates that trustDomain is a syntactically valid SPIFFE trust domain
// (lowercase [a-z0-9._-], no scheme/host:port/path) and that cell is a non-empty
// token without '/', whitespace, or control characters. Returns KindInvalid on
// violation (fail-closed; no silent normalization).
func ForCell(trustDomain, cell string) (CellID, error) {
	if err := validateTrustDomain(trustDomain); err != nil {
		return CellID{}, err
	}
	if err := validateCell(cell); err != nil {
		return CellID{}, err
	}
	return CellID{trustDomain: trustDomain, cell: cell}, nil
}

// ValidateTrustDomain reports whether td is a syntactically valid SPIFFE trust
// domain (non-empty, lowercase [a-z0-9._-], no scheme/host:port/path). Returns
// KindInvalid otherwise. Exported so composition-root resolvers (cellmodules/
// celltls) and gocell-validate gates can fail-fast on a bad GOCELL_SPIFFE_TRUST_DOMAIN
// at the point of ingestion rather than at first peer-config mint.
func ValidateTrustDomain(td string) error {
	return validateTrustDomain(td)
}

// Parse parses a canonical cell SPIFFE ID string (spiffe://<td>/cell/<cell>).
// Any other shape — wrong scheme, empty host, a path that is not exactly
// /cell/<non-empty>, or extra path segments — is rejected (KindInvalid).
func Parse(raw string) (CellID, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != scheme || u.Host == "" {
		return CellID{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgParseScheme,
			errcode.WithInternal(errcode.InternalAttr("raw", raw)))
	}
	cell, ok := cellFromPath(u.Path)
	if !ok {
		return CellID{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgParseCellPath,
			errcode.WithInternal(errcode.InternalAttr("raw", raw)))
	}
	// u.Host carries the trust domain (and only the trust domain — a spiffe URL
	// has no userinfo/port in canonical form). Validate it the same way ForCell
	// does so Parse and ForCell accept exactly the same trust-domain set.
	return ForCell(u.Host, cell)
}

// FromURIs extracts the single cell SPIFFE ID from a certificate's URI SANs
// (e.g. crypto/x509.Certificate.URIs, after the framework's mTLS middleware has
// copied them into pkg/ctxkeys.PeerIdentity.URIs). Non-cell SPIFFE IDs and
// non-spiffe URIs are ignored.
//
// Returns:
//   - (id, true, nil)  — exactly one distinct cell SPIFFE ID present.
//   - (zero, false, nil) — no cell SPIFFE ID present.
//   - (zero, false, err) — two or more DISTINCT cell SPIFFE IDs present
//     (ambiguous identity → fail-closed; the caller must not guess which).
//
// Duplicate identical cell IDs collapse to one (ok=true).
func FromURIs(uris []*url.URL) (CellID, bool, error) {
	var found CellID
	haveOne := false
	for _, u := range uris {
		if u == nil || u.Scheme != scheme || u.Host == "" {
			continue
		}
		cell, ok := cellFromPath(u.Path)
		if !ok {
			continue // a spiffe:// URI that is not a /cell/ workload ID — ignore.
		}
		id, err := ForCell(u.Host, cell)
		if err != nil {
			continue // malformed trust domain on a cell-shaped path — ignore, not ours.
		}
		if !haveOne {
			found, haveOne = id, true
			continue
		}
		if !found.Equal(id) {
			return CellID{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgAmbiguousURIs,
				errcode.WithInternal(
					errcode.InternalAttr("first", found.String()),
					errcode.InternalAttr("second", id.String()),
				))
		}
	}
	return found, haveOne, nil
}

// String returns the canonical spiffe://<trustDomain>/cell/<cell> form. The zero
// value returns "".
func (c CellID) String() string {
	if c.IsZero() {
		return ""
	}
	return scheme + "://" + c.trustDomain + cellPathPrefix + c.cell
}

// TrustDomain returns the trust-domain part.
func (c CellID) TrustDomain() string { return c.trustDomain }

// Cell returns the cell part.
func (c CellID) Cell() string { return c.cell }

// Equal reports whether two cell SPIFFE IDs are identical in both trust domain
// and cell. This is the ONLY sanctioned comparison (string-typed concept funnel).
func (c CellID) Equal(other CellID) bool {
	return c.trustDomain == other.trustDomain && c.cell == other.cell
}

// IsZero reports whether c is the zero (unconstructed) value.
func (c CellID) IsZero() bool { return c.trustDomain == "" && c.cell == "" }

// cellFromPath returns the cell token of a "/cell/<cell>" path. ok is false when
// the path is not exactly that shape (wrong prefix, empty cell, or extra
// segments).
func cellFromPath(path string) (cell string, ok bool) {
	rest, found := strings.CutPrefix(path, cellPathPrefix)
	if !found || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

// validateTrustDomain enforces the SPIFFE trust-domain charset: lowercase
// letters, digits, dot, hyphen, underscore. Rejects empty, uppercase, and any
// scheme/host:port/path artifacts (':' and '/').
func validateTrustDomain(td string) error {
	if td == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgEmptyTrustDomain)
	}
	for _, r := range td {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgInvalidTrustDomain,
				errcode.WithInternal(errcode.InternalAttr("trustDomain", td)))
		}
	}
	return nil
}

// validateCell enforces a non-empty cell token free of '/', whitespace, and
// control characters. (GoCell cell IDs are no-dash concat tokens, but this type
// stays decoupled from that naming rule — it only rejects what would break the
// spiffe:// path or canonical round-trip.)
func validateCell(cell string) error {
	if strings.TrimSpace(cell) == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgEmptyCell)
	}
	for _, r := range cell {
		if r == '/' || r <= ' ' {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgInvalidCell,
				errcode.WithInternal(errcode.InternalAttr("cell", cell)))
		}
	}
	return nil
}
