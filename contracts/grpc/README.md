# `contracts/grpc/` — platform gRPC proto root

This directory is the **platform** gRPC proto module root for GoCell's gRPC
transport (epic [#1099]). Cell-owned protos live next to their cell (e.g.
`examples/iotdevice/contracts/grpc/...`), which is added as a second buf module
root when the first cell proto lands in PR-8 ([#1151]).

## Toolchain (hermetic, zero `go.mod` footprint)

`.proto` → `.pb.go` is driven by **buf** + `protoc-gen-go` + `protoc-gen-go-grpc`,
all invoked through `go run` so nothing is installed on `$PATH` and the main
module's `go.mod` is **not** polluted with buf's dependency tree:

- `buf.yaml` / `buf.gen.yaml` (repo root) — module roots + plugin config.
- `protoc-gen-go` runs with **no** `@version`, so it resolves from the module's
  `google.golang.org/protobuf` require — generator and runtime stay in lockstep.
- `protoc-gen-go-grpc` is a separate module, version-pinned in `buf.gen.yaml`.
- `buf` itself is version-pinned by `BUF_VERSION` in the `Makefile`.

## Regenerate

```sh
make proto-gen        # buf generate -> generated/contracts/grpc/**/*.pb.go
```

Commit the regenerated `*.pb.go`. CI (`hack/verify-codegen-proto.sh`, wired into
the `verify-codegen` job) regenerates and `git diff`s to gate drift — a stale or
hand-edited `.pb.go` fails the build.

## Layout convention

A proto at `<module-root>/<domain-path>/vN/x.proto` with

```proto
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/<domain-path>/vN;<pkg>v<N>";
```

generates to `generated/contracts/grpc/<domain-path>/vN/` (via `out` +
`paths=source_relative`). The proto's module-relative path therefore **must**
equal its `go_package` import-path suffix, or `go build` cannot resolve the
package. The proto `package` must mirror the same path (buf `STANDARD` lint
`PACKAGE_DIRECTORY_MATCH`).

## `conformance/v1/`

`conformance/v1/conformance.proto` is **not** a runtime contract — it is the
toolchain conformance fixture: the single committed proto that keeps the
pipeline honest. `hack/verify-codegen-proto.sh` regenerates from it, and
`tools/protobuild` imports the generated package to assert it compiles + links
against the main module's protobuf + grpc runtimes.

[#1099]: https://github.com/ghbvf/gocell/issues/1099
[#1151]: https://github.com/ghbvf/gocell/issues/1151
