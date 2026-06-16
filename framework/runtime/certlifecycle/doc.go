// Package certlifecycle is the reusable server-side certificate-lifecycle
// reconcile.Reconciler. It periodically sweeps a cell's renewable device
// certificates, and for each one due for renewal (k8s 70–90% jitter) it
// Authorizes (fail-closed), re-signs the device's STORED CSR via the
// certsigning.Signer seam, and persists the new generation through the reconcile
// FencedWriter — the consumer's fenced write also lands the cert-issued L2 fact
// atomically with the row.
//
// It REUSES kernel/reconcile.Loop (no new control loop) and generalizes the
// iotdevice devicecertrenewal seed's loop shape, changing the action from
// "enqueue a rotate-cert command (the device self-renews)" to server-side
// signing. It is the L4 device-latent convergence archetype: a renewed cert is
// persisted server-side; the device refetches it via the status endpoint (the
// cert-issued event is metadata-only — no key material on the bus).
//
// # Layering
//
// runtime layer: depends on kernel (reconcile, clock), pkg (errcode, tenant,
// validation), and the runtime/certsigning INTERFACES (Signer, Authorizer). It
// does NOT depend on corecells or adapters, and in particular never imports a
// concrete CA (adapters/softca) — it works only through the certsigning seam.
// It deliberately does NOT import kernel/outbox or the generated contract types:
// the cert-issued L2 fact is written transactionally inside the consumer's
// ApplyFenced (see "cert-issued L2 emit" below), not by this package.
//
// # System identity / multi-tenant
//
// The reconcile Loop installs a tenantless system producer identity at its
// chokepoint (#1821): actor/subject="system", tenant cleared. So this Reconciler
// MUST source the tenant from each scanned Candidate row (Candidate.TenantID),
// never from ctx. The package declares no tenancy stance — that is the
// construction site's job: reconcile.New(r, reconcile.SingleTenant()) for a
// single-tenant deployment, or reconcile.TenantScoped() for a multi-tenant one
// (RECONCILE-TENANCY-DECLARED-01). Because the tenant rides on the row, a
// multi-tenant sweep keeps tenants distinct without an ambient ctx tenant.
//
// # cert-issued L2 emit is atomic with the fenced persist (not a separate Emit)
//
// The Reconciler does NOT call outbox.Emit. L2 (local tx + outbox) requires the
// certificate-row write and the cert-issued outbox entry to commit in ONE
// transaction, and the only transaction is the consumer's. So the Reconciler's
// single fenced write (IssuedMutation) carries every cert-issued field, and the
// consumer's DeviceCertRepository.ApplyFenced persists the row AND writes the
// cert-issued L2 outbox entry atomically. This is the FencedRepository-sanctioned
// "device writes / command emission from inside Reconcile" via the single fenced
// surface — and it is also forced by module topology: the framework module
// cannot import the generated contract types (separate module). The Reconciler is
// the emit DRIVER (it produces the data and triggers the fenced write); the
// consumer cell is the L2 atomic EXECUTOR.
//
// # Enforced invariants
//
// CERTLIFECYCLE-SIGN-VIA-FUNNEL-01. This Reconciler obtains issued certificates
// EXCLUSIVELY via certsigning.Signer.Sign — it never mints an IssuedCert nor
// reaches into a concrete CA. The SECURITY PROPERTY is Hard by inheritance, no
// new Hard mechanism required:
//   - certsigning.IssuedCert has only unexported fields, so this package cannot
//     forge one by composite literal (compile-time Hard,
//     CERT-VALUE-SEALED-CONSTRUCTION-01);
//   - the sole minter certsigning.NewIssuedCert is restricted by the existing
//     CERT-SIGN-FUNNEL-01 caller-allowlist to {adapters/softca}, which excludes
//     certlifecycle;
//   - certlifecycle cannot import adapters/softca at all: softca is a separate Go
//     module that requires the framework module, so the reverse import is a
//     circular module dependency the build refuses (module-topology Hard).
//
// Together these make Signer.Sign the only compilable way for this package to
// obtain an IssuedCert. The archtest of the same name is the Medium reverse
// self-check / anti-vacuity (it asserts the package actually calls Signer.Sign,
// does not call NewIssuedCert, and is absent from the mint allowlist) — see
// tools/archtest/certlifecycle_invariants_test.go.
//
// RECONCILE-FENCED-WRITE-FUNNEL-01 (Hard, reused). The renewed certificate is
// persisted EXCLUSIVELY through reconcile.FencedWriterFrom(ctx).Write; this
// package never calls DeviceCertRepository.ApplyFenced directly. The fenced
// writer's monotonic lease-epoch CAS is the cross-replica at-most-once guarantee.
//
// Why not runtime/command: the issue's "reuse runtime/command" is satisfied at
// the reconcile-loop archetype level; the "queue active-uniqueness, at most one
// valid signing" requirement is realized structurally by the fencing CAS, not a
// command queue. softca signs synchronously (<10ms), so an async sign command
// would only re-introduce the control loop this package is told not to build.
package certlifecycle
