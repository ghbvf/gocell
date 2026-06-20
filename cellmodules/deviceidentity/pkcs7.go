// Package deviceidentity provides helpers for the EST (RFC 7030) device
// certificate enrollment and renewal service.
package deviceidentity

// ref: RFC 5652 §5.1  — CMS SignedData
// ref: RFC 7030 §4.1.3 — EST /simpleenroll and /simplereenroll response format

import (
	"crypto/x509"
	"encoding/asn1"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// ASN.1 OIDs required by PKCS#7 / CMS.
var (
	// pkcs7OIDData is id-data (1.2.840.113549.1.7.1) used as the eContentType of
	// degenerate SignedData that carries only certificates.
	pkcs7OIDData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	// pkcs7OIDSignedData is id-signedData (1.2.840.113549.1.7.2).
	pkcs7OIDSignedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
)

// encapContentInfo represents the EncapsulatedContentInfo structure from
// RFC 5652 §5.2. For a degenerate certs-only PKCS#7, the eContent is absent.
type encapContentInfo struct {
	EContentType asn1.ObjectIdentifier
	// eContent is intentionally omitted — degenerate SignedData has no content.
}

// pkcs7SignedData is the ASN.1 layout of RFC 5652 §5.1 SignedData restricted
// to the degenerate (certs-only) case required by RFC 7030 §4.1.3.
//
// Fields are ordered to match the ASN.1 SEQUENCE definition:
//
//	SignedData ::= SEQUENCE {
//	    version          CMSVersion,
//	    digestAlgorithms DigestAlgorithmIdentifiers,  -- empty SET
//	    encapContentInfo EncapsulatedContentInfo,
//	    certificates     [0] IMPLICIT CertificateSet OPTIONAL,
//	    crls             [1] IMPLICIT RevocationInfoChoices OPTIONAL,  -- omitted
//	    signerInfos      SignerInfos  -- empty SET
//	}
type pkcs7SignedData struct {
	Version          int
	DigestAlgorithms []asn1.RawValue `asn1:"set"`
	EncapContentInfo encapContentInfo
	Certificates     []asn1.RawValue `asn1:"optional,tag:0,implicit,set"`
	SignerInfos      []asn1.RawValue `asn1:"set"`
}

// pkcs7ContentInfo is the RFC 5652 §3 ContentInfo wrapper.
//
//	ContentInfo ::= SEQUENCE {
//	    contentType ContentType,
//	    content     [0] EXPLICIT ANY DEFINED BY contentType
//	}
//
// The Content field uses a bare asn1.RawValue (no struct tag) so that we can
// supply a pre-tagged ClassContextSpecific [0] CONSTRUCTED value directly.
// encoding/asn1 embeds RawValue.FullBytes verbatim inside the SEQUENCE, which
// is the correct behavior for this field.
type pkcs7ContentInfo struct {
	ContentType asn1.ObjectIdentifier
	// Content holds the [0] EXPLICIT context wrapper around the SignedData
	// SEQUENCE. We provide a RawValue with Class=ContextSpecific, Tag=0,
	// IsCompound=true, Bytes=<SignedData DER> so that asn1.Marshal emits:
	//   a0 <len> <SignedData SEQUENCE bytes>
	// without a separate intermediate Marshal step.
	Content asn1.RawValue
}

const (
	msgEmptyCertsDER   = "certsDER must not be empty"
	msgCertParseFailed = "certsDER contains an invalid DER-encoded certificate"
)

// degenerateCertsOnly builds an RFC 5652 §5.1 / RFC 7030 §4.1.3 degenerate
// PKCS#7 SignedData structure that carries only certificates (no signers).
//
// The returned bytes are a DER-encoded ContentInfo wrapping a SignedData with:
//   - version 1
//   - digestAlgorithms: empty SET
//   - encapContentInfo: {eContentType=id-data, no eContent}
//   - certificates: [0] IMPLICIT SET containing the supplied DER certs
//   - signerInfos: empty SET
//
// This is the wire format produced by openssl and cert-manager for EST
// /simpleenroll and /simplereenroll responses (application/pkcs7-mime;
// smime-type=certs-only).
//
// certsDER must contain at least one DER-encoded X.509 certificate; each entry
// is validated by parsing with crypto/x509 before marshaling. An error is
// returned if the slice is empty or any entry fails to parse.
func degenerateCertsOnly(certsDER [][]byte) ([]byte, error) {
	if len(certsDER) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgEmptyCertsDER)
	}

	certs := make([]asn1.RawValue, 0, len(certsDER))
	for _, der := range certsDER {
		if _, err := x509.ParseCertificate(der); err != nil {
			return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				msgCertParseFailed, err)
		}
		// Each certificate is embedded as-is: FullBytes preserves the original
		// DER so the tag/class bytes from the Certificate SEQUENCE are kept
		// verbatim inside the IMPLICIT [0] SET.
		certs = append(certs, asn1.RawValue{FullBytes: der})
	}

	sdBytes, err := asn1.Marshal(pkcs7SignedData{
		Version:          1,
		DigestAlgorithms: []asn1.RawValue{},
		EncapContentInfo: encapContentInfo{EContentType: pkcs7OIDData},
		Certificates:     certs,
		SignerInfos:      []asn1.RawValue{},
	})
	if err != nil {
		// Unreachable with our static struct types; retained for defensive coverage.
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"failed to marshal PKCS7 SignedData", err)
	}

	// Build the ContentInfo where Content is a [0] EXPLICIT context wrapper
	// (ClassContextSpecific, tag 0, constructed) holding the SignedData bytes.
	// Using RawValue with Class+Tag+Bytes directly avoids an intermediate
	// asn1.Marshal call and matches what encoding/asn1 emits for an explicit
	// context-tagged ANY field.
	ciBytes, err := asn1.Marshal(pkcs7ContentInfo{
		ContentType: pkcs7OIDSignedData,
		Content: asn1.RawValue{
			Class:      asn1.ClassContextSpecific,
			Tag:        0,
			IsCompound: true,
			Bytes:      sdBytes,
		},
	})
	if err != nil {
		// Unreachable with our static struct types; retained for defensive coverage.
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"failed to marshal PKCS7 ContentInfo", err)
	}

	return ciBytes, nil
}
