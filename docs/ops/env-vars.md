# GoCell Environment Variables Reference

This document lists all environment variables consumed by `cmd/core-bundle` at startup.
Variables without a default value are **required** in the indicated adapter mode.
Missing required variables cause fail-fast before any assembly initialization.

## JWT Configuration (required in all modes)

| Variable | Purpose | Default | Required | Notes |
|---|---|---|---|---|
| `GOCELL_JWT_ISSUER` | JWT `iss` claim written by JWTIssuer and verified by JWTVerifier on every authenticated request | — | **All modes** | Required regardless of `GOCELL_CELL_ADAPTER_MODE` or `GOCELL_ADAPTER_MODE`; there is no dev fallback. Missing this variable causes fail-fast at startup. |
| `GOCELL_JWT_AUDIENCE` | JWT `aud` claim written by JWTIssuer and verified by JWTVerifier on every authenticated request | — | **All modes** | Required regardless of `GOCELL_CELL_ADAPTER_MODE` or `GOCELL_ADAPTER_MODE`; there is no dev fallback. Must match the value expected by all session-login/refresh token consumers. Missing this variable causes fail-fast at startup. Note: `GOCELL_JWT_AUDIENCES` (comma-separated multi-value) is not yet implemented; when introduced, migration path and priority over `GOCELL_JWT_AUDIENCE` will be defined. |

## RSA Key Set (for JWT signing and verification)

| Variable | Purpose | Default | Required |
|---|---|---|---|
| `GOCELL_JWT_PRIVATE_KEY` | PEM-encoded RSA private key for JWT signing | — (ephemeral in dev) | **Real mode** |
| `GOCELL_JWT_PUBLIC_KEY` | PEM-encoded RSA public key for JWT verification | — (derived in dev) | **Real mode** |
| `GOCELL_JWT_PREV_PUBLIC_KEY` | PEM-encoded previous RSA public key (rotation) | — (optional) | No |
| `GOCELL_JWT_PREV_KEY_EXPIRES` | RFC 3339 expiry for the previous public key | — | Only when `GOCELL_JWT_PREV_PUBLIC_KEY` is set |

## Service Token / Controlplane Guard

| Variable | Purpose | Default | Required | Notes |
|---|---|---|---|---|
| `GOCELL_SERVICE_SECRET` | HMAC-SHA256 secret (≥ 32 bytes) for `ServiceTokenMiddleware` protecting `/internal/v1/*` | — | **Real mode** | Introduced in PR #AUTH-TRUST-BOUNDARY-160 (C6). Value is used as raw UTF-8 bytes (not base64-decoded). To generate: `openssl rand -base64 32`. Empty in dev mode disables the guard (Warn logged). |
| `GOCELL_SERVICE_SECRET_PREVIOUS` | Previous HMAC secret for zero-downtime rotation | — | No | Optional; tried after current secret fails verification. |

## Storage and Session Keys

| Variable | Purpose | Default (dev) | Required |
|---|---|---|---|
| `GOCELL_HMAC_KEY` | HMAC key for session HMAC chains | `dev-hmac-key-replace-in-prod!!!!` | **Real mode** |
| `GOCELL_AUDIT_CURSOR_KEY` | HMAC key for audit cursor codec | `core-bundle-audit-cursor-key-32!` | **Real mode** |
| `GOCELL_AUDIT_CURSOR_PREVIOUS_KEY` | Previous audit cursor key (rotation) | — | No |
| `GOCELL_CONFIG_CURSOR_KEY` | HMAC key for config cursor codec | `core-bundle-cfg-cursor-key--32b!` | **Real mode** |
| `GOCELL_CONFIG_CURSOR_PREVIOUS_KEY` | Previous config cursor key (rotation) | — | No |

## Observability / Monitoring

| Variable | Purpose | Default | Required |
|---|---|---|---|
| `GOCELL_METRICS_TOKEN` | Bearer token for `/metrics` scraper authentication (`X-Metrics-Token` header) | — | **Real mode** |
| `GOCELL_READYZ_VERBOSE_TOKEN` | Bearer token for `/readyz?verbose` (exposes internal topology) | — | **Real mode** |

## Adapter Mode

| Variable | Purpose | Default | Accepted Values |
|---|---|---|---|
| `GOCELL_ADAPTER_MODE` | Selects secret-loading and fail-fast behaviour | `""` (dev/in-memory) | `""` (dev), `"real"` |
| `GOCELL_CELL_ADAPTER_MODE` | Selects the storage backend for Cell repositories | `""` (in-memory) | `""`, `"memory"`, `"postgres"` |
| `GOCELL_PG_DSN` | PostgreSQL DSN used when `GOCELL_CELL_ADAPTER_MODE=postgres` | — | Any valid DSN |

## State

| Variable | Purpose | Default |
|---|---|---|
| `GOCELL_STATE_DIR` | Directory for stateful files (e.g. initial admin credential on first run) | `/run/gocell` |
