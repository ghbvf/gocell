# R1D-5: adapters/s3 Product Review

- **Seat**: Product / Delivery
- **Date**: 2026-04-06
- **Baseline**: `5096d4f`
- **Scope**: `src/adapters/s3/`, direct caller `src/cells/audit-core/internal/adapters/s3archive/archive.go`, `.env.example`, `README.md`, `specs/feat/002-phase3-adapters/spec.md`
- **Method**: isolated rerun from code, config, public contract docs, and direct test results only; no existing review docs consulted during this rerun

## Summary

- `P0` audit archive keys can silently collide and overwrite previous archives. The generated key uses only second-level timestamp plus entry count, so two same-second batches with the same size map to the same object key. Refs: [archive.go:60](/Users/shengming/Documents/code/gocell/src/cells/audit-core/internal/adapters/s3archive/archive.go#L60), [archive.go:66](/Users/shengming/Documents/code/gocell/src/cells/audit-core/internal/adapters/s3archive/archive.go#L66)

- `P0` the repo promises an `S3/MinIO Client, PresignedURL`, but non-path-style routing is broken because `Bucket` is ignored when `UsePathStyle=false`. That breaks the external contract for a normal S3-style configuration. Refs: [README.md:247](/Users/shengming/Documents/code/gocell/README.md#L247), [spec.md:59](/Users/shengming/Documents/code/gocell/specs/feat/002-phase3-adapters/spec.md#L59), [client.go:145](/Users/shengming/Documents/code/gocell/src/adapters/s3/client.go#L145)

- `P1` `.env.example` does not produce a working config as written: missing scheme, missing required `GOCELL_S3_REGION`, and using `GOCELL_S3_USE_SSL` even though code reads `GOCELL_S3_USE_PATH_STYLE`. Refs: [.env.example:16](/Users/shengming/Documents/code/gocell/.env.example#L16), [.env.example:20](/Users/shengming/Documents/code/gocell/.env.example#L20), [client.go:49](/Users/shengming/Documents/code/gocell/src/adapters/s3/client.go#L49), [client.go:53](/Users/shengming/Documents/code/gocell/src/adapters/s3/client.go#L53), [client.go:74](/Users/shengming/Documents/code/gocell/src/adapters/s3/client.go#L74)

- `P1` presigned URLs are generated from the backend endpoint, not from a client-facing public base URL. In Docker/K8s/VPC setups that often makes the returned URL unusable for browsers or external clients. Refs: [presigned.go:39](/Users/shengming/Documents/code/gocell/src/adapters/s3/presigned.go#L39), [presigned.go:103](/Users/shengming/Documents/code/gocell/src/adapters/s3/presigned.go#L103)

- `P2` current tests do not prove deliverability of the advertised presigned URL feature. Unit tests assert substrings, while integration tests all skip. Refs: [client_test.go:264](/Users/shengming/Documents/code/gocell/src/adapters/s3/client_test.go#L264), [client_test.go:276](/Users/shengming/Documents/code/gocell/src/adapters/s3/client_test.go#L276), [integration_test.go:11](/Users/shengming/Documents/code/gocell/src/adapters/s3/integration_test.go#L11)
