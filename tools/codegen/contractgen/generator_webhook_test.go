package contractgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// setupWebhookRoot copies the synth_webhook fixture into a fresh t.TempDir()
// and parses it. Returns (root, project).
func setupWebhookRoot(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "synth", "synth_webhook"))
	if err != nil {
		t.Fatalf("abs path synth_webhook: %v", err)
	}
	root := t.TempDir()
	copyDirIntoTemp(t, abs, root)
	goMod := "module github.com/ghbvf/gocell\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	p, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("parse synth_webhook from tmp: %v", err)
	}
	return root, p
}

// TestGenerate_Webhook_KindRecognized verifies that contractgen does NOT error
// on a webhook contract (i.e., the kind is recognized). This is the primary
// RED assertion: currently buildContractSpec will return
// "unsupported kind webhook" until the impl adds "webhook" to the kind switch.
func TestGenerate_Webhook_KindRecognized(t *testing.T) {
	t.Parallel()
	root, p := setupWebhookRoot(t)

	_, err := Generate(root, p, Options{Scope: ScopeAll{}, ModulePath: "github.com/ghbvf/gocell"})
	if err != nil {
		t.Fatalf("Generate with webhook contract must not error (kind not recognized?): %v", err)
	}
}

// TestGenerate_Webhook_ZeroArtifacts verifies that webhook contracts produce
// ZERO generated Go artifacts. By design, webhook codegen is handled by
// cellgen (reg.RegisterWebhookReceiver / RegisterWebhookDispatch), not
// contractgen. This contrast with http (3 artifacts) and event (4 artifacts)
// documents the "recognized but zero artifacts" semantic.
func TestGenerate_Webhook_ZeroArtifacts(t *testing.T) {
	t.Parallel()
	root, p := setupWebhookRoot(t)

	res, err := Generate(root, p, Options{Scope: ScopeAll{}, ModulePath: "github.com/ghbvf/gocell"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.Generated) != 0 {
		t.Errorf("webhook contract should produce 0 generated artifacts, got %d: %v",
			len(res.Generated), res.Generated)
	}
}

// TestRenderContractArtifacts_Webhook_ZeroArtifacts verifies that
// RenderContractArtifacts also returns zero artifacts for a webhook contract
// (consistent with Generate behavior).
func TestRenderContractArtifacts_Webhook_ZeroArtifacts(t *testing.T) {
	t.Parallel()
	root, p := setupWebhookRoot(t)

	artifacts, err := RenderContractArtifacts(root, p, "webhook.stripe.payment-events.v1", "github.com/ghbvf/gocell")
	if err != nil {
		t.Fatalf("RenderContractArtifacts: %v", err)
	}
	if len(artifacts) != 0 {
		t.Errorf("webhook contract should produce 0 artifacts from RenderContractArtifacts, got %d: %v",
			len(artifacts), artifacts)
	}
}

// TestGenerate_HTTP_ProducesArtifacts verifies that http contracts still
// produce > 0 artifacts (regression guard: webhook zero-artifact path must
// not accidentally suppress http generation).
func TestGenerate_HTTP_ProducesArtifacts_Contrast(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPMinimalRoot(t)

	res, err := Generate(root, p, Options{Scope: ScopeAll{}, ModulePath: "github.com/ghbvf/gocell"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.Generated) == 0 {
		t.Error("http contract should produce >0 artifacts")
	}
}

// TestGenerate_Event_ProducesArtifacts_Contrast verifies that event contracts
// still produce > 0 artifacts (regression guard: webhook zero-artifact path
// must not accidentally suppress event generation).
func TestGenerate_Event_ProducesArtifacts_Contrast(t *testing.T) {
	t.Parallel()
	root, p := setupEventRoot(t)

	res, err := Generate(root, p, Options{Scope: ScopeAll{}, ModulePath: "github.com/ghbvf/gocell"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.Generated) == 0 {
		t.Error("event contract should produce >0 artifacts")
	}
}
