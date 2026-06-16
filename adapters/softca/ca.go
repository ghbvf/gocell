package softca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
)

// CA lifetimes and identities. The root is the long-lived trust anchor; the
// intermediate is the (shorter-lived) issuing CA that signs leaf device
// certificates and CRLs — the standard PKI split that isolates the anchor from
// the online signing key.
const (
	rootCATTL         = 10 * 365 * 24 * time.Hour
	interCATTL        = 5 * 365 * 24 * time.Hour
	rootCACommonName  = "GoCell SoftCA Root"
	interCACommonName = "GoCell SoftCA Intermediate"

	serialBits = 159 // positive 159-bit random serial (RFC 5280 ≤ 20 octets)

	keyFilePerm  = 0o600
	certFilePerm = 0o644
)

// On-disk file names for the file-backed CA (see [NewFileCA]).
const (
	rootKeyFile  = "root.key"
	rootCertFile = "root.crt"
	intKeyFile   = "inter.key"
	intCertFile  = "inter.crt"
)

// CA is softca's two-tier signing authority (root → intermediate). The signing
// keys (crypto.Signer) live in UNEXPORTED fields and are consumed only inside
// this package (signer.go / revocation.go): CA exposes NO accessor returning a
// key or raw private material (CERT-PRIVATE-KEY-CUSTODY-01 — the key never
// crosses the certsigning seam into kernel/runtime).
type CA struct {
	clk clock.Clock

	rootCert *x509.Certificate
	rootDER  []byte
	rootKey  crypto.Signer

	interCert *x509.Certificate
	interDER  []byte
	interKey  crypto.Signer
}

// NewDevCA generates an ephemeral in-memory two-tier CA (ECDSA P-256). It is the
// zero-dependency dev/test default; the keys live only in this process and are
// lost on restart. For persistence across restarts use [NewFileCA].
//
// On restart the trust anchor CHANGES, so every previously issued device
// certificate becomes unverifiable and all enrolled devices must re-enroll — fine
// for dev/test, but a reason to choose [NewFileCA] (paired with a persistent
// [Ledger]) for anything longer-lived.
func NewDevCA(clk clock.Clock) (*CA, error) {
	clock.MustHaveClock(clk, "softca.NewDevCA")
	return bootstrapCA(clk)
}

// NewFileCA loads a two-tier CA from PEM files under dir (root.key / root.crt /
// inter.key / inter.crt), or — when none are present — bootstraps a fresh CA and
// persists it there (keys 0600, certs 0644). A PARTIAL set fails closed rather
// than silently overwriting or half-loading. This is the dev "file custody"
// option; hardware-backed (KMS / HSM) custody is a separate future adapter, not
// a silent no-op here.
func NewFileCA(clk clock.Clock, dir string) (*CA, error) {
	clock.MustHaveClock(clk, "softca.NewFileCA")
	present := countPresent(dir)
	switch present {
	case 4:
		return loadFileCA(clk, dir)
	case 0:
		ca, err := bootstrapCA(clk)
		if err != nil {
			return nil, err
		}
		if err := persistCA(dir, ca); err != nil {
			return nil, err
		}
		return ca, nil
	default:
		return nil, errCAInit("partial CA key material on disk", nil)
	}
}

// bootstrapCA generates a fresh root + intermediate hierarchy.
func bootstrapCA(clk clock.Clock) (*CA, error) {
	now := clk.Now()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errCAInit("root key generation failed", err)
	}
	rootCert, rootDER, err := selfSignRoot(now, rootKey)
	if err != nil {
		return nil, err
	}
	interKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errCAInit("intermediate key generation failed", err)
	}
	interCert, interDER, err := signIntermediate(now, rootCert, rootKey, interKey)
	if err != nil {
		return nil, err
	}
	return &CA{
		clk:       clk,
		rootCert:  rootCert,
		rootDER:   rootDER,
		rootKey:   rootKey,
		interCert: interCert,
		interDER:  interDER,
		interKey:  interKey,
	}, nil
}

// selfSignRoot mints a self-signed root CA certificate.
func selfSignRoot(now time.Time, key crypto.Signer) (*x509.Certificate, []byte, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, errCAInit("root serial generation failed", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: rootCACommonName},
		NotBefore:             now,
		NotAfter:              now.Add(rootCATTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1, // permits exactly one intermediate below the root
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, nil, errCAInit("root certificate creation failed", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, errCAInit("root certificate parse failed", err)
	}
	return cert, der, nil
}

// signIntermediate mints the intermediate issuing CA, signed by the root.
func signIntermediate(now time.Time, rootCert *x509.Certificate, rootKey, interKey crypto.Signer) (*x509.Certificate, []byte, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, errCAInit("intermediate serial generation failed", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: interCACommonName},
		NotBefore:             now,
		NotAfter:              now.Add(interCATTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0, // no further CA below the intermediate
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, rootCert, interKey.Public(), rootKey)
	if err != nil {
		return nil, nil, errCAInit("intermediate certificate creation failed", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, errCAInit("intermediate certificate parse failed", err)
	}
	return cert, der, nil
}

// trustBundle returns the trust anchors as DER, ordered leaf-issuer first
// (intermediate) to root last — the order [Signer.TrustBundle] / an EST /cacerts
// response is built from. Returns defensive copies.
func (ca *CA) trustBundle() [][]byte {
	return [][]byte{
		append([]byte(nil), ca.interDER...),
		append([]byte(nil), ca.rootDER...),
	}
}

// randomSerial returns a positive random certificate serial number. It returns
// the bare crypto/rand error so each caller can classify it in its own context
// (CA bootstrap → ErrCertCAInit; leaf signing → ErrCertSignFailed).
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), serialBits)
	return rand.Int(rand.Reader, limit)
}

// ── file custody ───────────────────────────────────────────────────────────

// countPresent returns how many of the four CA files exist under dir.
func countPresent(dir string) int {
	n := 0
	for _, name := range []string{rootKeyFile, rootCertFile, intKeyFile, intCertFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			n++
		}
	}
	return n
}

// loadFileCA reads and validates a CA hierarchy from PEM files under dir.
func loadFileCA(clk clock.Clock, dir string) (*CA, error) {
	rootKey, err := readKey(filepath.Join(dir, rootKeyFile))
	if err != nil {
		return nil, err
	}
	rootCert, rootDER, err := readCert(filepath.Join(dir, rootCertFile))
	if err != nil {
		return nil, err
	}
	interKey, err := readKey(filepath.Join(dir, intKeyFile))
	if err != nil {
		return nil, err
	}
	interCert, interDER, err := readCert(filepath.Join(dir, intCertFile))
	if err != nil {
		return nil, err
	}
	if !rootCert.IsCA || !interCert.IsCA {
		return nil, errCAInit("loaded certificate is not a CA", nil)
	}
	if err := interCert.CheckSignatureFrom(rootCert); err != nil {
		return nil, errCAInit("intermediate not signed by loaded root", err)
	}
	return &CA{
		clk:       clk,
		rootCert:  rootCert,
		rootDER:   rootDER,
		rootKey:   rootKey,
		interCert: interCert,
		interDER:  interDER,
		interKey:  interKey,
	}, nil
}

// persistCA writes the CA hierarchy to PEM files under dir.
func persistCA(dir string, ca *CA) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errCAInit("create ca directory failed", err)
	}
	// MkdirAll does not tighten an already-existing directory; force 0700 so a
	// pre-created 0755 dir cannot leave the key file names world-listable. 0700 is
	// a DIRECTORY mode (traverse needs the execute bit); the key files themselves
	// are written 0600.
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: 0700 is a directory mode, not a file mode; keys are 0600
		return errCAInit("tighten ca directory perms failed", err)
	}
	if err := writeKey(filepath.Join(dir, rootKeyFile), ca.rootKey); err != nil {
		return err
	}
	if err := writeCert(filepath.Join(dir, rootCertFile), ca.rootDER); err != nil {
		return err
	}
	if err := writeKey(filepath.Join(dir, intKeyFile), ca.interKey); err != nil {
		return err
	}
	return writeCert(filepath.Join(dir, intCertFile), ca.interDER)
}

// readKey loads a PKCS#8 PEM private key as a crypto.Signer.
func readKey(path string) (crypto.Signer, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // dev file-custody path supplied by the operator
	if err != nil {
		return nil, errCAInit("read key file failed", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errCAInit("key file is not PEM", nil)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errCAInit("parse pkcs8 key failed", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errCAInit("key does not implement crypto.Signer", nil)
	}
	return signer, nil
}

// writeKey persists a crypto.Signer as a PKCS#8 PEM private key (0600).
func writeKey(path string, key crypto.Signer) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return errCAInit("marshal pkcs8 key failed", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, keyFilePerm); err != nil {
		return errCAInit("write key file failed", err)
	}
	return nil
}

// readCert loads a PEM certificate, returning the parsed cert and its DER.
func readCert(path string) (*x509.Certificate, []byte, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // dev file-custody path supplied by the operator
	if err != nil {
		return nil, nil, errCAInit("read cert file failed", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, nil, errCAInit("cert file is not PEM", nil)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, errCAInit("parse cert failed", err)
	}
	return cert, block.Bytes, nil
}

// writeCert persists a DER certificate as PEM (0644).
func writeCert(path string, der []byte) error {
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, certFilePerm); err != nil {
		return errCAInit("write cert file failed", err)
	}
	return nil
}
