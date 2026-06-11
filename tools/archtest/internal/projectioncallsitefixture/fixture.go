//go:build archtest_fixture

// Package projectioncallsitefixture is the build-tagged RED/GREEN fixture for
// the RESOURCE-PROJECTION-CALLSITE-LOCK-01 archtest reverse self-check. It is
// loaded only under the archtest_fixture build tag (the literal must agree with
// the unexported fixtureBuildTag const in tools/archtest/fixture.go; Go build
// directives cannot reference a Go const). The tag excludes this package from
// `go build ./...` / `go test ./...`, so it never pollutes real-repo scans.
//
// # Cases covered
//
// The callsite lock proves that a contract marked responseProjection has its
// generated Response.Data field typed as projection.ResourceProjection (single
// object) or []projection.ResourceProjection (array) — the masking-funnel
// carrier — and FLAGS any Response.Data re-pointed at a plain DTO type (a
// template regression that would re-open un-masked construction at the handler
// callsite). The fixture mirrors the generated Response shape:
//
// GREEN (MUST NOT be flagged) — Data IS the sealed carrier:
//   - GoodScalarResponse — Data projection.ResourceProjection.
//   - GoodSliceResponse  — Data []projection.ResourceProjection.
//
// RED (MUST be flagged) — Data is a plain DTO, NOT the carrier:
//   - BadResponse        — Data []*BadResponseDataItem (a regression re-pointing
//     the field at the un-masked item slice that contractgen used to emit before
//     the projection rewrite).
//
// The shared detector responseDataFieldDiag (resource_projection_callsite_lock_test.go)
// is run over this fixture: it must emit exactly one diagnostic (BadResponse) and
// none for the two Good* controls.
package projectioncallsitefixture

import "github.com/ghbvf/gocell/pkg/projection"

// GoodScalarResponse is a GREEN control: Data is the sealed single-resource
// carrier, exactly as contractgen emits for a `data:object` responseProjection
// contract.
type GoodScalarResponse struct {
	Data projection.ResourceProjection `json:"data"`
}

// GoodSliceResponse is a GREEN control: Data is the sealed list carrier, exactly
// as contractgen emits for a `data[]:object` responseProjection contract.
type GoodSliceResponse struct {
	Data []projection.ResourceProjection `json:"data"`
}

// BadResponseDataItem is a plain un-masked DTO — the item type contractgen would
// emit WITHOUT the projection rewrite.
type BadResponseDataItem struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

// BadResponse is the RED case: a regression that re-points Response.Data at the
// plain item slice, bypassing the sealed masking carrier. The detector MUST flag
// it.
type BadResponse struct {
	Data []*BadResponseDataItem `json:"data"`
}
