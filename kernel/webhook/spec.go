package webhook

import "github.com/ghbvf/gocell/pkg/errcode"

// ReceiverSpec is the pure-data descriptor cellgen emits into cell_gen.go to
// register an inbound webhook receiver. It carries only identifiers known at
// code-generation time — ContractID (slice.yaml contractUsages.contract),
// SourceID (slice.yaml contractUsages.sourceID, the secret-isolation key), and
// CellID (cell.yaml id, injected as a literal so the observability owner traces
// to cell metadata, mirroring reg.Subscribe's positional cellID). The runtime
// path resolves everything else (HTTP route mount, signature config, Claimer)
// from contract metadata.
//
// The reg.RegisterWebhookReceiver Registrar method and its bootstrap drain land
// in PR-3 (receiver runtime). This struct is the cellgen ↔ runtime seam that
// PR-2 stabilizes so the generated literal references a real type rather than a
// forward reference; the dispatch counterpart is [DispatchSpec].
type ReceiverSpec struct {
	ContractID string
	SourceID   string
	CellID     string
}

// Validate reports an [errcode.ErrWebhookConfigInvalid] error when any required
// field is empty. The zero value is invalid.
func (s ReceiverSpec) Validate() error {
	return validateSpecFields(s.ContractID, s.SourceID, s.CellID)
}

// DispatchSpec is the pure-data descriptor cellgen emits to register an outbound
// webhook dispatcher. Field semantics mirror [ReceiverSpec]; SourceID names the
// signing-secret source. The reg.RegisterWebhookDispatch Registrar method and
// the dispatcher runtime land in PR-5.
type DispatchSpec struct {
	ContractID string
	SourceID   string
	CellID     string
}

// Validate reports an [errcode.ErrWebhookConfigInvalid] error when any required
// field is empty. The zero value is invalid.
func (s DispatchSpec) Validate() error {
	return validateSpecFields(s.ContractID, s.SourceID, s.CellID)
}

// validateSpecFields is the shared three-field guard for [ReceiverSpec] and
// [DispatchSpec]. Messages are const literals (MESSAGE-CONST-LITERAL-01); the
// offending field is named in the message itself, no runtime data leaks.
func validateSpecFields(contractID, sourceID, cellID string) error {
	switch {
	case contractID == "":
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: spec requires a non-empty ContractID")
	case sourceID == "":
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: spec requires a non-empty SourceID")
	case cellID == "":
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: spec requires a non-empty CellID")
	default:
		return nil
	}
}
