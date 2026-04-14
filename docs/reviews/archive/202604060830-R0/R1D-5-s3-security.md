# R1D-5: adapters/s3 Security Review

- **Seat**: S2 Security
- **Date**: 2026-04-06
- **Baseline**: `5096d4f`
- **Scope**: `adapters/s3/`, direct caller `cells/audit-core/internal/adapters/s3archive/archive.go`
- **Method**: isolated rerun from code, tests, config, README, and spec only; no existing review docs consulted during this rerun

## Summary

No `P0`.

- `P1` empty object keys are accepted by `Upload`, `Download`, `Delete`, `PresignedGet`, and `PresignedPut`. That degrades object APIs into bucket/root requests and expands the capability beyond a specific object. Refs: [client.go:147](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L147), [client.go:154](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L154), [objects.go:15](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L15), [objects.go:50](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L50), [objects.go:81](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L81), [presigned.go:39](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L39)

- `P1` object key input crosses the URL/signing boundary without escaping or validation. `?`, `#`, `%2F`, spaces, or leading `/` can alter the effective target resource or break signature correctness. Refs: [client.go:145](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L145), [client.go:151](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L151), [client.go:174](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L174), [presigned.go:40](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L40), [presigned.go:45](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L45), [presigned.go:103](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L103)

- `P1` `PresignedPut` is an unconstrained bearer upload token. It signs only `host`, uses `UNSIGNED-PAYLOAD`, and cannot bind `Content-Type`, checksum, or size. Any holder of the URL can upload arbitrary content until expiry. Refs: [presigned.go:50](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L50), [presigned.go:55](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L55), [presigned.go:77](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L77)

- `P2` upload errors embed raw upstream response bodies into returned error messages. If those errors are surfaced through the default HTTP error path, bucket/object naming and backend details can leak outward. Refs: [objects.go:33](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L33), [objects.go:36](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L36), [archive.go:67](/Users/shengming/Documents/code/gocell/cells/audit-core/internal/adapters/s3archive/archive.go#L67), [response.go:45](/Users/shengming/Documents/code/gocell/pkg/httputil/response.go#L45)
