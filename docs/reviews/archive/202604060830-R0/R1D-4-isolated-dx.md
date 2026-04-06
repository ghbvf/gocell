# R1D-4: `adapters/oidc` Isolated DX Review

Scope: `src/adapters/oidc/*` plus the current config/integration docs that describe its public surface. I did not read or reuse any existing review/report file under `docs/reviews/`.

Baseline: `go test ./adapters/oidc/...` passes.

## Findings

### 1. [P1] Constructors allow half-configured or nil-backed objects that fail late

`NewProvider` only validates `IssuerURL` and `ClientID`, but `ExchangeCode` later relies on `RedirectURL` and `ClientSecret` being populated. That means a caller can successfully construct a provider with `DefaultConfig(...)`, pass `NewProvider`, and only discover the missing auth-code inputs when the first token exchange runs. The same pattern exists in `NewVerifier`: it stores `provider` without a nil guard, so `NewVerifier(nil)` produces a verifier that will panic when `Verify` reaches `v.provider.Discover(ctx)`.

Evidence:
- [`src/adapters/oidc/config.go:31`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/config.go:31)
- [`src/adapters/oidc/config.go:43`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/config.go:43)
- [`src/adapters/oidc/provider.go:38`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/provider.go:38)
- [`src/adapters/oidc/token.go:38`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/token.go:38)
- [`src/adapters/oidc/verifier.go:57`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go:57)
- [`src/adapters/oidc/verifier.go:67`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go:67)

Why this matters: this is a classic DX footgun. The API surface looks constructor-safe, but the object can still be invalid for the most common login flow. Failures land at runtime, far from the call site that created the bad config, and the nil-provider case can crash the process instead of returning an `errcode`-wrapped error.

Recommended shape: either make `Validate()` enforce the auth-code requirements that `ExchangeCode()` actually needs, or split discovery-only and auth-code configs so the constructor name makes the contract obvious. `NewVerifier` should also reject `nil` explicitly.

### 2. [P2] The exported config/doc surface has drifted from what the code actually honors

The `Config` struct exports `Scopes`, `JWKSCacheTTL`, `DiscoveryCacheTTL`, and `HTTPTimeout`, but only `DiscoveryCacheTTL` and `HTTPTimeout` are materially used today. `Scopes` is never read, and `JWKSCacheTTL` is not consulted anywhere in the verifier cache path. At the same time, the current config reference documents `issuerURL`, `clientID`, `clientSecret`, `redirectURL`, `scopes`, `discoveryTimeout`, and `jwksRefreshInterval`, which does not match the actual Go API names or the real behavior. In practice, this means the docs describe knobs that are either renamed or inert, and the only config error code (`ERR_ADAPTER_OIDC_CONFIG`) does not cover the auth-code fields the docs mark as required.

Evidence:
- [`src/adapters/oidc/config.go:19`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/config.go:19)
- [`src/adapters/oidc/config.go:21`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/config.go:21)
- [`src/adapters/oidc/provider.go:44`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/provider.go:44)
- [`src/adapters/oidc/provider.go:57`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/provider.go:57)
- [`src/adapters/oidc/verifier.go:155`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go:155)
- [`src/adapters/oidc/verifier.go:226`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go:226)
- [`src/adapters/oidc/token.go:26`](/Users/shengming/Documents/code/gocell/src/adapters/oidc/token.go:26)
- [`docs/guides/adapter-config-reference.md:80`](/Users/shengming/Documents/code/gocell/docs/guides/adapter-config-reference.md:80)
- [`docs/design/capability-inventory.md:172`](/Users/shengming/Documents/code/gocell/docs/design/capability-inventory.md:172)

Why this matters: the package presents a wider, more tunable API than it really has. Callers reading the docs will set expectations around cache refresh and auth-code validation that the runtime does not meet, which makes the adapter harder to adopt and harder to reason about during incidents.

Recommended shape: either wire the documented knobs through the runtime and align the field names, or trim the public config/doc surface down to only the knobs that are actually honored today.

## Note

I did not find a separate standalone errcode naming problem. The `ERR_ADAPTER_OIDC_*` codes themselves are internally consistent; the issue is that the current docs promise a config contract that the error path does not enforce.
