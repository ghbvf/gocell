// Code in this file is intentionally minimal: it carries the //go:generate
// directive that drives `go generate ./cmd/corebundle/`, kept in its own
// file so the directive is discoverable and not buried inside any of the
// per-concern bundle_*.go composition root files.
package main

// --module-path is pinned to the base module github.com/ghbvf/gocell because
// cmd/corebundle is its own go.work module (#1559): without it, the generator's
// resolveModule reads cmd/corebundle/go.mod and emits a wrong depgraph import
// (.../cmd/corebundle/kernel/depgraph). kernel/depgraph + the layer-classification
// root always live in the base module, so the base path is the correct value here.
//go:generate go run ../gocell generate catalog --out=catalog_gen.go --package=main --module-path=github.com/ghbvf/gocell
