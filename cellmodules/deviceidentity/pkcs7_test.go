package deviceidentity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"testing"
	"time"
)

// makeSelfSignedDER generates a minimal self-signed ECDSA P-256 certificate
// and returns its DER-encoded bytes.
func makeSelfSignedDER(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return der
}

// parseContentInfo decodes the outer ContentInfo and returns the raw content bytes
// plus the contentType OID, so we can inspect the SignedData inside.
type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,tag:0"`
}

type signedData struct {
	Version          int
	DigestAlgorithms asn1.RawValue   `asn1:"set"`
	EncapContentInfo asn1.RawValue   // SEQUENCE
	Certificates     []asn1.RawValue `asn1:"optional,tag:0,implicit,set"`
	SignerInfos      asn1.RawValue   `asn1:"set"`
}

var (
	oidData       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
)

func TestDegenerateCertsOnly(t *testing.T) {
	t.Parallel()

	cert1 := makeSelfSignedDER(t, "device-1")
	cert2 := makeSelfSignedDER(t, "device-2")
	cert3 := makeSelfSignedDER(t, "device-3")

	tests := []struct {
		name     string
		input    [][]byte
		wantErr  bool
		wantNDER int // expected number of certs embedded (only checked when !wantErr)
	}{
		{
			name:     "single cert round-trip",
			input:    [][]byte{cert1},
			wantNDER: 1,
		},
		{
			name:     "two certs round-trip",
			input:    [][]byte{cert1, cert2},
			wantNDER: 2,
		},
		{
			name:     "three certs round-trip",
			input:    [][]byte{cert1, cert2, cert3},
			wantNDER: 3,
		},
		{
			name:    "empty input returns error",
			input:   [][]byte{},
			wantErr: true,
		},
		{
			name:    "nil input returns error",
			input:   nil,
			wantErr: true,
		},
		{
			name:    "corrupt DER returns error",
			input:   [][]byte{[]byte("this is not a certificate")},
			wantErr: true,
		},
		{
			name:    "one good one corrupt returns error",
			input:   [][]byte{cert1, []byte("bad")},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := degenerateCertsOnly(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) == 0 {
				t.Fatal("expected non-empty DER output")
			}

			// Decode the outer ContentInfo.
			var ci contentInfo
			rest, err2 := asn1.Unmarshal(got, &ci)
			if err2 != nil {
				t.Fatalf("unmarshal ContentInfo: %v", err2)
			}
			if len(rest) != 0 {
				t.Errorf("trailing bytes after ContentInfo: %d", len(rest))
			}

			// Verify contentType = id-signedData.
			if !ci.ContentType.Equal(oidSignedData) {
				t.Errorf("ContentType = %v, want %v", ci.ContentType, oidSignedData)
			}

			// Decode the SignedData from the EXPLICIT [0] content bytes.
			var sd signedData
			if _, err3 := asn1.Unmarshal(ci.Content.Bytes, &sd); err3 != nil {
				t.Fatalf("unmarshal SignedData: %v", err3)
			}

			// version must be 1.
			if sd.Version != 1 {
				t.Errorf("SignedData.version = %d, want 1", sd.Version)
			}

			// certificates SET must contain exactly the input DERs.
			// DER SET elements are sorted by the encoder, so we use a set
			// (map) comparison rather than index-order comparison.
			if len(sd.Certificates) != tc.wantNDER {
				t.Errorf("certificates count = %d, want %d", len(sd.Certificates), tc.wantNDER)
			}
			inputSet := make(map[string]struct{}, len(tc.input))
			for _, der := range tc.input {
				inputSet[string(der)] = struct{}{}
			}
			for idx, rv := range sd.Certificates {
				if _, ok := inputSet[string(rv.FullBytes)]; !ok {
					t.Errorf("certificate[%d] DER not found in input set", idx)
				}
			}
		})
	}
}

// TestDegenerateCertsOnly_SignedDataOIDEmbedded verifies the encapContentInfo
// eContentType is id-data (1.2.840.113549.1.7.1) as required by RFC 7030 §4.1.3.
func TestDegenerateCertsOnly_EncapContentInfoOID(t *testing.T) {
	t.Parallel()

	cert := makeSelfSignedDER(t, "enc-oid-test")
	der, err := degenerateCertsOnly([][]byte{cert})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var ci contentInfo
	if _, err2 := asn1.Unmarshal(der, &ci); err2 != nil {
		t.Fatalf("unmarshal ContentInfo: %v", err2)
	}
	var sd signedData
	if _, err3 := asn1.Unmarshal(ci.Content.Bytes, &sd); err3 != nil {
		t.Fatalf("unmarshal SignedData: %v", err3)
	}

	// Decode EncapContentInfo to get the eContentType OID.
	var eci struct {
		EContentType asn1.ObjectIdentifier
	}
	if _, err4 := asn1.Unmarshal(sd.EncapContentInfo.FullBytes, &eci); err4 != nil {
		t.Fatalf("unmarshal EncapContentInfo: %v", err4)
	}
	if !eci.EContentType.Equal(oidData) {
		t.Errorf("eContentType = %v, want id-data %v", eci.EContentType, oidData)
	}
}
