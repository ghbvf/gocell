package cell

// repo_readiness_test.go is intentionally empty.
//
// The tests for RegisterRepoReadiness and RepoHealthProber were removed when
// those symbols were deleted from this package. The replacement — per-cell
// cellgen-emitted RegisterRepoReady helpers using healthz.RepoProber — will
// be tested in the B6/B8 batches.
