package codegen_test

import (
	"bytes"
	"testing"
	"text/template"

	gofumpt "mvdan.cc/gofumpt/format"

	"github.com/ghbvf/gocell/tools/codegen"
)

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
