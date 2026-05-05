// OUTBOX-PAYLOAD-SIZE-01 — kernel/outbox payload byte cap invariant.
//
// kernel/outbox.MaxPayloadBytes is the canonical upper bound on
// `Entry.Payload` length. A buggy or malicious producer that writes multi-MB
// JSON would otherwise inflate relay batch memory and PostgreSQL replication
// delay. The invariant has two parts:
//
//	OUTBOX-PAYLOAD-SIZE-01-A  kernel/outbox/outbox.go declares MaxPayloadBytes
//	OUTBOX-PAYLOAD-SIZE-01-B  kernel/outbox/outbox.go references MaxPayloadBytes
//	                          inside Entry.Validate (so the cap actually fires)
//
// We assert both parts with an AST scan rather than a runtime test so the
// gate fails CI with a clear message even when the production code never
// reaches a sized payload.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B6 PR-V1-PG-OUTBOX-RELAY-HARDEN
// ref: backlog2.md B2-A-07 (repurposed: metadata cap shipped, payload cap was the
//      true unguarded vector).
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutboxPayloadSize01_ConstantDeclaredAndUsedByValidate(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "kernel", "outbox", "outbox.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	const constName = "MaxPayloadBytes"
	var declared bool
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if name.Name == constName {
					declared = true
				}
			}
		}
	}
	if !declared {
		t.Errorf(
			"OUTBOX-PAYLOAD-SIZE-01-A: kernel/outbox/outbox.go must declare const %s "+
				"(payload byte cap; see roadmap B6, backlog2 B2-A-07 repurposed scope).",
			constName,
		)
	}

	// Walk Entry.Validate body for any reference to MaxPayloadBytes.
	var validate *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name != "Validate" || fd.Recv == nil || len(fd.Recv.List) != 1 {
			continue
		}
		// Recv must be `e Entry` (value receiver, type Entry).
		ident, ok := fd.Recv.List[0].Type.(*ast.Ident)
		if !ok || ident.Name != "Entry" {
			continue
		}
		validate = fd
		break
	}
	if validate == nil {
		t.Fatalf("OUTBOX-PAYLOAD-SIZE-01-B: cannot locate (Entry).Validate in %s", src)
	}
	var referenced bool
	ast.Inspect(validate.Body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && id.Name == constName {
			referenced = true
			return false
		}
		return true
	})
	if !referenced {
		t.Errorf(
			"OUTBOX-PAYLOAD-SIZE-01-B: (Entry).Validate body must reference %s "+
				"(otherwise the cap is decorative). Insert "+
				"`if len(e.Payload) > %s { return errcode.New(...) }`.",
			constName, constName,
		)
	}

	// Sanity: ensure the file's package import path is what we expect, so a
	// future reorganization that moves the file does not silently bypass the
	// gate. (`outbox` is the package name.)
	if f.Name.Name != "outbox" {
		t.Errorf("expected package outbox, got %s", f.Name.Name)
	}
	_ = strings.TrimSpace
}
