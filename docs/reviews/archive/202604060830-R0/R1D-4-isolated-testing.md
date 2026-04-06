# OIDC isolated testing review

Scope: `src/adapters/oidc` only. I reviewed the implementation and its tests, then verified the package with `go test` and coverage tooling. I did not read any existing files under `docs/reviews/`.

## Coverage Snapshot

The package-level statement coverage is `74.0%` from `go test ./adapters/oidc -coverprofile=/tmp/oidc.cover.out`.

Function coverage is uneven:

- `Config.Validate` and `DefaultConfig` are fully covered.
- `Provider.Discover` reports `100%`, but that is driven by the happy path plus cache reuse, not the failure branches in `fetchDiscovery`.
- `ExchangeCode` is at `68.0%`.
- `GetUserInfo` is at `70.8%`.
- `Verifier.Verify` is at `81.2%`.
- `audienceMatch` is only `42.9%`, so the `[]any` audience shape is not exercised.
- `fetchDiscovery`, `fetchJWKS`, and `parseRSAPublicKey` all have partial coverage, which matches the lack of error-path tests.

Relevant code and test locations:

- [provider.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/provider.go#L57) and [oidc_test.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/oidc_test.go#L160)
- [token.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/token.go#L27) and [oidc_test.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/oidc_test.go#L185)
- [userinfo.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/userinfo.go#L25) and [oidc_test.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/oidc_test.go#L305)
- [verifier.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go#L67) and [oidc_test.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/oidc_test.go#L203)

## Findings

1. Integration coverage is not ready yet. [integration_test.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/integration_test.go#L1) is build-tagged, but every test body is just `t.Skip(...)` ([integration_test.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/integration_test.go#L12), [integration_test.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/integration_test.go#L18), [integration_test.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/integration_test.go#L24), [integration_test.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/integration_test.go#L30)). That means the package has no executable end-to-end validation against a real OIDC provider yet, so CI can go green without proving discovery, token exchange, JWKS verification, or userinfo flow against a live server.

2. Negative-case coverage is still thin around the adapter boundaries. The current tests hit a few representative failures, but most error branches in `fetchDiscovery`, `ExchangeCode`, `GetUserInfo`, and `Verify` remain untested. In particular, there is no coverage for request creation failures, transport failures, non-200 responses, malformed JSON, missing discovery endpoints, missing or unknown `kid`, wrong signing algorithm, issuer mismatch, JWKS parsing failures, or signature verification failures. The code paths are present in [provider.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/provider.go#L70), [token.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/token.go#L27), [userinfo.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/userinfo.go#L25), and [verifier.go](/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go#L155).

3. Some existing tests are too loose to catch regressions. [TestProvider_Discover](/Users/shengming/Documents/code/gocell/src/adapters/oidc/oidc_test.go#L160) checks returned fields, but it does not assert that the second call avoids a network fetch. [TestProvider_ExchangeCode](/Users/shengming/Documents/code/gocell/src/adapters/oidc/oidc_test.go#L185) does not inspect the posted form body, so a regression in `grant_type`, `redirect_uri`, `client_id`, or `client_secret` would still pass. [TestVerifier_Verify_ExpiredToken](/Users/shengming/Documents/code/gocell/src/adapters/oidc/oidc_test.go#L275) only asserts that an error occurred, not that it is mapped to `ErrAdapterOIDCVerify`, so error-code regressions could slip through.

## Verification Commands

I ran these commands from `src/`:

```bash
go test ./adapters/oidc -coverprofile=/tmp/oidc.cover.out
go tool cover -func=/tmp/oidc.cover.out
go test ./adapters/oidc -run Test -tags=integration -v
```

Results:

- `go test ./adapters/oidc -coverprofile=/tmp/oidc.cover.out` passed with `74.0%` statement coverage.
- `go tool cover -func=/tmp/oidc.cover.out` confirmed the gaps listed above.
- `go test ./adapters/oidc -run Test -tags=integration -v` passed only because all four integration tests are skipped stubs.
