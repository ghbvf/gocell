// Package webhook — test-only fixture helpers (white-box, same package).
// MustSourceID and MustDeliveryID are panic variants of their New* counterparts
// used exclusively in _test.go files for table-driven fixture construction.
// They must not appear in production code; the archtest
// KERNEL-MUSTCTOR-PRODUCTION-DECL-01 enforces this.
package webhook

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// MustSourceID is the panic variant of [NewSourceID] for test fixtures and
// package init where an invalid ID is a programmer error.
func MustSourceID(s string) SourceID {
	id, err := NewSourceID(s)
	if err != nil {
		panic(panicregister.Approved("webhook-source-id-invalid",
			errcode.Assertion("webhook.MustSourceID(%q): %v", s, err)))
	}
	return id
}

// MustDeliveryID is the panic variant of [NewDeliveryID] for test fixtures.
func MustDeliveryID(s string) DeliveryID {
	id, err := NewDeliveryID(s)
	if err != nil {
		panic(panicregister.Approved("webhook-delivery-id-invalid",
			errcode.Assertion("webhook.MustDeliveryID(%q): %v", s, err)))
	}
	return id
}
