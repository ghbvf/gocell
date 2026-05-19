# Login Failure Triage Runbook

Public login endpoint (`POST /api/v1/access/sessions/login`) returns a uniform
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

`http_requests_total{route="POST /api/v1/access/sessions/login",status="401"}` —
counts the public-facing 401s. Use the slog discriminator above to break
them down by cause when paging.

`http_request_duration_seconds{route="POST /api/v1/access/sessions/login"}` —
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

## Auto-lockout 相关 slog 事件 (ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01)

| Event | Level | slog message | 字段 | 含义 |
|-------|-------|--------------|------|------|
| auto-lock 触发 | Warn | `account auto-locked` | `user_id` / `failed_count` / `reason=threshold_locked` | 用户连续失败达阈值，已自动锁定 |
| lazy-unlock 触发 | Info | `account lazy-unlocked` | `user_id` / `locked_until` / `reason=lazy_unlocked` | TTL 到期，sessionlogin 自动解锁 |
| lockout 计数器更新失败 | Error | `sessionlogin: lockout record failure failed` | `error` / `user_id` / `reason` | DB/outbox 故障导致计数器无法持久化 |

### 查询示例

近 1 小时被自动锁定的用户：

```
jq 'select(.msg == "account auto-locked") | {time, user_id, failed_count}'
```

lockout 计数器异常：

```
jq 'select(.msg | startswith("sessionlogin: lockout record failure"))'
```

## Break-glass：唯一 admin 被 auto-lock 后恢复

### 触发场景

所有 admin 用户被 `accountlockout` 阈值锁定后，登录端点 (`POST /api/v1/access/sessions`) 拒绝任何 admin login，导致无法走 admin unlock endpoint 解锁。

### 恢复路径

**首选：BootstrapAuth setup endpoint（FMT-28）**

`/api/v1/*/setup/admin` endpoint 走独立认证面（HTTP Basic via env `GOCELL_SETUP_ADMIN_USERNAME` + `GOCELL_SETUP_ADMIN_PASSWORD`），不参与 password-login lockout：
1. 确认 env 凭证仍可用（生产部署应保留）
2. 用 setup endpoint 重置 admin 密码 / 状态（具体 endpoint 参考 contracts/http/auth/setup/admin/*）

**次选：DB-level recovery（ops 操作）**

直连 PG 解除锁定：

```sql
UPDATE users
SET status='active', failed_login_count=0, locked_until=NULL, updated_at=NOW()
WHERE id='<admin-user-id>';
```

注意：此操作不通过 authzmutate funnel，不会 bump authz_epoch。如果担心 stale session，配合：

```sql
UPDATE sessions SET revoked_at=NOW() WHERE user_id='<admin-user-id>' AND revoked_at IS NULL;
UPDATE refresh_tokens SET revoked_at=NOW() WHERE user_id='<admin-user-id>' AND revoked_at IS NULL;
```

### 防御

监控 `auth_account_lockout_total{reason="threshold_locked"}` rate。设置告警阈值（如 > 1/min 持续 5min）。
