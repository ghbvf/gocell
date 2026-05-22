package cell

// repo_readiness.go is intentionally empty.
//
// RepoHealthProber (formerly here) has been replaced by kernel/healthz.RepoProber.
// RegisterRepoReadiness (formerly here) has been deleted; per-cell typed
// RegisterRepoReady helpers are emitted by cellgen (B6). Cell call sites have
// been migrated to use the cellgen-generated RegisterRepoReady (B8).
//
// cell.HealthProber (formerly in registry.go) has been deleted; callers that
// used the old Probes() map-drain pattern have been migrated to healthz.ProbeSet
// and RegisterEmitterProbes (B8).
