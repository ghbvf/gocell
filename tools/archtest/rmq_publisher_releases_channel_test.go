// RMQ-PUBLISHER-RELEASES-CHANNEL-01 — Publisher.Publish must pair every
// AcquireChannel call with a symmetric CloseEphemeralChannel (or
// ReleaseChannel) defer so that inUseChannels is always decremented when the
// publisher channel is done.
//
// Without this pairing, each Publish increments inUseChannels but never
// rolls it back; after MaxChannelsPerConn (default 256) calls all
// subsequent publishes fail with ErrAdapterAMQPChannelMaxExceeded.
//
// Gate ID: RMQ-PUBLISHER-RELEASES-CHANNEL-01
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN
// ref: adapters/rabbitmq/connection.go CloseEphemeralChannel
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestRMQPublisherReleasesChannel01 verifies that Publisher.Publish acquires a
// channel and pairs it with a defer that calls either
// p.conn.CloseEphemeralChannel or p.conn.ReleaseChannel.
//
// AST strategy:
//  1. Parse adapters/rabbitmq/publisher.go.
//  2. Find the Publish method on *Publisher.
//  3. Verify that the method body contains an AcquireChannel call site.
//  4. Verify that the method body contains at least one defer statement whose
//     call expression is p.conn.CloseEphemeralChannel or p.conn.ReleaseChannel.
func TestRMQPublisherReleasesChannel01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("RMQ-PUBLISHER-RELEASES-CHANNEL-01: parse %s: %v", src, err)
	}

	// Locate Publisher.Publish method.
	var publishMethod *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Name.Name != "Publish" {
			continue
		}
		publishMethod = fd
		break
	}
	if publishMethod == nil {
		t.Fatalf("RMQ-PUBLISHER-RELEASES-CHANNEL-01: Publisher.Publish method not found in %s", src)
	}

	// Check that AcquireChannel is called in the method body.
	var hasAcquire bool
	ast.Inspect(publishMethod.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "AcquireChannel" {
			hasAcquire = true
			return false
		}
		return true
	})
	if !hasAcquire {
		t.Errorf(
			"RMQ-PUBLISHER-RELEASES-CHANNEL-01: Publisher.Publish must call " +
				"conn.AcquireChannel to obtain a channel for confirm-mode publish.",
		)
	}

	// Check that there is a defer calling CloseEphemeralChannel or ReleaseChannel.
	//
	// We accept two forms:
	//   defer p.conn.CloseEphemeralChannel(ch)   — direct call expr
	//   defer func() { ... p.conn.CloseEphemeralChannel(ch) ... }()  — closure
	//
	// The AST check inspects all DeferStmt nodes in the method body for a
	// selector expression whose name is CloseEphemeralChannel or ReleaseChannel.
	releaseSelectors := map[string]bool{
		"CloseEphemeralChannel": true,
		"ReleaseChannel":        true,
	}

	var hasRelease bool
	ast.Inspect(publishMethod.Body, func(n ast.Node) bool {
		ds, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		// Walk the entire defer statement subtree for the release selector.
		ast.Inspect(ds, func(inner ast.Node) bool {
			sel, ok := inner.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if releaseSelectors[sel.Sel.Name] {
				hasRelease = true
				return false
			}
			return true
		})
		return !hasRelease
	})

	if !hasRelease {
		t.Errorf(
			"RMQ-PUBLISHER-RELEASES-CHANNEL-01: Publisher.Publish must pair " +
				"AcquireChannel with a deferred p.conn.CloseEphemeralChannel " +
				"(or p.conn.ReleaseChannel) call. Without this pairing every Publish " +
				"leaks one inUseChannels slot; after MaxChannelsPerConn (=256) " +
				"publishes all subsequent calls fail with " +
				"ErrAdapterAMQPChannelMaxExceeded.",
		)
	}
}
