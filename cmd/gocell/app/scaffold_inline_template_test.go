// INVARIANT: SCAFFOLD-INLINE-TEMPLATE-ARCHTEST
//
// inlineContractYAMLTpl (cmd/gocell/app/scaffold.go) emits a YAML document
// where the top-level key `codegen` has scalar value `false`. This is the
// inverse of SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01 (which asserts the bundle
// path omits the key entirely) — the standalone scaffold path uses the
// explicit `codegen: false` to ride the K#09 parser funnel
// (SCAFFOLD-CONTRACT-CODEGEN-DEFAULT-TRUE) and keep draft kind=command
// contracts deferred. Template degradation that loses the literal silently
// flips drafts to codegen=true, breaking the deferred-command workflow.
//
// AI-robust evaluation: Medium. Real-output capture (renders the template,
// parses the YAML, asserts semantic key/value pair) is robust to whitespace
// or formatting changes but fails immediately if the key or its value
// degrades. Cannot be Hard: the template is text/template hand-written; no
// type-system enforcement available. Sister archtest
// SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01 uses the same model. Hard upgrade
// would require codegen-from-schema for the inline templates (tracked under
// backlog SCAFFOLD-INLINE-TEMPLATE-HARDEN — tracked under issue #869).
package app

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestRenderInlineContractYAML_EmitsCodegenFalse asserts that
// renderInlineContractYAML produces a YAML document where the top-level
// mapping contains a `codegen` key with value `false`.
//
// INVARIANT: SCAFFOLD-INLINE-TEMPLATE-ARCHTEST
// AI-robust: Medium (real-output capture); see file-level godoc for rationale.
//
// Blind-spot self-check: this test only walks top-level MappingNode.Content
// pairs. Nested `codegen:` keys (e.g. under `endpoints:`) would not be
// found. However, the invariant is specifically about the top-level `codegen:`
// field — a nested codegen key would not affect parser funnel behavior.
// Reverse blind-spot: a `codegen:` key nested below the top level MUST NOT
// appear in the generated output (no such key exists today; template edit
// placing it nested instead of top-level would be caught by the fact that
// the top-level assertion would fail).
func TestRenderInlineContractYAML_EmitsCodegenFalse(t *testing.T) {
	t.Parallel()

	out, err := renderInlineContractYAML("test.http.foo.v1", "http", "acell")
	if err != nil {
		t.Fatalf("renderInlineContractYAML: %v", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(out, &root); err != nil {
		t.Fatalf("yaml.Unmarshal: %v\noutput was:\n%s", err, out)
	}
	if root.Kind != yaml.DocumentNode {
		t.Fatalf("expected DocumentNode at root, got %v", root.Kind)
	}
	if len(root.Content) != 1 {
		t.Fatalf("expected exactly 1 top-level node, got %d", len(root.Content))
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		t.Fatalf("expected MappingNode as document root, got %v", mapping.Kind)
	}

	// Walk top-level key/value pairs (pairs of nodes: even=key, odd=value).
	var codegenValue string
	var found bool
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		value := mapping.Content[i+1]
		if key.Kind == yaml.ScalarNode && key.Value == "codegen" {
			found = true
			codegenValue = value.Value
			break
		}
	}

	if !found {
		t.Errorf("INVARIANT SCAFFOLD-INLINE-TEMPLATE-ARCHTEST violated: "+
			"renderInlineContractYAML output has no top-level `codegen:` key; "+
			"the standalone scaffold path must emit `codegen: false` so that "+
			"draft contracts opt out of the K#09 codegen=true parser default.\n"+
			"output was:\n%s", out)
		return
	}
	if codegenValue != "false" {
		t.Errorf("INVARIANT SCAFFOLD-INLINE-TEMPLATE-ARCHTEST violated: "+
			"renderInlineContractYAML emits `codegen: %s`; want `codegen: false`. "+
			"The standalone scaffold path must explicitly opt draft contracts out "+
			"of the K#09 codegen=true parser funnel default.\n"+
			"output was:\n%s", codegenValue, out)
	}
}

// TestRenderInlineContractYAML_GRPCDraft asserts the kind=grpc branch renders a
// draft that parses into a ContractMeta with the endpoints.grpc subtree
// populated (service/method/proto) and server/clients set — the standalone
// draft is codegen=false so it has no dependency on grpc codegen (PR 2/6).
func TestRenderInlineContractYAML_GRPCDraft(t *testing.T) {
	t.Parallel()

	out, err := renderInlineContractYAML("grpc.demo.ping.v1", "grpc", "democell")
	if err != nil {
		t.Fatalf("renderInlineContractYAML: %v", err)
	}

	var cm metadata.ContractMeta
	if err := yaml.Unmarshal(out, &cm); err != nil {
		t.Fatalf("yaml.Unmarshal grpc draft: %v\noutput was:\n%s", err, out)
	}
	if cm.Kind != "grpc" {
		t.Fatalf("kind = %q, want grpc", cm.Kind)
	}
	if cm.Endpoints.Server != "democell" {
		t.Errorf("endpoints.server = %q, want democell (grpc provider mirrors http server)", cm.Endpoints.Server)
	}
	if cm.Endpoints.GRPC == nil {
		t.Fatalf("endpoints.grpc subtree is nil; grpc draft must emit it\noutput was:\n%s", out)
	}
	if cm.Endpoints.GRPC.Service == "" || cm.Endpoints.GRPC.Method == "" || cm.Endpoints.GRPC.Proto == "" {
		t.Errorf("endpoints.grpc must have service/method/proto, got %+v", cm.Endpoints.GRPC)
	}
	if cm.Endpoints.GRPC.Auth.Public {
		t.Error("grpc draft must default to auth.public=false (secure default)")
	}
}
