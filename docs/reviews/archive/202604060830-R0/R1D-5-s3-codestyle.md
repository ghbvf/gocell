# R1D-5: adapters/s3 Code Style / DX Review

- **Seat**: S5 DX / Maintainability
- **Date**: 2026-04-06
- **Baseline**: `5096d4f`
- **Scope**: `adapters/s3/`, `.env.example`, public docs under `docs/guides/`
- **Method**: isolated rerun from code, config, docs, and direct test results only; no existing review docs consulted during this rerun

## Summary

No `P0`.

- `P1` public docs and the real API contract have drifted badly. Docs claim optional/defaulted `region`, default `usePathStyle=true`, a `presignExpiry` config, and an Option-pattern constructor; code implements none of that. Refs: [adapter-config-reference.md:96](/Users/shengming/Documents/code/gocell/docs/guides/adapter-config-reference.md#L96), [adapter-config-reference.md:101](/Users/shengming/Documents/code/gocell/docs/guides/adapter-config-reference.md#L101), [adapter-config-reference.md:102](/Users/shengming/Documents/code/gocell/docs/guides/adapter-config-reference.md#L102), [adapter-config-reference.md:116](/Users/shengming/Documents/code/gocell/docs/guides/adapter-config-reference.md#L116), [client.go:73](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L73), [client.go:98](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L98)

- `P1` the default `UsePathStyle=false` value is a DX footgun because the false branch is currently broken. A missing env var silently selects the wrong mode. Refs: [client.go:53](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L53), [client.go:145](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L145)

- `P2` `doRequest()` hardcodes upload-oriented error codes and messages for all operations. Download and delete failures therefore nest an inner upload error, which is confusing in logs and diagnosis. Refs: [client.go:257](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L257), [client.go:260](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L260), [client.go:271](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L271), [objects.go:54](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L54), [objects.go:85](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L85)

- `P2` boolean env parsing is brittle because it relies on exact string equality with `"true"` instead of a normal boolean parser. Refs: [client.go:53](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L53)

- `P2` endpoint validation is deferred too late. `Validate()` checks only non-empty strings, so malformed endpoints survive construction and fail later during request creation. Refs: [client.go:73](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L73), [client.go:257](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L257)
