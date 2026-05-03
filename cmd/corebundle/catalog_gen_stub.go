// Stub generatedPackageGraph for builds that have not run `go generate`.
//
// catalog_gen.go (the real graph) is built from go/packages.Load(./...) which
// reports slightly different .Imports across macOS and Linux due to a known
// quirk in how internal *_test.go imports merge into production package
// imports. To eliminate cross-platform drift, the real generated file is
// .gitignore'd and built only when `-tags=catalog_gen` is passed to
// `go build` (CI does this; `make build` and `make generate` also do this).
//
// This stub provides an empty graph so `go build ./cmd/corebundle/` works
// out-of-the-box for newcomers and local dev iteration where the full
// dependency catalog is not needed. The catalog HTTP endpoint will return a
// `dependencies.packages.error: "stub graph; rebuild with -tags=catalog_gen"`
// payload in this mode.
//
// See docs/guides/devtools-catalog.md for full design rationale.

//go:build !catalog_gen

package main

import kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"

// generatedPackageGraph is the stub form: an empty graph rooted at this
// module. The Layer/CellID/Imports fields stay zero; the catalog endpoint
// degrades gracefully (cellDeps + statusBoard + entities still populated;
// only packageDeps is empty).
var generatedPackageGraph = kerneldepgraph.FromNodes(
	"github.com/ghbvf/gocell",
	[]*kerneldepgraph.Node{},
)
