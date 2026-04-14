# R1D-5: adapters/s3 Architecture Review

- **Seat**: S1 Architecture
- **Date**: 2026-04-06
- **Baseline**: `5096d4f`
- **Scope**: `adapters/s3/`, direct caller `cells/audit-core/internal/adapters/s3archive/archive.go`
- **Method**: isolated rerun from code, tests, config, README, and spec only; no existing review docs consulted during this rerun

## Summary

No `P0`.

- `P1` `UsePathStyle=false` is not virtual-hosted style; it drops `Bucket` entirely and sends requests to `endpoint[/key]`. This breaks `Health`, object operations, and presigned URLs outside path-style mode. Refs: [client.go:117](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L117), [client.go:145](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L145), [client.go:157](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L157), [presigned.go:39](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L39), [presigned.go:103](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L103)

- `P1` object keys are treated as URL path fragments instead of object identifiers. The code concatenates `key` into a raw URL string, then derives canonical URI from parsed path. Reserved characters such as `?`, `#`, `%2F`, and leading `/` can change request semantics or signature target. Refs: [client.go:145](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L145), [client.go:151](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L151), [client.go:174](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L174), [presigned.go:40](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L40), [presigned.go:45](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L45)

- `P2` one `Endpoint` field is used for both backend S3 access and the externally returned presigned URL host. In real deployments those are often different. Refs: [client.go:21](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L21), [presigned.go:39](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L39), [presigned.go:103](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L103)

- `P2` the API shape bakes in a small-object, full-buffer model: uploads take `[]byte`, downloads return `[]byte`, and timeout control is a single `http.Client.Timeout`. This makes future streaming/multipart support an API change rather than an implementation detail. Refs: [objects.go:14](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L14), [objects.go:49](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L49), [client.go:34](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L34), [archive.go:26](/Users/shengming/Documents/code/gocell/cells/audit-core/internal/adapters/s3archive/archive.go#L26)

## Notes

- Layering is otherwise clean: no imports of `cells/` from the adapter package, and the cell-side archive wrapper stays decoupled behind an interface.
