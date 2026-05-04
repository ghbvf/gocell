package archtest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestOutboxHandleResultNoReceiptField enforces
// OUTBOX-HANDLERESULT-NO-RECEIPT-FIELD-01: kernel/outbox.HandleResult must
// not declare a Receipt field. The field was removed in 029 K#12 (PR-V1-
// OUTBOX-RECEIPT-EXTRACT) — Settlement is now delivered via SubscriberHandler
// return value, not embedded in HandleResult.
//
// This gate supersedes the prior HANDLER-RECEIPT-WRITE-01 detector (cell
// handlers writing HandleResult.Receipt). The struct field no longer exists,
// so no handler can write it; the structural absence is the stronger guard.
func TestOutboxHandleResultNoReceiptField(t *testing.T) {
	root := orFindModuleRoot(t)
	path := filepath.Join(root, "kernel", "outbox", "outbox.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var (
		found        bool
		receiptLine  int
		receiptField string
	)
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "HandleResult" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return false
		}
		found = true
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if name.Name == "Receipt" {
					receiptLine = fset.Position(name.Pos()).Line
					receiptField = name.Name
				}
			}
		}
		return false
	})

	if !found {
		t.Fatal("HandleResult struct definition not found in kernel/outbox/outbox.go")
	}
	if receiptField != "" {
		t.Errorf("OUTBOX-HANDLERESULT-NO-RECEIPT-FIELD-01: kernel/outbox/outbox.go:%d: "+
			"HandleResult must not declare a %s field — Settlement is delivered "+
			"via SubscriberHandler return value (see 029 K#12)",
			receiptLine, receiptField)
	}
}

// orFindModuleRoot walks up from the test working directory to find go.mod.
func orFindModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from working directory")
		}
		dir = parent
	}
}
