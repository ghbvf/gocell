package codegen_test

import (
	"strings"
	"testing"
	"text/template"

	"github.com/ghbvf/gocell/tools/codegen"
)

func TestRender_HappyPath(t *testing.T) {
	t.Parallel()
	tpl := template.Must(template.New("simple").Parse(`package {{.Pkg}}

import "fmt"

func {{.Fn}}() string { return fmt.Sprintf("hi %s", "world") }
`))

	out, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "simple",
		Templates:    tpl,
		Data:         map[string]string{"Pkg": "demo", "Fn": "Hello"},
		Filename:     "demo.go",
	})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "package demo") {
		t.Errorf("missing package header, got:\n%s", got)
	}
	if !strings.Contains(got, "func Hello()") {
		t.Errorf("missing function, got:\n%s", got)
	}
	if !strings.Contains(got, `"fmt"`) {
		t.Errorf("missing fmt import, got:\n%s", got)
	}
}

func TestRender_GoimportsAddsMissingImport(t *testing.T) {
	t.Parallel()
	// Source omits the fmt import — goimports must add it.
	tpl := template.Must(template.New("noimport").Parse(`package demo

func F() { fmt.Println("hi") }
`))

	out, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "noimport",
		Templates:    tpl,
		Data:         nil,
		Filename:     "demo.go",
	})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if !strings.Contains(string(out), `"fmt"`) {
		t.Errorf("expected goimports to inject fmt; got:\n%s", out)
	}
}

func TestRender_GoimportsRemovesUnusedImport(t *testing.T) {
	t.Parallel()
	tpl := template.Must(template.New("unused").Parse(`package demo

import "fmt"

func F() {}
`))

	out, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "unused",
		Templates:    tpl,
		Data:         nil,
		Filename:     "demo.go",
	})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if strings.Contains(string(out), `"fmt"`) {
		t.Errorf("expected goimports to drop unused fmt; got:\n%s", out)
	}
}

func TestRender_FormatFailureReturnsRawAndError(t *testing.T) {
	t.Parallel()
	tpl := template.Must(template.New("broken").Parse(`package demo

func ( bogus syntax {{.X}}
`))

	out, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "broken",
		Templates:    tpl,
		Data:         map[string]string{"X": "bad"},
	})
	if err == nil {
		t.Fatal("expected go/format error, got nil")
	}
	if len(out) == 0 {
		t.Error("expected raw template output for debugging on format failure")
	}
	if !strings.Contains(err.Error(), "go/format failed") {
		t.Errorf("expected wrapped format error, got %v", err)
	}
}

func TestRender_NilTemplatesIsError(t *testing.T) {
	t.Parallel()
	_, err := codegen.Render(codegen.RenderOptions{TemplateName: "x"})
	if err == nil {
		t.Fatal("expected error for nil Templates")
	}
}

func TestRender_EmptyTemplateNameIsError(t *testing.T) {
	t.Parallel()
	tpl := template.Must(template.New("x").Parse(`package x`))
	_, err := codegen.Render(codegen.RenderOptions{Templates: tpl})
	if err == nil {
		t.Fatal("expected error for empty TemplateName")
	}
}

func TestRender_TemplateExecutionError(t *testing.T) {
	t.Parallel()
	tpl := template.Must(template.New("missing").Parse(`{{.X.Y.Z}}`))
	_, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "missing",
		Templates:    tpl,
		Data:         struct{}{},
	})
	if err == nil {
		t.Fatal("expected template execution error")
	}
	if !strings.Contains(err.Error(), "execute template") {
		t.Errorf("expected execute template wrap, got %v", err)
	}
}
