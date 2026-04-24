package ctxkeys

import "context"

// ctxKey is an unexported type to prevent key collisions with other packages.
type ctxKey string

// Cell-model context keys propagated through context.Context.
const (
	CellID     ctxKey = "cell_id"
	SliceID    ctxKey = "slice_id"
	JourneyID  ctxKey = "journey_id"
	ContractID ctxKey = "contract_id"
)

// WithCellID returns a new context carrying the given cell ID.
func WithCellID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, CellID, id)
}

// CellIDFrom extracts the cell ID from ctx. The boolean reports whether the
// key was present; it can be true with an empty value, so callers that treat
// "" as invalid must check both ok and v != "".
func CellIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(CellID).(string)
	return v, ok
}

// WithSliceID returns a new context carrying the given slice ID.
func WithSliceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, SliceID, id)
}

// SliceIDFrom extracts the slice ID from ctx. The boolean reports whether the
// key was present; it can be true with an empty value, so callers that treat
// "" as invalid must check both ok and v != "".
func SliceIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(SliceID).(string)
	return v, ok
}

// WithJourneyID returns a new context carrying the given journey ID.
func WithJourneyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, JourneyID, id)
}

// JourneyIDFrom extracts the journey ID from ctx. The boolean reports whether
// the key was present; it can be true with an empty value, so callers that
// treat "" as invalid must check both ok and v != "".
func JourneyIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(JourneyID).(string)
	return v, ok
}

// WithContractID returns a new context carrying the given contract ID.
// The contract id is set by wrapper.HTTPHandler / WrapConsumer at the moment
// a request / event is accepted for processing, so downstream middleware,
// handlers, and event consumers can tag logs and metrics with the same
// identifier that the span carries in the gocell.contract.id attribute.
func WithContractID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ContractID, id)
}

// ContractIDFrom extracts the contract ID from ctx. The boolean reports
// whether the key was present; callers that treat "" as invalid must check
// both ok and v != "".
func ContractIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ContractID).(string)
	return v, ok
}
