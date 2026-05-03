// Code in this file is intentionally minimal: it carries the //go:generate
// directive that drives `go generate ./cmd/corebundle/`, kept separate from
// bundle.go so the directive is discoverable and not hidden inside a 500-line
// composition root file.
package main

//go:generate go run ../gocell generate catalog --out=catalog_gen.go --package=main
