package codegen_test

import (
	"bytes"
	"testing"
	"text/template"

	gofumpt "mvdan.cc/gofumpt/format"

	"github.com/ghbvf/gocell/tools/codegen"
)

// TestRender_GroupsLocalImports proves Render emits the same import grouping
// that the CI consumer gate expects: stdlib first, third-party second, and
// `github.com/ghbvf/gocell/...` (this module) last, separated by blank lines.
//
// Without imports.LocalPrefix wired up, goimports would mix third-party and
// local-module imports into a single block — producer output would pass its
// own round-trip but `golangci-lint run` (which honors
// `.golangci.yml goimports.local-prefixes: github.com/ghbvf/gocell`) would
// then re-group on the consumer side, breaking "producer output is CI-canonical".
func TestRender_GroupsLocalImports(t *testing.T) {
	t.Parallel()
	// Note the imports are intentionally on a single line each, unsorted, no
	// grouping — Render must group them into three blocks separated by blank
	// lines (stdlib / third-party / local module).
	tpl := template.Must(template.New("groups").Parse(`package demo

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/ghbvf/gocell/kernel/clock"
)

func F(t assert.TestingT) {
	fmt.Println("hi")
	_ = clock.Real()
	assert.True(t, true)
}
`))

	out, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "groups",
		Templates:    tpl,
		Filename:     "demo.go",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := `package demo

import (
	"fmt"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/clock"
)
`
	if !bytes.Contains(out, []byte(want)) {
		t.Errorf("Render did not produce the three-block local-prefix grouping.\n--- want substring\n%s\n--- got\n%s",
			want, out)
	}
}

// TestRender_OutputIsGofumptClean drives the producer-side formatter contract.
//
// The template intentionally emits a function whose body has a leading blank
// line: canonical gofmt accepts it (so stdlib go/format.Source leaves it
// untouched), but gofumpt strips it. Asserting bytes.Equal(out, gofumpt(out))
// proves Render emits gofumpt-canonical bytes — the same shape the CI
// formatter gate expects from every codegen / scaffold path.
func TestRender_OutputIsGofumptClean(t *testing.T) {
	t.Parallel()
	tpl := template.Must(template.New("blank").Parse(`package demo

func F() {

	_ = 1
}
`))

	out, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "blank",
		Templates:    tpl,
		Filename:     "demo.go",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	canonical, err := gofumpt.Source(out, codegen.GofumptOptions)
	if err != nil {
		t.Fatalf("gofumpt.Source on rendered output: %v", err)
	}
	if !bytes.Equal(out, canonical) {
		t.Errorf("Render output is not gofumpt-canonical:\n--- got\n%s\n--- gofumpt(got)\n%s",
			out, canonical)
	}
}
