// Package protobuild is the home of the GoCell proto -> .pb.go toolchain
// conformance check. It carries no runtime code.
//
// The pipeline itself (buf + protoc-gen-go + protoc-gen-go-grpc) is configured
// hermetically at the repo root (buf.yaml + buf.gen.yaml) and driven by the
// Makefile `proto-gen` target via `go run` — no buf/protoc binary is installed
// or pinned in the main module's go.mod. CI gates drift with
// hack/verify-codegen-proto.sh (regenerate + git diff).
//
// conformance_test.go imports the committed
// generated/contracts/grpc/conformance/v1 fixture and exercises it against the
// main module's protobuf + grpc runtimes, so a broken toolchain, an
// incompatible plugin/runtime version pair, or a stale checked-in .pb.go fails
// `go test` here — not only the CI diff gate. The first real cell-owned grpc
// contract lands with examples/iotdevice in the PR-8 epic (#1151).
package protobuild
