package cellgen

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// stripeInboundContract returns a fully-configured inbound webhook contract for
// stripe, usable as a fixture in receiver happy-path tests.
func stripeInboundContract() *metadata.ContractMeta {
	return &metadata.ContractMeta{
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
}

// TestBuildWebhookReceiversFromSlices_HappyPath verifies that a slice with
// role=webhook-receive produces a WebhookReceiverGenSpec with the correct
// ContractID, SourceID, HandlerExpr, and baked runtime config fields.
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
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{stripeInboundContract()})
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
	// Baked runtime config assertions.
	if recv.PathPattern != "/api/webhooks/stripe/payment-events" {
		t.Errorf("PathPattern = %q, want /api/webhooks/stripe/payment-events", recv.PathPattern)
	}
	if recv.DeliveryIDHeader != "svix-id" {
		t.Errorf("DeliveryIDHeader = %q, want svix-id", recv.DeliveryIDHeader)
	}
	if recv.TimestampHeader != "svix-timestamp" {
		t.Errorf("TimestampHeader = %q, want svix-timestamp", recv.TimestampHeader)
	}
	if recv.SignatureHeader != "svix-signature" {
		t.Errorf("SignatureHeader = %q, want svix-signature", recv.SignatureHeader)
	}
	if recv.ToleranceSeconds != 300 {
		t.Errorf("ToleranceSeconds = %d, want 300", recv.ToleranceSeconds)
	}
	if recv.MaxBodyBytes != 1048576 {
		t.Errorf("MaxBodyBytes = %d, want 1048576", recv.MaxBodyBytes)
	}
}

// TestBuildWebhookReceivers_SortedBySliceIDThenContractID verifies the ordering
// invariant: receivers are sorted by (SliceID, ContractID), mirroring the
// subscription path. The fixture is chosen so SliceID order is the REVERSE of
// ContractID order, so the assertion deterministically distinguishes the
// (SliceID, ContractID) sort from a ContractID-only sort.
func TestBuildWebhookReceivers_SortedBySliceIDThenContractID(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           "hooks",
		Dir:          "hooks",
		File:         "cells/hooks/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("HooksCell"),
	}
	// alphaingest (low SliceID) receives the high-ContractID contract;
	// zebraingest (high SliceID) receives the low-ContractID contract.
	alpha := &metadata.SliceMeta{
		ID: "alphaingest", BelongsToCell: "hooks", Dir: "alphaingest",
		File: "cells/hooks/slices/alphaingest/slice.yaml",
		ContractUsages: []metadata.ContractUsage{{
			Contract: "webhook.zzz.events.v1", Role: "webhook-receive",
			Handler: "HandleZzz", SourceID: "zzz",
		}},
	}
	zebra := &metadata.SliceMeta{
		ID: "zebraingest", BelongsToCell: "hooks", Dir: "zebraingest",
		File: "cells/hooks/slices/zebraingest/slice.yaml",
		ContractUsages: []metadata.ContractUsage{{
			Contract: "webhook.aaa.events.v1", Role: "webhook-receive",
			Handler: "HandleAaa", SourceID: "aaa",
		}},
	}
	contracts := []*metadata.ContractMeta{
		{
			ID: "webhook.zzz.events.v1", Kind: "webhook", Direction: "inbound",
			Signature: &metadata.WebhookSignatureMeta{
				DeliveryIDHeader: "x-delivery-id", TimestampHeader: "x-ts", SignatureHeader: "x-sig",
				ToleranceSeconds: 60,
			},
			Endpoints: metadata.EndpointsMeta{Inbound: &metadata.WebhookInboundMeta{PathPattern: "/webhooks/zzz", SourceID: "zzz"}},
			Payload:   &metadata.WebhookPayloadMeta{MaxBodyBytes: 65536},
		},
		{
			ID: "webhook.aaa.events.v1", Kind: "webhook", Direction: "inbound",
			Signature: &metadata.WebhookSignatureMeta{
				DeliveryIDHeader: "x-delivery-id", TimestampHeader: "x-ts", SignatureHeader: "x-sig",
				ToleranceSeconds: 60,
			},
			Endpoints: metadata.EndpointsMeta{Inbound: &metadata.WebhookInboundMeta{PathPattern: "/webhooks/aaa", SourceID: "aaa"}},
			Payload:   &metadata.WebhookPayloadMeta{MaxBodyBytes: 65536},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{alpha, zebra}, contracts)
	fieldIndex := idxOf(map[string]string{"alphaingest": "alphaSvc", "zebraingest": "zebraSvc"})

	spec, err := BuildCellSpec(p, "hooks", markergen.WireBundle{}, fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.WebhookReceivers) != 2 {
		t.Fatalf("WebhookReceivers len = %d, want 2", len(spec.WebhookReceivers))
	}
	// Primary key is SliceID: alphaingest before zebraingest, regardless of
	// their ContractIDs (which are in the opposite order).
	if got := spec.WebhookReceivers[0].SliceID; got != "alphaingest" {
		t.Errorf("WebhookReceivers[0].SliceID = %q, want alphaingest (sorted by SliceID)", got)
	}
	if got := spec.WebhookReceivers[1].SliceID; got != "zebraingest" {
		t.Errorf("WebhookReceivers[1].SliceID = %q, want zebraingest (sorted by SliceID)", got)
	}
}

// TestBuildWebhookDispatches_SortedBySliceIDThenContractID verifies the
// ordering invariant for dispatches (symmetric to the receivers test): specs
// are sorted by (SliceID, ContractID). The fixture puts SliceID order in the
// REVERSE of ContractID order so the assertion deterministically distinguishes
// the (SliceID, ContractID) sort from a ContractID-only sort.
func TestBuildWebhookDispatches_SortedBySliceIDThenContractID(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           "hooks",
		Dir:          "hooks",
		File:         "cells/hooks/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("HooksCell"),
	}
	// alphapush (low SliceID) dispatches the high-ContractID contract;
	// zebrapush (high SliceID) dispatches the low-ContractID contract.
	alpha := &metadata.SliceMeta{
		ID: "alphapush", BelongsToCell: "hooks", Dir: "alphapush",
		File: "cells/hooks/slices/alphapush/slice.yaml",
		ContractUsages: []metadata.ContractUsage{{
			Contract: "webhook.zzz.orders.v1", Role: "webhook-dispatch",
			TargetSelector: "ZzzTarget", SourceID: "zzz",
		}},
	}
	zebra := &metadata.SliceMeta{
		ID: "zebrapush", BelongsToCell: "hooks", Dir: "zebrapush",
		File: "cells/hooks/slices/zebrapush/slice.yaml",
		ContractUsages: []metadata.ContractUsage{{
			Contract: "webhook.aaa.orders.v1", Role: "webhook-dispatch",
			TargetSelector: "AaaTarget", SourceID: "aaa",
		}},
	}
	contracts := []*metadata.ContractMeta{
		{ID: "webhook.zzz.orders.v1", Kind: "webhook"},
		{ID: "webhook.aaa.orders.v1", Kind: "webhook"},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{alpha, zebra}, contracts)
	fieldIndex := idxOf(map[string]string{"alphapush": "alphaSvc", "zebrapush": "zebraSvc"})

	spec, err := BuildCellSpec(p, "hooks", markergen.WireBundle{}, fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.WebhookDispatches) != 2 {
		t.Fatalf("WebhookDispatches len = %d, want 2", len(spec.WebhookDispatches))
	}
	if got := spec.WebhookDispatches[0].SliceID; got != "alphapush" {
		t.Errorf("WebhookDispatches[0].SliceID = %q, want alphapush (sorted by SliceID)", got)
	}
	if got := spec.WebhookDispatches[1].SliceID; got != "zebrapush" {
		t.Errorf("WebhookDispatches[1].SliceID = %q, want zebrapush (sorted by SliceID)", got)
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

// TestBuildWebhookReceivers_MissingSignature verifies that a webhook-receive CU
// whose contract lacks a signature section is rejected at codegen time.
func TestBuildWebhookReceivers_MissingSignature(t *testing.T) {
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
			{Contract: "webhook.stripe.payment-events.v1", Role: "webhook-receive", Handler: "HandleStripeEvent", SourceID: "stripe"},
		},
	}
	// Contract with no Signature section.
	contract := &metadata.ContractMeta{
		ID:   "webhook.stripe.payment-events.v1",
		Kind: "webhook",
		Endpoints: metadata.EndpointsMeta{
			Inbound: &metadata.WebhookInboundMeta{PathPattern: "/webhooks/stripe", SourceID: "stripe"},
		},
		Payload: &metadata.WebhookPayloadMeta{MaxBodyBytes: 65536},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	fieldIndex := idxOf(map[string]string{"stripeingest": "stripeSvc"})

	_, err := BuildCellSpec(p, "hooks", markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for missing signature section, got nil")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("error should mention signature, got: %v", err)
	}
}

// TestBuildWebhookReceivers_MissingInbound verifies that a webhook-receive CU
// whose contract lacks an endpoints.inbound section is rejected at codegen time.
func TestBuildWebhookReceivers_MissingInbound(t *testing.T) {
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
			{Contract: "webhook.stripe.payment-events.v1", Role: "webhook-receive", Handler: "HandleStripeEvent", SourceID: "stripe"},
		},
	}
	// Contract with signature but no Inbound section.
	contract := &metadata.ContractMeta{
		ID:   "webhook.stripe.payment-events.v1",
		Kind: "webhook",
		Signature: &metadata.WebhookSignatureMeta{
			DeliveryIDHeader: "svix-id", TimestampHeader: "svix-timestamp", SignatureHeader: "svix-signature",
			ToleranceSeconds: 300,
		},
		Payload: &metadata.WebhookPayloadMeta{MaxBodyBytes: 65536},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	fieldIndex := idxOf(map[string]string{"stripeingest": "stripeSvc"})

	_, err := BuildCellSpec(p, "hooks", markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for missing endpoints.inbound section, got nil")
	}
	if !strings.Contains(err.Error(), "inbound") {
		t.Errorf("error should mention inbound, got: %v", err)
	}
}

// TestBuildWebhookReceivers_MissingPayload verifies that a webhook-receive CU
// whose contract lacks a payload section is rejected at codegen time.
func TestBuildWebhookReceivers_MissingPayload(t *testing.T) {
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
			{Contract: "webhook.stripe.payment-events.v1", Role: "webhook-receive", Handler: "HandleStripeEvent", SourceID: "stripe"},
		},
	}
	// Contract with signature and inbound but no Payload section.
	contract := &metadata.ContractMeta{
		ID:   "webhook.stripe.payment-events.v1",
		Kind: "webhook",
		Signature: &metadata.WebhookSignatureMeta{
			DeliveryIDHeader: "svix-id", TimestampHeader: "svix-timestamp", SignatureHeader: "svix-signature",
			ToleranceSeconds: 300,
		},
		Endpoints: metadata.EndpointsMeta{
			Inbound: &metadata.WebhookInboundMeta{PathPattern: "/webhooks/stripe", SourceID: "stripe"},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	fieldIndex := idxOf(map[string]string{"stripeingest": "stripeSvc"})

	_, err := BuildCellSpec(p, "hooks", markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for missing payload section, got nil")
	}
	if !strings.Contains(err.Error(), "payload") {
		t.Errorf("error should mention payload, got: %v", err)
	}
}

// TestBuildWebhookReceivers_ZeroToleranceSeconds verifies that a contract with
// toleranceSeconds=0 is rejected (must be positive).
func TestBuildWebhookReceivers_ZeroToleranceSeconds(t *testing.T) {
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
			{Contract: "webhook.stripe.payment-events.v1", Role: "webhook-receive", Handler: "HandleStripeEvent", SourceID: "stripe"},
		},
	}
	contract := &metadata.ContractMeta{
		ID:   "webhook.stripe.payment-events.v1",
		Kind: "webhook",
		Signature: &metadata.WebhookSignatureMeta{
			DeliveryIDHeader: "svix-id", TimestampHeader: "svix-timestamp", SignatureHeader: "svix-signature",
			ToleranceSeconds: 0, // invalid
		},
		Endpoints: metadata.EndpointsMeta{
			Inbound: &metadata.WebhookInboundMeta{PathPattern: "/webhooks/stripe", SourceID: "stripe"},
		},
		Payload: &metadata.WebhookPayloadMeta{MaxBodyBytes: 65536},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	fieldIndex := idxOf(map[string]string{"stripeingest": "stripeSvc"})

	_, err := BuildCellSpec(p, "hooks", markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for zero toleranceSeconds, got nil")
	}
	if !strings.Contains(err.Error(), "toleranceSeconds") && !strings.Contains(err.Error(), "tolerance") {
		t.Errorf("error should mention toleranceSeconds, got: %v", err)
	}
}

// TestBuildWebhookReceivers_ZeroMaxBodyBytes verifies that a contract with
// maxBodyBytes=0 is rejected (must be positive).
func TestBuildWebhookReceivers_ZeroMaxBodyBytes(t *testing.T) {
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
			{Contract: "webhook.stripe.payment-events.v1", Role: "webhook-receive", Handler: "HandleStripeEvent", SourceID: "stripe"},
		},
	}
	contract := &metadata.ContractMeta{
		ID:   "webhook.stripe.payment-events.v1",
		Kind: "webhook",
		Signature: &metadata.WebhookSignatureMeta{
			DeliveryIDHeader: "svix-id", TimestampHeader: "svix-timestamp", SignatureHeader: "svix-signature",
			ToleranceSeconds: 300,
		},
		Endpoints: metadata.EndpointsMeta{
			Inbound: &metadata.WebhookInboundMeta{PathPattern: "/webhooks/stripe", SourceID: "stripe"},
		},
		Payload: &metadata.WebhookPayloadMeta{MaxBodyBytes: 0}, // invalid
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{contract})
	fieldIndex := idxOf(map[string]string{"stripeingest": "stripeSvc"})

	_, err := BuildCellSpec(p, "hooks", markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for zero maxBodyBytes, got nil")
	}
	if !strings.Contains(err.Error(), "maxBodyBytes") && !strings.Contains(err.Error(), "body") {
		t.Errorf("error should mention maxBodyBytes, got: %v", err)
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
