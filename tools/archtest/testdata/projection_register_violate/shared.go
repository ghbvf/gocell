// Package projection_register_violate is a synthetic fixture for the
// PROJECTION-REGISTER-FUNNEL-01 archtest rule. It contains reg.RegisterProjection
// callsites in: the sanctioned cell_gen.go (banner → allowed), a sibling
// generated healthz_gen.go (banner, wrong basename → must fire), and a rogue
// non-generated file (→ must fire).
//
// DO NOT use this package in production code.
package projection_register_violate

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/contractspec"
)

// fixtureRequest builds a valid ProjectionRequest for the fixture callsites.
func fixtureRequest() cell.ProjectionRequest {
	return cell.ProjectionRequest{
		Spec: contractspec.ContractSpec{
			ID:        "event.test.v1",
			Kind:      "event",
			Transport: "amqp",
			Topic:     "test.v1",
		},
		ProjectionID: "p1",
		CellID:       "c1",
		Apply:        func(_ context.Context, _ cellvocab.ProjectionEvent) error { return nil },
	}
}
