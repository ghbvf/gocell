package certsigning

import (
	"crypto/x509"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// maxIdentLen bounds issuer / device / serial / common-name string lengths,
// mirroring the contract-schema maxLength guards on the deviceidentity wire
// (#1899: deviceId maxLength 256, serial maxLength 128, issuer maxLength 256).
const (
	maxIssuerLen     = 256
	maxDeviceLen     = 256
	maxSerialHexLen  = 128
	maxCommonNameLen = 256
)

// IssuerID is the sealed issuing-CA identifier (the RFC 5280 issuer dimension of
// a certificate identity: issuer name + serial uniquely identify a certificate,
// not a globally-unique serial — RFC 5280 §4.1.2.2). The single field is
// unexported, so a populated IssuerID cannot be forged by an external composite
// literal; [NewIssuerID] is the sole minter. A zero value is invalid (IsZero).
type IssuerID struct{ v string }

// NewIssuerID validates s (non-blank, within maxIssuerLen) and returns a sealed
// IssuerID. Empty / over-long input is rejected fail-closed — an issuer cannot be
// absent at the point a CertScope is required.
func NewIssuerID(s string) (IssuerID, error) {
	if s == "" {
		return IssuerID{}, errCertScopeInvalid("issuer must not be empty")
	}
	if len(s) > maxIssuerLen {
		return IssuerID{}, errCertScopeInvalid("issuer too long")
	}
	return IssuerID{v: s}, nil
}

// String returns the underlying issuer identifier.
func (i IssuerID) String() string { return i.v }

// IsZero reports whether i is the invalid zero value.
func (i IssuerID) IsZero() bool { return i.v == "" }

// DeviceID is the sealed device identifier (the device dimension of a CertScope
// and the subject of a device certificate). The single field is unexported;
// [NewDeviceID] is the sole minter. A zero value is invalid.
type DeviceID struct{ v string }

// NewDeviceID validates s (non-blank, within maxDeviceLen) and returns a sealed
// DeviceID. Empty / over-long input is rejected fail-closed.
func NewDeviceID(s string) (DeviceID, error) {
	if s == "" {
		return DeviceID{}, errCertScopeInvalid("device must not be empty")
	}
	if len(s) > maxDeviceLen {
		return DeviceID{}, errCertScopeInvalid("device too long")
	}
	return DeviceID{v: s}, nil
}

// String returns the underlying device identifier.
func (d DeviceID) String() string { return d.v }

// IsZero reports whether d is the invalid zero value.
func (d DeviceID) IsZero() bool { return d.v == "" }

// Serial is the sealed certificate serial number in canonical lowercase hex
// (RFC 5280 §4.1.2.2). It is NOT a CertScope dimension: a serial alone never
// crosses an isolation boundary — Revoke / RevocationList scope it with a
// [CertScope] (issuer + serial is the RFC 5280 unique identity). The single
// field is unexported; [NewSerial] is the sole minter.
type Serial struct{ v string }

// NewSerial validates that hex is a non-blank, within-bounds hexadecimal serial
// and returns a sealed Serial normalized to lowercase. Non-hex / empty / over-
// long input is rejected fail-closed.
func NewSerial(hex string) (Serial, error) {
	if hex == "" {
		return Serial{}, errCertScopeInvalid("serial must not be empty")
	}
	if len(hex) > maxSerialHexLen {
		return Serial{}, errCertScopeInvalid("serial too long")
	}
	for _, r := range hex {
		if !isHexDigit(r) {
			return Serial{}, errCertScopeInvalid("serial must be hexadecimal")
		}
	}
	return Serial{v: strings.ToLower(hex)}, nil
}

// String returns the canonical lowercase hex serial.
func (s Serial) String() string { return s.v }

// IsZero reports whether s is the invalid zero value.
func (s Serial) IsZero() bool { return s.v == "" }

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// CertScope is the sealed isolation scope shared by signing and revocation:
// {tenant, issuer, device}. It is the typed positional parameter that makes
// "操作不带 scope" a compile error on Signer/RevocationStore APIs, and the
// isolation invariant behind the #1899 status-contract narrowing (a bare serial
// is never an isolation key — specific-cert lookup must carry a CertScope).
// All three fields are unexported; [NewCertScope] is the sole minter.
type CertScope struct {
	tenant tenant.TenantID
	issuer IssuerID
	device DeviceID
}

// NewCertScope returns a sealed CertScope from typed dimensions. The tenant is a
// canonical-UUID [tenant.TenantID] (validated here); issuer and device must be
// non-zero. Any missing dimension is rejected fail-closed — a scope cannot
// express a valid isolation predicate without all three.
func NewCertScope(t tenant.TenantID, issuer IssuerID, device DeviceID) (CertScope, error) {
	if err := t.Validate(); err != nil {
		return CertScope{}, errcode.Wrap(errcode.KindInvalid, errCertScopeInvalidCode,
			msgScopeTenantInvalid, err)
	}
	if issuer.IsZero() {
		return CertScope{}, errCertScopeInvalid("issuer must not be empty")
	}
	if device.IsZero() {
		return CertScope{}, errCertScopeInvalid("device must not be empty")
	}
	return CertScope{tenant: t, issuer: issuer, device: device}, nil
}

// Tenant returns the scope tenant.
func (s CertScope) Tenant() tenant.TenantID { return s.tenant }

// Issuer returns the scope issuer.
func (s CertScope) Issuer() IssuerID { return s.issuer }

// Device returns the scope device.
func (s CertScope) Device() DeviceID { return s.device }

// IsZero reports whether s is the zero value (any dimension absent). Because
// CertScope is sealed (the only minter is [NewCertScope], which validates the
// tenant as a canonical UUID and rejects empty issuer/device), a non-zero
// CertScope is guaranteed well-formed — so IsZero is a sufficient guard on any
// API taking a CertScope. It is a zero-value test, not a re-validation: a value
// that survived NewCertScope is already canonical.
func (s CertScope) IsZero() bool {
	return s.tenant == "" || s.issuer.IsZero() || s.device.IsZero()
}

// Equal reports whether s and other share the same isolation domain. It is the
// sanctioned cross-scope comparison for revocation-store implementations
// enforcing "绝不凭裸 serial 跨隔离域" (a stored cert's scope must Equal the
// caller's scope before its serial is visible).
func (s CertScope) Equal(other CertScope) bool {
	return s.tenant == other.tenant && s.issuer == other.issuer && s.device == other.device
}

// SubjectAltNames is the sealed set of certificate Subject Alternative Names
// (DNS / IP / URI). SAN forgery is a real escalation surface, so SANs are an
// explicit sealed request input — the Signer takes them from the (constrained)
// request, never blindly from the CSR. The fields are unexported;
// [NewSubjectAltNames] is the sole minter.
type SubjectAltNames struct {
	dnsNames []string
	ipAddrs  []net.IP
	uris     []*url.URL
}

// NewSubjectAltNames returns a sealed SAN set. Empty is allowed (a request may
// carry no SANs); individual DNS entries must be non-blank, IP entries must be a
// valid (non-empty) net.IP, and URI entries must be non-nil. The entries are
// DEEP copied (net.IP byte slices and *url.URL values, not just the outer
// slices) so the sealed value cannot be mutated through an aliased element.
func NewSubjectAltNames(dnsNames []string, ipAddrs []net.IP, uris []*url.URL) (SubjectAltNames, error) {
	for _, d := range dnsNames {
		if strings.TrimSpace(d) == "" {
			return SubjectAltNames{}, errCertRequestInvalid("dns SAN must not be blank")
		}
	}
	for _, ip := range ipAddrs {
		if len(ip) == 0 {
			return SubjectAltNames{}, errCertRequestInvalid("ip SAN must not be empty")
		}
	}
	for _, u := range uris {
		if u == nil {
			return SubjectAltNames{}, errCertRequestInvalid("uri SAN must not be nil")
		}
	}
	return SubjectAltNames{
		dnsNames: append([]string(nil), dnsNames...),
		ipAddrs:  deepCopyIPs(ipAddrs),
		uris:     deepCopyURLs(uris),
	}, nil
}

// deepCopyIPs copies the outer slice AND each net.IP's bytes, so a caller cannot
// mutate the sealed SAN set through a retained net.IP element.
func deepCopyIPs(in []net.IP) []net.IP {
	if len(in) == 0 {
		return nil
	}
	out := make([]net.IP, len(in))
	for i, ip := range in {
		out[i] = append(net.IP(nil), ip...)
	}
	return out
}

// deepCopyURLs copies the outer slice AND each *url.URL (by value), so a caller
// cannot mutate the sealed SAN set through a retained URL pointer.
func deepCopyURLs(in []*url.URL) []*url.URL {
	if len(in) == 0 {
		return nil
	}
	out := make([]*url.URL, len(in))
	for i, u := range in {
		dup := *u
		out[i] = &dup
	}
	return out
}

// DNSNames returns a copy of the DNS SANs.
func (s SubjectAltNames) DNSNames() []string { return append([]string(nil), s.dnsNames...) }

// IPAddresses returns a deep copy of the IP SANs.
func (s SubjectAltNames) IPAddresses() []net.IP { return deepCopyIPs(s.ipAddrs) }

// URIs returns a deep copy of the URI SANs.
func (s SubjectAltNames) URIs() []*url.URL { return deepCopyURLs(s.uris) }

// IsEmpty reports whether the SAN set carries no entries.
func (s SubjectAltNames) IsEmpty() bool {
	return len(s.dnsNames) == 0 && len(s.ipAddrs) == 0 && len(s.uris) == 0
}

// KeyUsages is the sealed set of X.509 key usages requested for a certificate.
// The fields are unexported; [NewKeyUsages] is the sole minter. A zero usage
// (no key-usage bits) is rejected — a certificate with no usage is unusable.
type KeyUsages struct {
	keyUsage    x509.KeyUsage
	extKeyUsage []x509.ExtKeyUsage
}

// NewKeyUsages returns a sealed KeyUsages from an x509.KeyUsage bitmask plus
// optional extended usages. A zero keyUsage is rejected fail-closed.
func NewKeyUsages(keyUsage x509.KeyUsage, extKeyUsage ...x509.ExtKeyUsage) (KeyUsages, error) {
	if keyUsage == 0 {
		return KeyUsages{}, errCertRequestInvalid("key usage must not be empty")
	}
	return KeyUsages{
		keyUsage:    keyUsage,
		extKeyUsage: append([]x509.ExtKeyUsage(nil), extKeyUsage...),
	}, nil
}

// IsZero reports whether k is the invalid zero value (no key-usage bits).
func (k KeyUsages) IsZero() bool { return k.keyUsage == 0 }

// KeyUsage returns the requested x509.KeyUsage bitmask.
func (k KeyUsages) KeyUsage() x509.KeyUsage { return k.keyUsage }

// ExtKeyUsage returns a copy of the requested extended key usages.
func (k KeyUsages) ExtKeyUsage() []x509.ExtKeyUsage {
	return append([]x509.ExtKeyUsage(nil), k.extKeyUsage...)
}

// DeviceSubject is the sealed certificate subject identity (the device + tenant
// dimension plus the X.509 common name). The fields are unexported;
// [NewDeviceSubject] is the sole minter. The subject is a policy decision made
// at the enroll boundary, not trusted from the CSR.
type DeviceSubject struct {
	tenant     tenant.TenantID
	device     DeviceID
	commonName string
}

// NewDeviceSubject returns a sealed DeviceSubject. The tenant must be canonical,
// the device non-zero, and the common name non-blank and within bounds.
func NewDeviceSubject(t tenant.TenantID, device DeviceID, commonName string) (DeviceSubject, error) {
	if err := t.Validate(); err != nil {
		return DeviceSubject{}, errcode.Wrap(errcode.KindInvalid, errCertRequestInvalidCode,
			msgSubjectTenantInvalid, err)
	}
	if device.IsZero() {
		return DeviceSubject{}, errCertRequestInvalid("subject device must not be empty")
	}
	if commonName == "" {
		return DeviceSubject{}, errCertRequestInvalid("subject common name must not be empty")
	}
	if len(commonName) > maxCommonNameLen {
		return DeviceSubject{}, errCertRequestInvalid("subject common name too long")
	}
	return DeviceSubject{tenant: t, device: device, commonName: commonName}, nil
}

// Tenant returns the subject tenant.
func (d DeviceSubject) Tenant() tenant.TenantID { return d.tenant }

// Device returns the subject device.
func (d DeviceSubject) Device() DeviceID { return d.device }

// CommonName returns the subject X.509 common name.
func (d DeviceSubject) CommonName() string { return d.commonName }

// IsZero reports whether d is the invalid zero value.
func (d DeviceSubject) IsZero() bool {
	return d.tenant == "" || d.device.IsZero() || d.commonName == ""
}

// CertRequest is the sealed protocol-agnostic signing request: an isolation
// scope, the policy-decided subject / SANs / usages / TTL, and the raw PKCS#10
// CSR DER (public key + proof-of-possession; the private key never crosses this
// boundary). EST / SCEP / XCEP front-ends decode to this single shape. All
// fields are unexported; [NewCertRequest] is the sole minter, so an external
// package cannot forge signing inputs by composite literal.
type CertRequest struct {
	scope   CertScope
	subject DeviceSubject
	csrDER  []byte
	sans    SubjectAltNames
	usages  KeyUsages
	ttl     time.Duration
}

// NewCertRequest validates and returns a sealed CertRequest. The scope and
// subject must be non-zero, the CSR DER must parse as a PKCS#10
// CertificateRequest, and the TTL must be positive. The CSR is defensively
// copied. Validation is fail-closed: a malformed request never reaches a Signer.
func NewCertRequest(
	scope CertScope,
	subject DeviceSubject,
	csrDER []byte,
	sans SubjectAltNames,
	usages KeyUsages,
	ttl time.Duration,
) (CertRequest, error) {
	if scope.IsZero() {
		return CertRequest{}, errCertRequestInvalid("scope must not be empty")
	}
	if subject.IsZero() {
		return CertRequest{}, errCertRequestInvalid("subject must not be empty")
	}
	if len(csrDER) == 0 {
		return CertRequest{}, errCertRequestInvalid("csr must not be empty")
	}
	if _, err := x509.ParseCertificateRequest(csrDER); err != nil {
		return CertRequest{}, errcode.Wrap(errcode.KindInvalid, errCertRequestInvalidCode,
			msgCSRUnparseable, err)
	}
	if usages.IsZero() {
		return CertRequest{}, errCertRequestInvalid("key usage must not be empty")
	}
	if ttl <= 0 {
		return CertRequest{}, errCertRequestInvalid("ttl must be positive")
	}
	return CertRequest{
		scope:   scope,
		subject: subject,
		csrDER:  append([]byte(nil), csrDER...),
		sans:    sans,
		usages:  usages,
		ttl:     ttl,
	}, nil
}

// Scope returns the request isolation scope.
func (r CertRequest) Scope() CertScope { return r.scope }

// Subject returns the requested certificate subject.
func (r CertRequest) Subject() DeviceSubject { return r.subject }

// CSRDER returns a copy of the raw PKCS#10 CSR DER.
func (r CertRequest) CSRDER() []byte { return append([]byte(nil), r.csrDER...) }

// SubjectAltNames returns the requested SAN set.
func (r CertRequest) SubjectAltNames() SubjectAltNames { return r.sans }

// KeyUsages returns the requested key usages.
func (r CertRequest) KeyUsages() KeyUsages { return r.usages }

// TTL returns the requested certificate lifetime.
func (r CertRequest) TTL() time.Duration { return r.ttl }

// IssuedCert is the sealed result of a signing operation (a minted credential):
// the certificate DER, its chain, and the renewal epoch. The canonical material
// is the opaque DER; the serial / notBefore / notAfter are DERIVED from it at
// construction (single source — an issued cert's serial cannot disagree with
// its bytes). All fields are unexported; [NewIssuedCert] is the sole minter, the
// upstream-Hard half of CERT-SIGN-FUNNEL-01 (no package outside can forge an
// issued certificate by composite literal).
type IssuedCert struct {
	scope     CertScope
	serial    Serial
	certDER   []byte
	chainDER  [][]byte
	notBefore time.Time
	notAfter  time.Time
	epoch     uint64
}

// NewIssuedCert returns a sealed IssuedCert from the signed certificate DER, its
// intermediate chain DER, the isolation scope, and the renewal epoch. The DER is
// parsed to derive (and validate) the serial / notBefore / notAfter — so the
// identity is single-sourced from the bytes. Empty / unparseable DER is rejected
// fail-closed. The byte slices are defensively copied.
func NewIssuedCert(scope CertScope, certDER []byte, chainDER [][]byte, epoch uint64) (IssuedCert, error) {
	if scope.IsZero() {
		return IssuedCert{}, errCertIssuedInvalid("scope must not be empty")
	}
	if len(certDER) == 0 {
		return IssuedCert{}, errCertIssuedInvalid("certificate must not be empty")
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return IssuedCert{}, errcode.Wrap(errcode.KindInvalid, errCertIssuedInvalidCode,
			msgCertUnparseable, err)
	}
	serial, err := NewSerial(cert.SerialNumber.Text(16))
	if err != nil {
		return IssuedCert{}, err
	}
	chainCopy := make([][]byte, len(chainDER))
	for i, link := range chainDER {
		chainCopy[i] = append([]byte(nil), link...)
	}
	return IssuedCert{
		scope:     scope,
		serial:    serial,
		certDER:   append([]byte(nil), certDER...),
		chainDER:  chainCopy,
		notBefore: cert.NotBefore,
		notAfter:  cert.NotAfter,
		epoch:     epoch,
	}, nil
}

// Scope returns the issued certificate's isolation scope.
func (c IssuedCert) Scope() CertScope { return c.scope }

// Serial returns the certificate serial (derived from the DER).
func (c IssuedCert) Serial() Serial { return c.serial }

// DER returns a copy of the certificate DER.
func (c IssuedCert) DER() []byte { return append([]byte(nil), c.certDER...) }

// Chain returns a copy of the intermediate-chain DER links.
func (c IssuedCert) Chain() [][]byte {
	out := make([][]byte, len(c.chainDER))
	for i, link := range c.chainDER {
		out[i] = append([]byte(nil), link...)
	}
	return out
}

// NotBefore returns the certificate validity start (derived from the DER).
func (c IssuedCert) NotBefore() time.Time { return c.notBefore }

// NotAfter returns the certificate expiry (derived from the DER).
func (c IssuedCert) NotAfter() time.Time { return c.notAfter }

// Epoch returns the monotonic renewal epoch (0 for initial enrollment).
func (c IssuedCert) Epoch() uint64 { return c.epoch }

// Certificate parses and returns the certificate for full X.509 access. It is
// the convenience accessor over the canonical DER (re-parsed on demand) — the
// seam stores opaque bytes, not a *x509.Certificate, to stay protocol-neutral.
// Each call re-parses the DER; callers needing repeated full access (e.g. a
// lifecycle reconciler) should cache the returned value. The cheap identity
// fields ([IssuedCert.Serial] / [IssuedCert.NotAfter] / [IssuedCert.NotBefore])
// are pre-derived and need no re-parse.
func (c IssuedCert) Certificate() (*x509.Certificate, error) {
	cert, err := x509.ParseCertificate(c.certDER)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errCertIssuedInvalidCode,
			msgCertUnparseable, err)
	}
	return cert, nil
}
