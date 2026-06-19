// Package tlsutiltest is the single sanctioned source of self-signed mTLS cert
// material for GoCell tests: a self-signed ECDSA P-256 root CA and the cell /
// transport leaves it issues (SPIFFE-URI / DNS / IP SANs, ServerAuth /
// ClientAuth EKU).
//
// It is an exported test-support package (imports testing, takes *testing.T),
// mirroring framework/kernel/reconcile/reconciletest and the broader
// {pkg}test convention. ref: net/http → net/http/httptest.
//
// # Why it exists (issue #2287, F13)
//
// Before this package, 9 near-equivalent cert-gen helpers were copy-pasted
// across the test tree — 8 "self-signed CA + cell leaf" chain helpers (tlsutil
// client + server, middleware, transport, celltls, celltransport, adapters/grpc,
// adapters/mqtt) plus a CA-only cert-pool fixture (adapters/mqtt config_test).
// cert-gen detail — curve, validity window, EKU set, SAN shape — is sensitive to
// mTLS correctness: a bug in any one copy is invisible to the others and a
// change must be synced N ways. Centralizing the minting here makes the cert
// shape a single source. The TLS-TEST-MATERIAL-FUNNEL-01 archtest keeps it that
// way by banning x509.CreateCertificate in the test tree outside this package
// (plus the cert-issuance-pipeline tests that fabricate certs as
// system-under-test input — certsigning / certlifecycle).
//
// Framework consumers (including examples/* projects) write their mTLS tests by
// importing this package directly — it is the sanctioned source, so no
// allowlist exemption is needed.
//
// # Funnel boundary — material only, no provisioning
//
// This package emits raw PEM material (CertPEM / KeyPEM / a *x509.CertPool /
// a ready tls.Certificate) and never calls the provisioning constructors
// tlsutil.NewClientIdentity / tlsutil.NewServerMTLSConfig. Those are
// restricted by CELLTLS-MATERIAL-FUNNEL-01 to cellmodules/celltls and
// adapters/grpc production code; its scanner exempts _test.go files but not a
// non-test importable package like this one. So callers that need a
// ClientIdentity or a server *tls.Config wrap this package's PEM output in
// their own _test.go (the funnel-exempt site). Keeping the boundary here means
// adding this package required zero change to the CELLTLS allowlist.
package tlsutiltest
