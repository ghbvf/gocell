# R1D-5: adapters/s3 Kernel Guardian Review

- **Seat**: Kernel Guardian
- **Date**: 2026-04-06
- **Baseline**: `5096d4f`
- **Scope**: `adapters/s3/`, direct wrapper `cells/audit-core/internal/adapters/s3archive/archive.go`
- **Method**: isolated rerun from code, spec, and direct caller usage only; no existing review docs consulted during this rerun

## Layering Check

Green on dependency direction:

- `adapters/s3/` imports stdlib + `pkg/errcode` only
- no import of `cells/`, `cmd/`, or sibling adapters
- `audit-core` wraps the concrete uploader behind a local interface instead of importing the adapter package directly

## Findings

- `P1` there is no shared object-storage port above the concrete adapter. The cell wrapper therefore defines a private `ObjectUploader` interface, and future cells will likely invent their own slices of the same capability. That fragments storage semantics across the codebase. Refs: [archive.go:24](/Users/shengming/Documents/code/gocell/cells/audit-core/internal/adapters/s3archive/archive.go#L24), [archive.go:41](/Users/shengming/Documents/code/gocell/cells/audit-core/internal/adapters/s3archive/archive.go#L41), [client.go:92](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L92)

- `P2` the implemented API collapses bucket selection into process config, while the original phase-3 contract described bucket-per-operation methods. That is workable, but it should now be treated as an explicit design decision rather than an accidental drift. Refs: [spec.md:63](/Users/shengming/Documents/code/gocell/specs/feat/002-phase3-adapters/spec.md#L63), [spec.md:64](/Users/shengming/Documents/code/gocell/specs/feat/002-phase3-adapters/spec.md#L64), [objects.go:14](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L14), [presigned.go:15](/Users/shengming/Documents/code/gocell/adapters/s3/presigned.go#L15)
