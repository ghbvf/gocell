// Package celltls is the topology-gated resolver for split-topology cross-cell
// mTLS material (#2263). It is the SOLE sanctioned producer of the cell's client
// mTLS identity ([tlsutil.ClientIdentity]) and server mTLS config (*tls.Config),
// mirroring the sibling resolvers cellmodules/{replaydeps,eventtransport,
// sagaprojectiondeps} (CELLTLS-MATERIAL-FUNNEL-01).
//
// # Material
//
// A cell's transport identity is operator-provisioned PEM (sealed under #2263
// scope to static provisioning; automatic issuance/rotation via the existing
// runtime/certlifecycle reconciler is a documented follow-up):
//
//   - GOCELL_TRANSPORT_TLS_CERT_FILE — this cell's leaf certificate (PEM). Its
//     URI SAN MUST be spiffe://<trustDomain>/cell/<thisCell>, and it MUST carry
//     both ServerAuth and ClientAuth EKUs (a cell is both a peer server and a
//     peer client).
//   - GOCELL_TRANSPORT_TLS_KEY_FILE — the matching private key (PEM).
//   - GOCELL_TRANSPORT_TLS_CA_FILE — the trust-root bundle (PEM) that signs every
//     cell cert. Used as the client RootCAs (verify the peer we dial) AND the
//     server ClientCAs (verify the peer dialing us) — symmetric single root.
//   - GOCELL_SPIFFE_TRUST_DOMAIN — the SPIFFE trust domain.
//
// All four are all-or-nothing: a partial set is a fail-closed configuration error.
//
// # Fail-closed gate (no soft fallback)
//
// [Resolve] fails closed when the deployment topology has a non-loopback remote
// cell (topo.HasNonLoopbackRemoteCells) but no TLS material is configured: a
// process that will dial a peer across a real network boundary must not start in
// plaintext. A loopback-only split (local multi-process dev) or all-colocated
// topology stays plaintext-eligible. When material IS present it is always
// honored (operator opt-in), regardless of topology.
//
// This removes the previous "plaintext + private-network compensation" soft
// fallback for non-loopback split (#2263): mTLS is mandatory there, enforced here
// at startup and, per-peer, again in cellmodules/celltransport.Resolve.
package celltls
