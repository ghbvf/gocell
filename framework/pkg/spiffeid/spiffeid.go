// Package spiffeid provides a sealed SPIFFE-ID type identifying a GoCell cell at
// the transport (mTLS) layer.
//
// A cell's SPIFFE ID has the canonical form:
//
//	spiffe://<trustDomain>/cell/<cell>
//
// A cell SPIFFE ID is carried as a URI SAN on the cell's mTLS leaf certificate. A
// process may host SEVERAL cells (#2297): its workload certificate then carries
// every hosted cell's SPIFFE ID, forming a [CellSet]. Peer authorization is set
// MEMBERSHIP, not single-identity equality — the client transport checks the
// expected target cell is IN the server cert's set ([CellSet.Contains]); the
// server's cross-binding guard checks the service-token caller cell is IN the
// client cert's set. This is the allow-set model of go-spiffe's
// tlsconfig.Authorizer (AuthorizeMemberOf). See ADR 202606171200-2263 (#2263,
// amended #2297) and ADR 049.
//
// INVARIANT (string-typed concept funnel, AI-robust Hard 范本): SPIFFE cell IDs
// are NEVER compared as bare strings. [CellID] and [CellSet] are sealed
// (unexported fields, sole constructors [ForCell] / [Parse] / [CellSetFromURIs]);
// comparison goes through [CellID.Equal] and membership through
// [CellSet.Contains] (which takes a [CellID], so the trust domain is always part
// of the check). Neither type exposes a raw comparable cell string, so a
// hand-rolled, mis-normalized string compare is unrepresentable at call sites and
// the spiffe:// wire form stays single-sourced (canonical [CellID.String]).
//
// The constructors share ONE strict parser (parseCellURI): a cell-shaped SPIFFE
// URI that is not canonical — carrying userinfo, a port, a query, a fragment, an
// opaque part, or an invalid trust domain — is rejected, never normalized. Only
// the exact spiffe://<td>/cell/<cell> form enters a [CellID] / [CellSet], so a
// forged ID like spiffe://evil@td/cell/x cannot masquerade as a member.
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
	"sort"
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
	msgMixedTrustDomains  = "spiffeid: certificate presents cell SPIFFE IDs from more than one trust domain"
	msgCellURIComponents  = "spiffeid: cell SPIFFE ID must not carry userinfo, port, query, fragment, or an opaque part"
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
// /cell/<non-empty>, extra path segments, or non-canonical URL components
// (userinfo, port, query, fragment, opaque part) — is rejected (KindInvalid).
func Parse(raw string) (CellID, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != scheme || u.Host == "" {
		return CellID{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgParseScheme,
			errcode.WithInternal(errcode.InternalAttr("raw", raw)))
	}
	id, ok, perr := parseCellURI(u)
	if !ok {
		return CellID{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgParseCellPath,
			errcode.WithInternal(errcode.InternalAttr("raw", raw)))
	}
	// ok == true: u is cell-shaped; perr carries any non-canonical-component or
	// invalid-trust-domain rejection (the precise reason), nil on success.
	return id, perr
}

// CellSet is the sealed set of cell SPIFFE IDs carried on a single mTLS workload
// certificate (its URI SANs), all sharing ONE trust domain. A process hosting
// several cells presents a workload cert whose CellSet enumerates every cell it
// hosts (#2297). Peer authorization is membership ([CellSet.Contains]) — the
// allow-set model of go-spiffe's tlsconfig.Authorizer (AuthorizeMemberOf) — never
// single-identity equality, so co-located cells legitimately share one cert.
//
// Sealed: fields unexported, sole constructor [CellSetFromURIs]. The zero value is
// the empty set ([CellSet.IsEmpty] reports true). Membership goes through
// Contains(CellID); the set never exposes a raw comparable cell string (string-
// typed concept funnel, see package INVARIANT).
type CellSet struct {
	trustDomain string
	cells       map[string]struct{}
}

// CellSetFromURIs extracts the set of cell SPIFFE IDs from a certificate's URI
// SANs (e.g. crypto/x509.Certificate.URIs, after the framework's mTLS middleware
// has copied them into pkg/ctxkeys.PeerIdentity.URIs). Foreign SANs — non-spiffe
// URIs and spiffe URIs that are not /cell/ workload IDs — are ignored; duplicate
// identical cell IDs collapse.
//
// A cell-shaped SPIFFE URI (spiffe scheme + /cell/<cell> path) that is NOT
// canonical — carrying userinfo, a port, a query, a fragment, an opaque part, or
// an invalid trust domain — is REJECTED (KindInvalid), never silently ignored: a
// non-canonical ID that resembles a cell must fail closed, because silently
// dropping it (or, worse, normalizing it) would let e.g. spiffe://evil@td/cell/x
// be read as the legitimate spiffe://td/cell/x member.
//
// All cell SPIFFE IDs MUST share one trust domain: a certificate carrying cell IDs
// from two distinct trust domains is rejected (KindInvalid) — a workload belongs
// to exactly one trust domain, and a bridging cert is a misconfiguration that must
// fail closed rather than authorize against either domain. Returns the empty set
// (no error) when no cell SPIFFE ID is present; the caller decides whether that is
// acceptable.
func CellSetFromURIs(uris []*url.URL) (CellSet, error) {
	set := CellSet{}
	for _, u := range uris {
		id, ok, err := parseCellURI(u)
		if err != nil {
			return CellSet{}, err
		}
		if !ok {
			continue // not a cell SPIFFE URI — a foreign SAN, ignore.
		}
		if set.cells == nil {
			set.trustDomain = id.trustDomain
			set.cells = make(map[string]struct{}, len(uris))
		}
		if id.trustDomain != set.trustDomain {
			return CellSet{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgMixedTrustDomains,
				errcode.WithInternal(
					errcode.InternalAttr("first", set.trustDomain),
					errcode.InternalAttr("second", id.trustDomain),
				))
		}
		set.cells[id.cell] = struct{}{}
	}
	return set, nil
}

// Contains reports whether id is a member of the set: both the trust domain and
// the cell must match. The zero CellID is never a member. This is the sole
// sanctioned membership check (string-typed concept funnel) — the go-spiffe
// AuthorizeMemberOf analog.
func (s CellSet) Contains(id CellID) bool {
	if id.IsZero() || id.trustDomain != s.trustDomain {
		return false
	}
	_, ok := s.cells[id.cell]
	return ok
}

// TrustDomain returns the common trust domain of the set's cells, or "" for the
// empty set. 仅用于诊断或传给 [ForCell] / [ValidateTrustDomain] 等调用；不要在包外直接
// 比较返回的字符串——成员判定走 [CellSet.Contains](CellID)。
func (s CellSet) TrustDomain() string { return s.trustDomain }

// Len returns the number of distinct cells in the set.
func (s CellSet) Len() int { return len(s.cells) }

// IsEmpty reports whether the set carries no cell SPIFFE ID.
func (s CellSet) IsEmpty() bool { return len(s.cells) == 0 }

// String returns a deterministic, canonical diagnostic rendering: the sorted
// spiffe://<td>/cell/<cell> IDs joined by spaces inside brackets (e.g.
// "[spiffe://td/cell/a spiffe://td/cell/b]"). The empty set returns "[]".
func (s CellSet) String() string {
	if s.IsEmpty() {
		return "[]"
	}
	ids := make([]string, 0, len(s.cells))
	for cell := range s.cells {
		ids = append(ids, scheme+"://"+s.trustDomain+cellPathPrefix+cell)
	}
	sort.Strings(ids)
	return "[" + strings.Join(ids, " ") + "]"
}

// String returns the canonical spiffe://<trustDomain>/cell/<cell> form. The zero
// value returns "".
func (c CellID) String() string {
	if c.IsZero() {
		return ""
	}
	return scheme + "://" + c.trustDomain + cellPathPrefix + c.cell
}

// Cells returns the sorted slice of CellIDs in the set. The zero-value / empty set
// returns nil (no allocation). The returned slice is a snapshot — mutations to the
// set after this call are not reflected. Callers should use the returned []CellID
// only for diagnostics and error reporting; membership checks must go through
// [CellSet.Contains] (which takes a CellID, not a bare string).
func (s CellSet) Cells() []CellID {
	if s.IsEmpty() {
		return nil
	}
	ids := make([]CellID, 0, len(s.cells))
	for cell := range s.cells {
		ids = append(ids, CellID{trustDomain: s.trustDomain, cell: cell})
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].cell < ids[j].cell })
	return ids
}

// TrustDomain returns the trust-domain part. 仅用于诊断 / 传给 [ForCell] /
// [ValidateTrustDomain] 使用；不要在包外直接比较返回值——成员判定走
// [CellSet.Contains](CellID)。
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

// parseCellURI is the single strict translator from a SAN URL to a cell SPIFFE
// ID, shared by [Parse] and [CellSetFromURIs] so the canonical funnel has exactly
// one definition (it closes the bug class where two parse sites disagreed on what
// a cell URI may contain). Its three outcomes:
//
//   - ok=false, err=nil  → u is not a cell SPIFFE URI at all (not the spiffe
//     scheme, or a path that is not /cell/<cell>). Foreign SANs are ignored — a
//     workload cert may legitimately carry non-cell URI SANs.
//   - ok=true,  err!=nil → u IS cell-shaped (spiffe scheme + /cell/<cell> path)
//     but non-canonical: it carries URL components a SPIFFE ID must never have
//     (userinfo / port / query / fragment / opaque) or an invalid trust domain.
//     This is a malformed or forged identity and MUST fail closed — never be
//     silently normalized to the canonical spiffe://<td>/cell/<cell> it resembles.
//   - ok=true,  err=nil  → u is a canonical cell SPIFFE ID; id is its value.
func parseCellURI(u *url.URL) (id CellID, ok bool, err error) {
	if u == nil || u.Scheme != scheme {
		return CellID{}, false, nil
	}
	cell, isCell := cellFromPath(u.Path)
	if !isCell {
		return CellID{}, false, nil
	}
	// Cell-shaped: it MUST now be canonical. url.Parse stashes userinfo, port,
	// query, and fragment OUTSIDE Host/Path, so a Host/Path-only check would read
	// spiffe://evil@td/cell/x?q#f as the clean spiffe://td/cell/x. A SPIFFE ID
	// carries none of these components; reject them at the funnel (fail closed).
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Port() != "" {
		return CellID{}, true, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgCellURIComponents,
			errcode.WithInternal(errcode.InternalAttr("raw", u.String())))
	}
	// Hostname() (== Host here, port already rejected) carries the trust domain;
	// validate it the same way ForCell does so every entry point accepts exactly
	// the same trust-domain set.
	id, ferr := ForCell(u.Hostname(), cell)
	if ferr != nil {
		return CellID{}, true, ferr
	}
	return id, true, nil
}

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
