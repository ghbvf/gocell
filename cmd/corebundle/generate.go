// Code in this file is intentionally minimal: it carries the //go:generate
// directive that drives `go generate ./cmd/corebundle/`, kept in its own
// file so the directive is discoverable and not buried inside any of the
// per-concern bundle_*.go composition root files.
package main

//go:generate go run ../gocell generate catalog --out=catalog_gen.go --package=main
