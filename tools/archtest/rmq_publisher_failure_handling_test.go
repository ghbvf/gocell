// RMQ-PUBLISHER-FAILURE-HANDLING-01 — Publisher must distinguish broker NACK
// from confirm timeout / confirm-channel-close, emit slog.Warn on each failure
// branch with structured fields, and record an observability counter so the
// adapter layer's wire-level failure cause is queryable independently of the
// outbox relay's lost-counter.
//
// Gate IDs:
//
//	RMQ-PUBLISHER-FAILURE-HANDLING-01-A  Publish must reference ErrAdapterAMQPNack
//	                                      somewhere in its body (NACK errcode is
//	                                      distinct from ErrAdapterAMQPConfirmTimeout).
//	RMQ-PUBLISHER-FAILURE-HANDLING-01-B  Publish must call slog.Warn at least 3 times
//	                                      (one for each of NACK / timeout / confirmCh
//	                                      closed) — proves all failure branches are
//	                                      logged, not just one.
//	RMQ-PUBLISHER-FAILURE-HANDLING-01-C  Publish must call a publisher-collector
//	                                      RecordPublishFailure method at least once
//	                                      so a metric records the failure reason.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN
// ref: ThreeDotsLabs/watermill-amqp publisher.go — NACK returns hard error
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestRMQPublisherFailureHandling01_NackErrcodeReferenced(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatalf("RMQ-PUBLISHER-FAILURE-HANDLING-01-A: Publish method not found in %s", src)
	}

	var found bool
	ast.Inspect(publish.Body, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "ErrAdapterAMQPNack" {
			found = true
			return false
		}
		return true
	})

	if !found {
		rel, _ := filepath.Rel(root, src)
		if rel == "" {
			rel = src
		}
		t.Errorf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-A: Publish in %s must reference "+
				"ErrAdapterAMQPNack to mark broker-NACK as a distinct error code (vs "+
				"ErrAdapterAMQPConfirmTimeout). Sharing a code makes alerting rules "+
				"unable to tell broker rejection from network timeout.",
			rel,
		)
	}
}

func TestRMQPublisherFailureHandling01_AllBranchesEmitWarn(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatalf("RMQ-PUBLISHER-FAILURE-HANDLING-01-B: Publish method not found in %s", src)
	}

	const requiredWarnCalls = 3
	var warnCount int
	ast.Inspect(publish.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Warn" {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "slog" {
			warnCount++
		}
		return true
	})

	if warnCount < requiredWarnCalls {
		t.Errorf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-B: Publish in %s must call slog.Warn at "+
				"least %d times (NACK / confirm timeout / confirm-channel-closed); "+
				"found %d. Silent failure branches make on-call diagnosis impossible.",
			src, requiredWarnCalls, warnCount,
		)
	}
}

func TestRMQPublisherFailureHandling01_RecordsFailureMetric(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatalf("RMQ-PUBLISHER-FAILURE-HANDLING-01-C: Publish method not found in %s", src)
	}

	var calls int
	ast.Inspect(publish.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "RecordPublishFailure" {
			calls++
		}
		return true
	})

	if calls < 1 {
		rel, _ := filepath.Rel(root, src)
		if rel == "" {
			rel = src
		}
		t.Errorf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-C: Publish in %s must call "+
				"RecordPublishFailure on the injected PublisherCollector so the failure "+
				"reason is queryable as a metric. Defaulting to NoopPublisherCollector "+
				"keeps the call cheap; production wiring injects the provider-backed "+
				"collector at the composition root.",
			rel,
		)
	}
}

// findMethod returns the FuncDecl for a method whose name matches `name` and
// which has a non-nil receiver. Returns nil if not found.
func findMethod(f *ast.File, name string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == name && fd.Recv != nil {
			return fd
		}
	}
	return nil
}
