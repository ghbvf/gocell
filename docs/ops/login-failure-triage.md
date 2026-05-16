# Login Failure Triage Runbook

Public login endpoint (`POST /api/v1/auth/sessions`) returns a uniform
`401 ERR_AUTH_LOGIN_FAILED` for **every** authentication failure to prevent
account-existence enumeration and timing sidechannels. The real failure reason
is recorded server-side via `errcode.WithInternal` and lands in structured
`slog` only — it never reaches the wire.

This runbook lists the four Internal-text templates that distinguish the
underlying causes, and the slog queries operators use to triage incidents.

## Wire shape (all four cases)

```json
{
  "error": {
    "code": "ERR_AUTH_LOGIN_FAILED",
    "message": "invalid credentials",
    "details": []
  }
}
```

HTTP status: `401`. Identical body regardless of the underlying cause —
operators cannot infer the cause from a captured response.

## Internal text templates

`errcode.WithInternal` payloads — recorded by the framework's HTTP middleware
(`pkg/httputil.log4xx`) as `slog.String("internal", ...)` on the public-facing
4xx log record. The log line uses `slog.Warn` with msg `"error (4xx)"` (label
comes from the generated handler's `writeErrcodeError` call site; "error" is
the default label). The `internal` field is never serialized to the wire.

| # | Template | Source | When emitted |
|---|----------|--------|--------------|
| 1 | `user lookup failed: <repo-error>` | `sessionlogin.Login` post-fetch branch | Username not found OR repo returned an error (DB unreachable / row corrupt). Both collapse to 401 for enumeration safety. `<repo-error>` is the repository error's Error() text (e.g. `"not found"`); it does NOT embed the attempted username — to recover the username use access logs / request-body logging if enabled. |
| 2 | `account not active (user_id=<uuid> status=<status> bcrypt_ok=<bool>)` | `sessionlogin.Login` non-active branch | User exists but `CanAuthenticate()` is false (status ∈ {locked, suspended}). `bcrypt_ok` indicates whether the password was correct — useful for distinguishing "right password but account locked" from "wrong password on locked account". |
| 3 | *(no WithInternal — message-only)* | `sessionlogin.Login` bcrypt-mismatch branch | User exists and is active, but bcrypt compare failed (wrong password). Identifiable in slog by `code=ERR_AUTH_LOGIN_FAILED` with the `internal` field **absent** (jq: `.internal == null`). |
| 4 | `account deactivated in race window (in-tx check): user=<username>` | `sessionlogin.loginInTx` in-tx re-check | User passed the pre-tx active check but was locked/suspended concurrently before the tx FOR UPDATE re-fetch. Rare race; if frequent, investigate concurrent admin-side write contention. |

## slog query recipes

### "Show me all failed logins in the last 5 minutes"

```bash
kubectl logs deployment/accesscore --since=5m \
  | jq -r 'select(.code=="ERR_AUTH_LOGIN_FAILED") |
           "\(.time) request_id=\(.request_id // "-") reason=\(.internal // "wrong_password")"'
```

Top-level slog fields on the 4xx record: `time`, `level` (`WARN`), `msg`
(`"error (4xx)"` by default), `code`, `status`, optionally `internal`,
`request_id`, `trace_id`, `span_id` (see `pkg/httputil.log4xx` + `AppendCorrelationAttrs`).
The wire-side `user_agent` is in the ingress / access log, not this slog
record — join via `request_id`.

### "Distinguish missing-user vs wrong-password vs inactive"

The `internal` field is the discriminator (absent for the wrong-password
branch — Template #3 has no `WithInternal`):

```bash
kubectl logs deployment/accesscore --since=15m \
  | jq -r 'select(.code=="ERR_AUTH_LOGIN_FAILED") |
      if   .internal == null                                then "wrong_password"
      elif .internal | startswith("user lookup failed")     then "missing_user_or_repo_error"
      elif .internal | startswith("account not active")     then "inactive"
      elif .internal | startswith("account deactivated")    then "race_window"
      else "unknown"
      end' \
  | sort | uniq -c | sort -rn
```

### "Inspect repo-error texts on the missing-user/repo-failure branch"

Template #1 records the repository error text, not the attempted username.
Use this to spot DB-side issues (e.g. `"context deadline exceeded"`,
`"connection refused"`) hiding behind the uniform 401:

```bash
kubectl logs deployment/accesscore --since=1h \
  | jq -r 'select(.code=="ERR_AUTH_LOGIN_FAILED" and (.internal // "" | startswith("user lookup failed"))) | .internal' \
  | sort | uniq -c | sort -rn | head -20
```

For per-username brute-force triage, correlate the 401 timestamps with
ingress / access logs where the request body or username is preserved
(`request_id` in the slog record matches the access log line).

### "Find lockout / suspension hits"

```bash
kubectl logs deployment/accesscore --since=1h \
  | jq -r 'select(.code=="ERR_AUTH_LOGIN_FAILED" and (.internal // "" | startswith("account not active"))) |
           .internal' \
  | sort | uniq -c | sort -rn
```

`bcrypt_ok=true` here means the password was correct but the account was
inactive — typical when an admin disables a user who still has a working
credential cache.

## Correlated metrics

`http_requests_total{route="POST /api/v1/auth/sessions",status="401"}` —
counts the public-facing 401s. Use the slog discriminator above to break
them down by cause when paging.

`http_request_duration_seconds{route="POST /api/v1/auth/sessions"}` —
must remain flat across the four causes. The login handler runs bcrypt
unconditionally (using `dummyBcryptHash` on missing-user) so all four paths
produce ~12-cost bcrypt latency. A statistically distinguishable bimodal
distribution = regression of the timing-normalization invariant
(see ADR §3 threat model "timing 旁路均一化" row).

## Related decisions

- ADR `docs/architecture/202605101400-adr-credential-session-protocol.md` §3
  threat matrix rows "账号枚举防护 (401 三态归一)" + "timing 旁路均一化"
- contract: `contracts/http/auth/login/v1/contract.yaml` 401 description
- code: `cells/accesscore/slices/sessionlogin/service.go` — `errMsgInvalidCredentials`
  const + `dummyBcryptHash` + the four Internal templates above

## Admin path divergence (note)

`identitymanage.IssueForUser` (called from ChangePassword) returns
`KindPermissionDenied` (403 `ERR_AUTH_USER_NOT_ACTIVE`) for non-active users
rather than the uniform 401. The path is admin-authenticated, so there is no
enumeration concern; surfacing the specific cause helps admin tooling.
See `IssueForUser` godoc for the rationale.
