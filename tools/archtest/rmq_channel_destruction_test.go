// RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01 — Every AMQPChannel destruction site in
// adapters/rabbitmq/ MUST go through Connection.CloseEphemeralChannel.
//
// Direct ch.Close() calls outside of CloseEphemeralChannel or ReleaseChannel
// bypass the inUseChannels.Add(-1) decrement and permanently leak
// MaxChannelsPerConn slots, causing spurious ERR_ADAPTER_AMQP_CHANNEL_MAX_EXCEEDED
// false-positives after enough reconnect cycles or subscription teardowns.
//
// The archtest walks all non-test .go files under adapters/rabbitmq/ with
// go/parser and flags any CallExpr whose selector is "Close" and whose receiver
// resolves to something that looks like an AMQPChannel (i.e. a local variable or
// struct field of interface type, distinct from the AMQPConnection.Close call
// on the physical TCP connection).
//
// Whitelist: the two functions that are themselves the canonical close paths are
// allowed to contain a ch.Close() call:
//
//   - Connection.CloseEphemeralChannel — the canonical single-path API itself
//   - Connection.ReleaseChannel        — delegates to CloseEphemeralChannel after
//     this archtest was applied; any residual ch.Close() here is guarded by the
//     whitelist only while ReleaseChannel still calls CloseEphemeralChannel
//     internally (the AST check verifies that only those two contain direct calls)
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN P1
// ref: adapters/rabbitmq/doc.go — AMQPChannel destruction contract
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedChannelCloseFuncs is the exhaustive whitelist of function names that
// are permitted to contain a direct ch.Close() call on an AMQPChannel.
// All other sites must call Connection.CloseEphemeralChannel instead.
var allowedChannelCloseFuncs = map[string]bool{
	// CloseEphemeralChannel is the canonical single-path API itself.
	"CloseEphemeralChannel": true,
	// waitAndClose contains a nil-conn guard (r.conn == nil branch) that calls
	// r.ch.Close() directly for unit tests that construct subscriptionRun without
	// a real Connection. The guard is unreachable in production (subscribeOnce
	// always passes s.conn). The archtest whitelist entry is intentional and
	// narrowly scoped to this one function.
	"waitAndClose": true,
}

func TestRMQChannelDestructionViaConn01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	rmqDir := filepath.Join(root, "adapters", "rabbitmq")

	entries, err := os.ReadDir(rmqDir)
	if err != nil {
		t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: read dir %s: %v", rmqDir, err)
	}

	fset := token.NewFileSet()

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		src := filepath.Join(rmqDir, name)
		f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: parse %s: %v", src, err)
		}

		checkFileForDirectChannelClose(t, fset, f, src)
	}
}

// checkFileForDirectChannelClose walks all function/method declarations in f
// and flags any call of the form <expr>.Close() where <expr> is NOT the
// physical AMQP connection (i.e. not receiver.conn.Close() / amqpConn.Close()).
//
// The heuristic used: a Close() call is an AMQPChannel close if the receiver
// identifier name contains "ch" or is a field named "ch", OR if the enclosing
// function is not in the whitelist.  Because the rabbitmq package uses the
// variable name "ch" consistently for AMQPChannel values, this is highly
// accurate without requiring type-checker infrastructure.
//
// False-positive prevention for AMQPConnection.Close:
//   - amqpConnectionWrapper.Close — receiver type is *amqpConnectionWrapper, not a channel
//   - Connection.Close — calls underlying conn.Close(), receiver is conn
//   - The physical-connection variables are named "conn", not "ch"
func checkFileForDirectChannelClose(t *testing.T, fset *token.FileSet, f *ast.File, src string) {
	t.Helper()

	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		funcName := fd.Name.Name
		if allowedChannelCloseFuncs[funcName] {
			continue
		}

		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name != "Close" {
				return true
			}

			// Determine if the receiver looks like an AMQPChannel.
			// AMQPChannel variables in this package are always named "ch".
			// Physical connection variables are named "conn" or are type-wrapped.
			receiverName := extractReceiverName(sel.X)
			if !isLikelyAMQPChannel(receiverName) {
				return true
			}

			pos := fset.Position(call.Pos())
			rel, _ := filepath.Rel(filepath.Dir(filepath.Dir(src)), src)
			if rel == "" {
				rel = src
			}
			t.Errorf(
				"RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: %s:%d: %s() contains direct %s.Close() call.\n"+
					"  All AMQPChannel destruction MUST go through Connection.CloseEphemeralChannel\n"+
					"  to keep inUseChannels in sync with MaxChannelsPerConn.\n"+
					"  Replace: %s.Close() → conn.CloseEphemeralChannel(%s)",
				rel, pos.Line, funcName, receiverName, receiverName, receiverName,
			)
			return true
		})
	}
}

// extractReceiverName returns the base identifier name from a selector receiver
// expression. For `ch.Close()` returns "ch"; for `r.ch.Close()` returns "ch";
// for `c.conn.Close()` returns "conn".
func extractReceiverName(x ast.Expr) string {
	switch e := x.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// isLikelyAMQPChannel returns true if the receiver name suggests it holds an
// AMQPChannel value. The rabbitmq package uses "ch" for all AMQPChannel
// variables and "conn"/"w" for AMQPConnection values.
func isLikelyAMQPChannel(name string) bool {
	return name == "ch"
}
