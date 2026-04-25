// Command gocell-vet is a standalone vet driver that runs the
// unconditionalskip analyzer via singlechecker.Main. It is intended for local
// development and CI invocations that want a thin binary without the full
// gocell CLI:
//
//	gocell-vet ./...
//
// For integration with the gocell CLI, use:
//
//	gocell check unconditional-skip [--format text|json|sarif] [./...]
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/ghbvf/gocell/tools/nogo/unconditionalskip"
)

func main() {
	singlechecker.Main(unconditionalskip.Analyzer)
}
