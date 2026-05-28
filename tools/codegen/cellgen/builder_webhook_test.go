package cellgen

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// TestBuildWebhookReceiversFromSlices_HappyPath verifies that a slice with
// role=webhook-receive produces a WebhookReceiverGenSpec with the correct
// ContractID, SourceID, and HandlerExpr.
func TestBuildWebhookReceiversFromSlices_HappyPath(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           "hooks",
		Dir:          "hooks",
		File:         "cells/hooks/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("HooksCell"),
	}
	slc := &metadata.SliceMeta{
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
	contract := &metadata.ContractMeta{
		ID:   "webhook.stripe.payment-events.v1",
		Kind: "webhook",
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	fieldIndex := idxOf(map[string]string{"stripeingest": "stripeSvc"})

	spec, err := BuildCellSpec(p, "hooks", markergen.WireBundle{}, fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.WebhookReceivers) != 1 {
		t.Fatalf("WebhookReceivers len = %d, want 1", len(spec.WebhookReceivers))
	}
	recv := spec.WebhookReceivers[0]
	if recv.ContractID != "webhook.stripe.payment-events.v1" {
		t.Errorf("ContractID = %q, want webhook.stripe.payment-events.v1", recv.ContractID)
	}
	if recv.SourceID != "stripe" {
		t.Errorf("SourceID = %q, want stripe", recv.SourceID)
	}
	if recv.HandlerExpr != "c.stripeSvc.HandleStripeEvent" {
		t.Errorf("HandlerExpr = %q, want c.stripeSvc.HandleStripeEvent", recv.HandlerExpr)
	}
}

// TestBuildWebhookDispatchesFromSlices_HappyPath verifies that a slice with
// role=webhook-dispatch produces a WebhookDispatchGenSpec with the correct
// ContractID, SourceID, and SelectorExpr.
func TestBuildWebhookDispatchesFromSlices_HappyPath(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           "hooks",
		Dir:          "hooks",
		File:         "cells/hooks/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("HooksCell"),
	}
	slc := &metadata.SliceMeta{
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
	contract := &metadata.ContractMeta{
		ID:   "webhook.shopify.orders.v1",
		Kind: "webhook",
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	fieldIndex := idxOf(map[string]string{"shopifypush": "shopifySvc"})

	spec, err := BuildCellSpec(p, "hooks", markergen.WireBundle{}, fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.WebhookDispatches) != 1 {
		t.Fatalf("WebhookDispatches len = %d, want 1", len(spec.WebhookDispatches))
	}
	disp := spec.WebhookDispatches[0]
	if disp.ContractID != "webhook.shopify.orders.v1" {
		t.Errorf("ContractID = %q, want webhook.shopify.orders.v1", disp.ContractID)
	}
	if disp.SourceID != "shopify" {
		t.Errorf("SourceID = %q, want shopify", disp.SourceID)
	}
	if disp.SelectorExpr != "c.shopifySvc.ShopifyTarget" {
		t.Errorf("SelectorExpr = %q, want c.shopifySvc.ShopifyTarget", disp.SelectorExpr)
	}
}

// TestBuildWebhookReceivers_InvalidHandler verifies that a webhook-receive CU
// with a non-exported identifier as handler is rejected.
func TestBuildWebhookReceivers_InvalidHandler(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "webhooksvc",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "webhooksvc",
		File:          "cells/demo/slices/webhooksvc/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract: "webhook.stripe.payment-events.v1",
				Role:     "webhook-receive",
				Handler:  "handleStripe", // lowercase — invalid exported ident
				SourceID: "stripe",
			},
		},
	}
	contract := &metadata.ContractMeta{
		ID:   "webhook.stripe.payment-events.v1",
		Kind: "webhook",
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	fieldIndex := idxOf(map[string]string{"webhooksvc": "webhookSvc"})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for unexported handler identifier, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "handler") && !strings.Contains(err.Error(), "exported") {
		t.Errorf("error should mention handler or exported, got: %v", err)
	}
}

// TestBuildWebhookDispatches_InvalidTargetSelector verifies that a
// webhook-dispatch CU with a non-exported identifier as targetSelector is rejected.
func TestBuildWebhookDispatches_InvalidTargetSelector(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "webhooksvc",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "webhooksvc",
		File:          "cells/demo/slices/webhooksvc/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:       "webhook.shopify.orders.v1",
				Role:           "webhook-dispatch",
				TargetSelector: "shopifyTarget", // lowercase — invalid
				SourceID:       "shopify",
			},
		},
	}
	contract := &metadata.ContractMeta{
		ID:   "webhook.shopify.orders.v1",
		Kind: "webhook",
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	fieldIndex := idxOf(map[string]string{"webhooksvc": "webhookSvc"})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for unexported targetSelector identifier, got nil")
	}
}

// TestBuildWebhookReceivers_MissingContract verifies that a webhook-receive CU
// referencing an unknown contract is rejected.
func TestBuildWebhookReceivers_MissingContract(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "webhooksvc",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "webhooksvc",
		File:          "cells/demo/slices/webhooksvc/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract: "webhook.ghost.v1", // does not exist in project
				Role:     "webhook-receive",
				Handler:  "HandleGhost",
				SourceID: "ghost",
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil) // no contracts
	fieldIndex := idxOf(map[string]string{"webhooksvc": "webhookSvc"})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for missing contract, got nil")
	}
	if !strings.Contains(err.Error(), "contract") {
		t.Errorf("error should mention contract, got: %v", err)
	}
}

// TestBuildWebhookReceivers_NonWebhookContract verifies that a webhook-receive
// CU targeting a non-webhook contract kind is rejected.
func TestBuildWebhookReceivers_NonWebhookContract(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "webhooksvc",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "webhooksvc",
		File:          "cells/demo/slices/webhooksvc/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract: "event.foo.v1", // kind=event, not webhook
				Role:     "webhook-receive",
				Handler:  "HandleFoo",
				SourceID: "foo",
			},
		},
	}
	contract := &metadata.ContractMeta{
		ID:   "event.foo.v1",
		Kind: "event",
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	fieldIndex := idxOf(map[string]string{"webhooksvc": "webhookSvc"})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for non-webhook contract kind, got nil")
	}
	if !strings.Contains(err.Error(), "webhook") {
		t.Errorf("error should mention webhook kind, got: %v", err)
	}
}

// TestBuildWebhookReceivers_FieldIndexAmbiguity verifies that a webhook-receive
// CU whose slice is ambiguous in the field index (multiple same-package fields)
// is rejected when no explicit field: is provided.
func TestBuildWebhookReceivers_FieldIndexAmbiguity(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "stripeingest",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "stripeingest",
		File:          "cells/demo/slices/stripeingest/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract: "webhook.stripe.payment-events.v1",
				Role:     "webhook-receive",
				Handler:  "HandleStripeEvent",
				SourceID: "stripe",
				// No Field: set — ambiguity should fail
			},
		},
	}
	contract := &metadata.ContractMeta{
		ID:   "webhook.stripe.payment-events.v1",
		Kind: "webhook",
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	// Ambiguous: two *stripeingest.T fields
	fieldIndex := &CellFieldIndex{
		byPkg:   map[string]string{"stripeingest": ambiguousField},
		byField: map[string]string{"stripeHandler": "stripeingest", "stripeConsumer": "stripeingest"},
	}

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
	if !strings.Contains(err.Error(), "disambiguate") && !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention ambiguity/disambiguation, got: %v", err)
	}
}

// TestBuildWebhookReceivers_InvalidSourceID verifies that a webhook-receive CU
// with a malformed sourceID is rejected at codegen time (not silently baked into
// generated source).
func TestBuildWebhookReceivers_InvalidSourceID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		sourceID string
	}{
		{"uppercase", "Stripe"},
		{"leading digit", "1stripe"},
		{"contains colon", "stripe:v1"},
		{"empty", ""},
		{"contains slash", "str/ipe"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cell := &metadata.CellMeta{
				ID:           metadatatest.CellIDDemo,
				Dir:          "demo",
				File:         "cells/demo/cell.yaml",
				GoStructName: metadata.MustNewGoIdentifier("Demo"),
			}
			slc := &metadata.SliceMeta{
				ID:            "webhooksvc",
				BelongsToCell: metadatatest.CellIDDemo,
				Dir:           "webhooksvc",
				File:          "cells/demo/slices/webhooksvc/slice.yaml",
				ContractUsages: []metadata.ContractUsage{
					{
						Contract: "webhook.stripe.payment-events.v1",
						Role:     "webhook-receive",
						Handler:  "HandleStripeEvent",
						SourceID: tc.sourceID,
					},
				},
			}
			contract := &metadata.ContractMeta{
				ID:   "webhook.stripe.payment-events.v1",
				Kind: "webhook",
			}
			p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
			fieldIndex := idxOf(map[string]string{"webhooksvc": "webhookSvc"})

			_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
			if err == nil {
				t.Fatalf("expected error for invalid sourceID %q, got nil", tc.sourceID)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "sourceid") &&
				!strings.Contains(err.Error(), "source") {
				t.Errorf("error should mention sourceID, got: %v", err)
			}
		})
	}
}

// TestBuildWebhookDispatches_InvalidSourceID verifies that a webhook-dispatch CU
// with a malformed sourceID is rejected at codegen time.
func TestBuildWebhookDispatches_InvalidSourceID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		sourceID string
	}{
		{"uppercase", "Shopify"},
		{"leading digit", "9shopify"},
		{"empty", ""},
		{"contains dot", "shop.ify"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cell := &metadata.CellMeta{
				ID:           metadatatest.CellIDDemo,
				Dir:          "demo",
				File:         "cells/demo/cell.yaml",
				GoStructName: metadata.MustNewGoIdentifier("Demo"),
			}
			slc := &metadata.SliceMeta{
				ID:            "shopifypush",
				BelongsToCell: metadatatest.CellIDDemo,
				Dir:           "shopifypush",
				File:          "cells/demo/slices/shopifypush/slice.yaml",
				ContractUsages: []metadata.ContractUsage{
					{
						Contract:       "webhook.shopify.orders.v1",
						Role:           "webhook-dispatch",
						TargetSelector: "ShopifyTarget",
						SourceID:       tc.sourceID,
					},
				},
			}
			contract := &metadata.ContractMeta{
				ID:   "webhook.shopify.orders.v1",
				Kind: "webhook",
			}
			p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
			fieldIndex := idxOf(map[string]string{"shopifypush": "shopifySvc"})

			_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
			if err == nil {
				t.Fatalf("expected error for invalid sourceID %q, got nil", tc.sourceID)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "sourceid") &&
				!strings.Contains(err.Error(), "source") {
				t.Errorf("error should mention sourceID, got: %v", err)
			}
		})
	}
}

// TestBuildWebhookDispatches_MissingContract verifies that a webhook-dispatch CU
// referencing an unknown contract is rejected.
func TestBuildWebhookDispatches_MissingContract(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "shopifypush",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "shopifypush",
		File:          "cells/demo/slices/shopifypush/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:       "webhook.ghost.v1",
				Role:           "webhook-dispatch",
				TargetSelector: "GhostTarget",
				SourceID:       "ghost",
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)
	fieldIndex := idxOf(map[string]string{"shopifypush": "shopifySvc"})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for missing dispatch contract, got nil")
	}
	if !strings.Contains(err.Error(), "contract") {
		t.Errorf("error should mention contract, got: %v", err)
	}
}
