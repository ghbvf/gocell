# ADR — gRPC errcode.Kind → codes.Code Mapping (PR-12 closure)

| | |
|---|---|
| **Status** | Accepted (PR 12 of 12) |
| **Date** | 2026-05-26 |
| **Epic** | #1099 gRPC transport adapter |
| **Issue** | #1155 |
| **Spec / Plan** | `docs/plans/specs/202605262300-048-grpc-adapter/spec.md` |
| **PR series** | PR 1 (foundation ADR `202605260000`) … PR 12 (this ADR) |

ref: go-kratos/kratos errors/errors.go GRPCStatus()
ref: grpc-ecosystem/grpc-gateway runtime/errors.go DefaultHTTPErrorHandler

## 背景

Prior to PR-12, a handler returning an `*errcode.Error` surfaced on the gRPC wire as
`codes.Unknown` — the default conversion grpc-go applies to any Go error that does not
implement the `GRPCStatus()` interface. This was documented as a known gap in
`J-sessionverify` (criterion 5, deferred with explicit "depends on PR-12 #1155").

The `pkg/errcode` package defines `errcode.Kind`, a typed closed set covering every
HTTP-semantic domain error the platform produces. Two design constraints shaped the
mapping strategy:

1. **pkg/errcode must not import `google.golang.org/grpc/codes`** — `pkg/` sits below
   `runtime/` in the dependency lattice and is shared with `framework/kernel/`. Importing
   grpc codes into `pkg/errcode` would pollute the kernel transitively, violating the
   `kernel/ ↛ grpc` layering constraint (verified by the `depguard` lane in CI).
2. **The Kratos `GRPCStatus()` model** — Kratos places the HTTP→gRPC translation on the
   error type itself (each error implements `GRPCStatus() *status.Status`). This requires
   the error package to import the grpc status package, which violates constraint 1.
   GoCell therefore adapts the semantic goal — making every `errcode.Kind` produce a
   deterministic, documented gRPC status code — while placing the mapping at the
   interceptor layer instead of the error type.

The chosen architecture: a new **`ErrcodeMap` unary+stream interceptor** in
`framework/runtime/grpc/interceptor/errcode_mapping.go` inspects the returned Go error,
unwraps it via `errors.As` to `*errcode.Error`, and converts `errcode.Kind` to the
correct `codes.Code` before sending the response. This is the "Kratos `GRPCStatus()` model
adapted" — same semantic outcome, different structural placement.

## 決策記録

### D1 — The errcode.Kind → codes.Code mapping table (wire-irreversible)

| errcode.Kind | HTTP equiv | grpc codes.Code | Rationale |
|---|---|---|---|
| KindInternal | 500 | Internal | Direct analog. |
| KindInvalid | 400 | InvalidArgument | Direct analog. |
| KindUnauthenticated | 401 | Unauthenticated | Direct analog. |
| KindPermissionDenied | 403 | PermissionDenied | Direct analog. |
| KindNotFound | 404 | NotFound | Direct analog. |
| KindConflict | 409 | Aborted | gRPC has no Conflict; Aborted is the canonical CAS/concurrency-conflict code (grpc-gateway maps 409→Aborted). |
| KindUnprocessable | 422 | InvalidArgument | 422 is a semantic-validation failure — same caller-error family as 400; InvalidArgument is the correct gRPC semantic. |
| KindGone | 410 | NotFound | gRPC has no Gone code; the caller's retry should stop — NotFound is the closest available semantic. |
| KindPayloadTooLarge | 413 | ResourceExhausted | grpc-gateway convention: payload overload maps to ResourceExhausted (same as rate-limit, reflecting that both exhaust a server resource). |
| KindRateLimited | 429 | ResourceExhausted | Per the gRPC canonical recommendation; signals the caller should back off. |
| KindClientClosed | 499 | Canceled | Direct analog; the client cancelled the request. |
| KindDeadlineExceeded | 504 | DeadlineExceeded | Direct analog. |
| KindUnavailable | 503 | Unavailable | Direct analog. |
| KindNotImplemented | 501 | Unimplemented | Direct analog. |
| (unknown / default) | — | Internal | Fail-closed: an unmapped Kind produces Internal, not a silent codes.OK. |

Non-obvious choices rationale:

- **KindConflict → Aborted (not FailedPrecondition)**: grpc-gateway's
  `DefaultHTTPErrorHandler` maps 409 to `Aborted`. `Aborted` is used for
  compare-and-swap failures and concurrency conflicts — the dominant GoCell
  KindConflict use-case (row-level CAS on outbox + saga state). `FailedPrecondition`
  would imply the system is in a wrong state for the operation; that is a different
  semantic.
- **KindGone → NotFound**: gRPC has no equivalent of "was here, now permanently gone".
  `NotFound` is the least-wrong choice. **Consumer guidance**: `KindGone → NotFound` is a
  deliberate lossy mapping; consumers MUST NOT infer retryability from `codes.NotFound`
  alone — a `NotFound` may indicate either "never existed" or "permanently gone". Handlers
  that need to signal permanent deletion must do so via an app-level signal (e.g., a
  response field or dedicated event) — the gRPC code alone is insufficient. `Unimplemented`
  is reserved for protocol-gap cases, not resource lifecycle.
- **KindUnprocessable → InvalidArgument**: 422 is HTTP-specific validation semantics.
  The gRPC vocabulary doesn't separate syntactic vs semantic validation; `InvalidArgument`
  covers both and is the correct client-error signal.
- **Default → Internal (fail-closed)**: An unknown `Kind` value must not leak as
  `codes.OK` (success) or `codes.Unknown` (opaque). Hard fail-closed to `codes.Internal`
  gives the caller a defined error signal and forces the Kind gap to be fixed.

### D2 — Placement in `framework/runtime/grpc/interceptor/` (not in `pkg/errcode`)

The mapping lives in `errcode_mapping.go` under `runtime/grpc/interceptor`. This is the
only package in the runtime allowed to import both `pkg/errcode` and
`google.golang.org/grpc/codes`. Locating it here enforces the `pkg/errcode ⊥ grpc`
constraint structurally (depguard) rather than by documentation.

### D3 — 4xx vs 5xx redaction parity with HTTP (#2479 message-only fail-safe → #2482 delayed completion)

The `ErrcodeMap` interceptor applies the same redaction discipline as the HTTP
`httputil` 5xx projection. PR-12 (#2479) shipped a message-only fail-safe (4xx
forwarded only the errcode message, not PublicDetails). #2482 completes the parity:

- **4xx-equivalent errors** (`Kind.IsClient()` returns true): the `errcode.Error.Message`
  is forwarded as the gRPC status message (const literal, MESSAGE-CONST-LITERAL-01),
  and `WithDetails` PublicDetail attributes are forwarded in a `google.rpc.ErrorInfo`
  detail embedded in `google.rpc.Status.Details`:
  - `ErrorInfo.Reason` = `string(ec.Code)` (machine-readable, stable across releases).
  - `ErrorInfo.Domain` = `errcodeDomain` (`"gocell.errcode"`) — distinct from
    `denyReasonDomain` (`"gocell.authz.grpc"`) so gRPC clients can distinguish a
    business 4xx ErrorInfo from an auth/authz denial ErrorInfo by keying on
    (Domain, Reason) without parsing the English message.
  - `ErrorInfo.Metadata` = key→rendered-value map, value rendering aligned with HTTP
    `marshalJSONValue` semantics: PublicString → raw string; PublicInt/Bool/Duration →
    `json.Marshal(scalar)` decimal/boolean/nanosecond-integer string; PublicTime →
    RFC3339Nano string, unquoted (same instant as `json.Marshal(time.Time)`, minus the
    structural JSON quotes — a `map[string]string` value is the string itself, so
    string and time values omit the JSON quotes the HTTP body context would carry).
  - When `ec.Details` is empty, no ErrorInfo is attached (no spurious empty-Metadata
    detail on the wire).

- **5xx-equivalent errors** (`!Kind.IsClient()`): the wire message is replaced with a
  generic constant literal; all `WithDetails` PublicDetail attributes are stripped.
  Only `WithInternal(InternalAttr)` data reaches server-side `slog`, never the wire.

This is the same PII discipline documented in
`docs/architecture/202605051730-adr-errcode-message-pii-safety.md`, applied to the gRPC
transport surface.

**Security rationale for the D3 amendment (ai-robust.md ADR amendment clause)**:

1. **5xx-no-details invariant — HARD (split-constructor, typed function choice)**: The
   5xx path in `errToStatus` calls `status.Error(code, msgInternalServerError)` directly —
   there is no ErrorInfo parameter at the call site. `clientStatusWithDetails` is a
   separate function that accepts a `*errcode.Error` and is ONLY called on the
   `ec.Kind.IsClient()` branch. Making 5xx-details carry structurally inexpressible in
   Go's type system (both callsites are in the same `if/else` — a future AI or dev would
   have to actively add a second `clientStatusWithDetails` call on the 5xx branch).

2. **PublicDetail is the errcode 4xx-safe projection surface**: `error-handling.md` §Message
   と PII documents that `WithDetails(PublicString/…)` attrs are "4xx 可下发 / 5xx strip".
   PublicDetails are already the errcode-defined safe projection for client error surfaces;
   forwarding them in gRPC ErrorInfo.Metadata is not a new trust decision — it extends the
   existing errcode contract to the gRPC transport.

3. **Domain separation prevents client confusion**: `errcodeDomain` (`"gocell.errcode"`)
   vs `denyReasonDomain` (`"gocell.authz.grpc"`) ensures a client keying on the Domain
   field of an incoming ErrorInfo cannot confuse a business validation error with an
   auth/authz denial. Same response path cannot carry both: an auth denial returns an
   already-status error (codes ≠ Unknown), which `errToStatus` passes through unchanged
   (the `status.FromError` guard), so `errToStatus` never generates an errcode ErrorInfo
   for a request that already received an auth denial ErrorInfo.

4. **No conflict with auth `deniedStatus` ErrorInfo**: A single response carries at most
   one ErrorInfo: either the auth interceptor's denial (returned as an already-status
   error that bypasses `errToStatus`) or the errcode mapping's business 4xx ErrorInfo
   (the non-status path). The two producers are mutually exclusive by the
   `codes != Unknown` pass-through guard.

Enforcement: archtest **GRPC-ERRCODE-MAPPING-01** (Medium, permanent Go ceiling) verifies
exhaustive coverage of every `errcode.Kind` value against the mapping table and asserts
that 5xx-class codes produce a redacted generic message. The exhaustiveness check catches
future `Kind` additions at CI. The 5xx-no-details invariant is HARD (split-constructor,
no archtest needed — the type system enforces it).

### D4 — RateLimit and CircuitBreaker interceptors (opt-in via Deps)

Two new interceptors complete the protection chain:

**RateLimit**: `interceptor.Deps.RateLimiter` (interface `RateLimiter.Allow(key string) bool`).
When nil, the interceptor is a passthrough. When wired, a deny yields `codes.ResourceExhausted`
(consistent with `KindRateLimited → ResourceExhausted`). The rate-limit key is the gRPC peer
address. The concrete limiter is the **same transport-agnostic instance** as the HTTP rate-limit
middleware — a single composition-root wire supplies both transports.

**CircuitBreaker**: `interceptor.Deps.Allower` (interface `Allower.Allow() (bool, done func(error))`).
When nil, passthrough. Open circuit → `codes.Unavailable`. The breaker counts a call as a
server failure for codes `{Internal, Unknown, Unavailable, DataLoss, DeadlineExceeded}` only.
Excluded from the failure set:

- `Unimplemented` — a permanent contract gap, not a runtime failure; counting it as a server
  failure would open the breaker against a server that is simply missing a method (unhelpful).
- `ResourceExhausted` — overload or rate-limit (a 4xx-class semantic per the mapping table);
  counting it as server failure would penalize the caller's own load, not a server defect.

This mirrors HTTP's convention of counting only 5xx status codes as circuit failures.

For **streaming RPCs**, the breaker gates stream establishment: open circuit → `Unavailable`
returned at stream open (before the handler runs). `done(err)` is called at stream close with
the final status; the failure classification uses the same code set as unary.

Both interceptors reuse the transport-agnostic cores. The composition root (e.g., the winmdm/MDM
agent) supplies a concrete limiter/breaker; `corebundle` leaves both fields nil.

**RateLimit and CircuitBreaker run BEFORE Auth** in the chain (see D6). This is deliberate
and symmetric with the HTTP middleware ordering: unauthenticated or invalid-token requests
also consume rate-limit / circuit-breaker budget. Deployers should configure lenient burst
limits so that legitimate principals are not starved by pre-auth traffic.

### D5 — Chain-presence guard: resolved by #1752 (no new guard added)

Issue #1155's 2026-06-01 comment asked to Hard-ify a "gRPC chain-presence guard" to prevent
a composition root from building an unauthenticated gRPC server (i.e., skipping the auth
interceptor). This is **already closed** by the #1752 refactor:

- `adapters/grpc.Config.Interceptors` is a required, `Validate()`-gated field (Hard —
  the `required` tag + `Validate()` make it a compile-time structural constraint).
- The only valid `Interceptors` value comes from `interceptor.NewServerInterceptors(deps)`
  (the sole public construction funnel), which hardcodes the full auth chain.
- Archtests `GRPC-WIRING-BUNDLE-CALLER-01` and `GRPC-WIRING-REGISTRAR-MINT-FUNNEL-01`
  guard the funnel: **Medium downstream** (go/types caller-allowlist); **Hard upstream is
  unreachable** — a permanent Go-language ceiling (the constructors `NewServiceRegistrar`
  and `NewDrainSignal` are exported, so Go visibility cannot seal their callers). The
  required+Validate()-gated `Interceptors` field is the actual Hard guarantee; the
  archtests are the Medium backstop on the registrar/drain source.

No new guard is added in PR-12; the chain-presence requirement is documented here as
resolved, superseding the issue's open action item.

### D6 — Chain order after PR-12

The full interceptor chain order is:

**Unary**:
```
RequestID → CellAttribution → Tracing → AccessLog → Metrics → RateLimit → CircuitBreaker → Auth → ErrcodeMap → Recovery → handler
```

**Stream**:
```
RequestID → CellAttribution → Tracing → AccessLog → Metrics → RateLimit → CircuitBreaker → Auth → Drain → ErrcodeMap → Recovery → handler
```

RateLimit and CircuitBreaker sit between Metrics and Auth (protection chain, mirroring HTTP's
middleware ordering). `ErrcodeMap` sits just outside `Recovery` so that Recovery remains the
innermost panic backstop: any error emitted by Recovery (panic→`codes.Internal`) also flows
through ErrcodeMap for consistent wire format.

## Wire-code irreversibility

Once PR-12 ships, the mapping table entries become wire-observable by all gRPC clients of the
platform. Changing a mapping (e.g., moving `KindConflict` from `Aborted` to `FailedPrecondition`)
is a breaking wire change for any client that pattern-matches on the code. The pre-GA wire-break
window (to 2026-12-31) applies: the initial `codes.Unknown` → proper codes transition is a
**correction of a known gap** (not a semantic change), and there are no external gRPC consumers.
Post-GA, changes to the mapping table require a new contract version.

Future `errcode.Kind` additions are caught at CI by the GRPC-ERRCODE-MAPPING-01 exhaustiveness
archtest — a new Kind that is not added to the mapping table fails the archtest (Medium, permanent
Go ceiling, not eliminable without restructuring `errcode.Kind` itself).

## 威胁矩阵

| Concern | Assessment |
|---|---|
| Wire / schema break | The `Unknown` → mapped-codes transition is a correction for zero external consumers (pre-GA window). Future mapping changes are irreversible; the exhaustiveness archtest catches gaps at CI. |
| PII / redaction | 5xx messages are const literals; `WithInternal` data never reaches gRPC trailers. The same `DETAILS-SEALED-FIELD-FROZEN-01` + `MESSAGE-CONST-LITERAL-01` guards that protect HTTP apply at the interceptor layer. 4xx `PublicDetails` are forwarded in `ErrorInfo.Metadata` (Domain=`errcodeDomain`); `PublicDetail` is the errcode-defined 4xx-safe projection surface (error-handling.md §PII). 5xx-no-details is HARD via split-constructor (typed function choice, no archtest needed). See D3 §Security rationale. |
| Layering (`pkg/errcode ⊥ grpc`) | Enforced by depguard; the mapping lives entirely in `runtime/grpc/interceptor/` (legal to import both). |
| AI-robustness | Exhaustiveness: Medium (GRPC-ERRCODE-MAPPING-01 typed scan catches missing Kind entries; permanent Go ceiling). Redaction parity: same carrier as HTTP (existing Hard/Medium guards). Chain-presence: Hard (existing #1752 guards, no new mechanism needed). |
| RateLimit / CircuitBreaker opt-in | nil Deps fields = passthrough (safe default). Composition roots that do NOT wire a limiter/breaker are correct, not misconfigurations. |
