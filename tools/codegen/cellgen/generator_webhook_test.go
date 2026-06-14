package cellgen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/pkg/testutil/fileutil"
	"github.com/ghbvf/gocell/tools/codegen"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// TestRenderCell_GoldenWebhook renders a synthetic "hooks" cell project
// containing both a webhook-receive slice (stripeingest) and a webhook-dispatch
// slice (shopifypush) through cell.tmpl and compares the result against the
// committed golden file synth_webhook_cell_gen.go.golden.
//
// When run with -update it regenerates the golden file instead of comparing.
//
// Synth project shape:
//   - cell ID: "hooks", GoStructName: "HooksCell"
//   - slice "stripeingest": CU role=webhook-receive, contract webhook.stripe.payment-events.v1,
//     sourceID=stripe, handler=HandleStripeEvent → field stripeSvc
//   - slice "shopifypush": CU role=webhook-dispatch, contract webhook.shopify.orders.v1,
//     sourceID=shopify, targetSelector=ShopifyTarget → field shopifySvc
//
// Expected Init body (see golden file):
//
//	reg.RegisterWebhookReceiver(webhook.ReceiverSpec{...}, c.stripeSvc.HandleStripeEvent)
//	reg.RegisterWebhookDispatch(webhook.DispatchSpec{...}, c.shopifySvc.ShopifyTarget)
func TestRenderCell_GoldenWebhook(t *testing.T) {
	t.Parallel()

	project := buildWebhookSyntheticProject()
	fieldIndex := webhookSyntheticFieldIndex()

	spec, err := BuildCellSpec(project, "hooks", webhookSyntheticBundle(), fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}

	out, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "cell.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     "hooks/cell_gen.go",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	goldenPath := filepath.Join("testdata", "golden", "synth_webhook_cell_gen.go.golden")
	if *updateGolden {
		if err := os.WriteFile(goldenPath, out, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("golden file updated: %s", goldenPath)
		return
	}

	golden := fileutil.MustReadFile(t, goldenPath)
	if !bytes.Equal(out, golden) {
		t.Errorf("rendered output diverges from golden:\n--- got ---\n%s\n--- want ---\n%s", out, golden)
	}

	// Lightweight assertion: the receiver registration block must carry the
	// correct CellID field value ("hooks"). This guards against regressions
	// where the cellgen template omits or mis-derives CellID from the cell
	// metadata, which would silently break observability labeling at runtime.
	outStr := string(out)
	if !bytes.Contains(out, []byte(`CellID:           "hooks"`)) {
		t.Errorf("generated output does not contain CellID field for receiver registration; got:\n%s", outStr)
	}
}

// buildWebhookSyntheticProject builds the in-memory ProjectMeta for the
// webhook golden test. Two slices: stripeingest (webhook-receive) and
// shopifypush (webhook-dispatch).
func buildWebhookSyntheticProject() *metadata.ProjectMeta {
	cell := &metadata.CellMeta{
		ID:           "hooks",
		Dir:          "hooks",
		File:         "cells/hooks/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("HooksCell"),
	}
	stripeSlice := &metadata.SliceMeta{
		ID:            "stripeingest",
		BelongsToCell: "hooks",
		Dir:           "stripeingest",
		File:          "cells/hooks/slices/stripeingest/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract: "webhook.stripe.payment-events.v1",
				Role:     "webhook-receive",
				Handler:  "HandleStripeEvent",
				SourceID: "stripe",
			},
		},
	}
	shopifySlice := &metadata.SliceMeta{
		ID:            "shopifypush",
		BelongsToCell: "hooks",
		Dir:           "shopifypush",
		File:          "cells/hooks/slices/shopifypush/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:       "webhook.shopify.orders.v1",
				Role:           "webhook-dispatch",
				TargetSelector: "ShopifyTarget",
				SourceID:       "shopify",
			},
		},
	}
	stripeContract := &metadata.ContractMeta{
		ID:        "webhook.stripe.payment-events.v1",
		Kind:      "webhook",
		Direction: "inbound",
		Signature: &metadata.WebhookSignatureMeta{
			Algorithm:        "hmac-sha256",
			DeliveryIDHeader: "svix-id",
			TimestampHeader:  "svix-timestamp",
			SignatureHeader:  "svix-signature",
			ToleranceSeconds: 300,
		},
		Endpoints: metadata.EndpointsMeta{
			Inbound: &metadata.WebhookInboundMeta{
				PathPattern: "/api/webhooks/stripe/payment-events",
				SourceID:    "stripe",
			},
		},
		Payload: &metadata.WebhookPayloadMeta{
			MaxBodyBytes: 1048576,
		},
	}
	shopifyContract := &metadata.ContractMeta{
		ID:   "webhook.shopify.orders.v1",
		Kind: "webhook",
	}
	return fixtureProject(
		cell,
		[]*metadata.SliceMeta{stripeSlice, shopifySlice},
		[]*metadata.ContractMeta{stripeContract, shopifyContract},
	)
}

// webhookSyntheticFieldIndex maps the two slice package names to the cell
// struct fields holding them.
func webhookSyntheticFieldIndex() *CellFieldIndex {
	return idxOf(map[string]string{
		"stripeingest": "stripeSvc",
		"shopifypush":  "shopifySvc",
	})
}

// webhookSyntheticBundle returns the WireBundle for the hooks cell.
// No HTTP routes — webhook cells typically don't mount chi routes.
func webhookSyntheticBundle() markergen.WireBundle {
	return markergen.WireBundle{}
}
