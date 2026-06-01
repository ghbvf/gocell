// Package correlate implements the ops/tooling reverse-lookup service for
// issue #1048. It provides two lookup modes:
//
//   - trace mode: query the audit ledger for entries with a given trace_id
//   - cell mode: resolve the owning team/role for a cell ID from the topology
//     and emit metric/alert selector pointers
//
// This is a runtime framework service (not a cell). It lives in runtime/
// and is wired by bootstrap via WithCorrelateRoutes.
//
// ref: kubernetes/apiserver pkg/endpoints/handlers — framework-internal
// handler pattern; dependency injection without cell.Registrar.
package correlate

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/correlation"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

const (
	// traceQueryLimit is the upper bound on entries returned for a single
	// trace reverse-lookup. A trace correlating more than this count is
	// unusual enough that ops should use the full audit query endpoint.
	traceQueryLimit = 500
)

// auditEntryDTO is the wire shape for a single audit entry in trace-mode
// responses. Sensitive fields (SessionID, Payload, TenantID) are deliberately
// omitted — ops use this for correlation lookup, not full audit trail access.
type auditEntryDTO struct {
	ID            string    `json:"id"`
	EventType     string    `json:"eventType"`
	ActorID       string    `json:"actorId"`
	SubjectID     string    `json:"subjectId"`
	OccurredAt    time.Time `json:"occurredAt"`
	Timestamp     time.Time `json:"timestamp"`
	CorrelationID string    `json:"correlationId"`
}

// ownerDTO carries the cell owner metadata returned for cell-mode lookups.
type ownerDTO struct {
	CellID string `json:"cellId"`
	Team   string `json:"team"`
	Role   string `json:"role"`
}

// selectorsDTO carries metric/alert selector pointers for cell-mode lookups.
type selectorsDTO struct {
	// Metric is a PromQL label-selector hint: {cell="<id>"}
	Metric string `json:"metric"`
	// Alert is a short human-readable hint for finding alerts by cell label.
	Alert string `json:"alert"`
}

// traceResult is the result type for correlateByTrace.
type traceResult struct {
	TraceID      string          `json:"traceId"`
	AuditEntries []auditEntryDTO `json:"auditEntries"`
}

// cellResult is the result type for correlateByCell.
type cellResult struct {
	Owner     ownerDTO     `json:"owner"`
	Selectors selectorsDTO `json:"selectors"`
}

// Service is the correlate reverse-lookup service. It is a runtime framework
// service, not a cell service — it does NOT use gocell:"required" struct tags
// or validateRequired(). Nil-guarding is done explicitly in NewService to
// match the health-handler / devtools-handler convention in runtime/.
type Service struct {
	store  ledger.QueryStore
	topo   correlation.Topology
	logger *slog.Logger
}

// NewService constructs a Service. store must be non-nil (nil-guarded via
// pkg/validation.IsNilInterface). topo may be nil or empty (an empty topology
// returns "not found" for every cell lookup, which is the correct fail-closed
// behavior). logger may be nil; when nil slog.Default() is used.
//
// This is NOT a cell service — do not add gocell:"required" struct tags or
// validateRequired() here. Follow the health-handler / devtools-handler
// explicit-guard convention instead.
func NewService(store ledger.QueryStore, topo correlation.Topology, logger *slog.Logger) (*Service, error) {
	if validation.IsNilInterface(store) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"correlate: store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, topo: topo, logger: logger}, nil
}

// CorrelateByTrace queries the audit ledger for entries with the given traceID
// and returns a traceResult. Returns KindNotFound when no entries match.
//
// The query uses a fixed ListParams with traceQueryLimit rows, sorted by the
// canonical ledger order (timestamp DESC, id ASC). For production-scale
// deployments with very high trace entry counts, callers should use the full
// auditquery endpoint.
func (s *Service) CorrelateByTrace(ctx context.Context, traceID string) (traceResult, error) {
	params := query.ListParams{
		Limit: traceQueryLimit,
		Sort:  ledger.QuerySort(),
	}
	entries, err := s.store.Query(ctx, ledger.AuditFilters{TraceID: traceID}, params)
	if err != nil {
		return traceResult{}, fmt.Errorf("correlate: query by trace: %w", err)
	}
	if len(entries) == 0 {
		return traceResult{}, errcode.New(errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
			"no audit entries found for trace ID",
			errcode.WithDetails(errcode.PublicString("traceId", traceID)))
	}
	return traceResult{
		TraceID:      traceID,
		AuditEntries: toAuditEntryDTOs(entries),
	}, nil
}

// CorrelateByCell resolves the owner of cellID from the topology and returns
// metric/alert selector hints. Returns KindNotFound when cellID is not in the
// topology map.
func (s *Service) CorrelateByCell(ctx context.Context, cellID string) (cellResult, error) {
	owner, ok := s.topo.Owner(cellID)
	if !ok {
		return cellResult{}, errcode.New(errcode.KindNotFound, errcode.ErrCellNotFound,
			"cell not found in topology",
			errcode.WithDetails(errcode.PublicString("cellId", cellID)))
	}
	return cellResult{
		Owner: ownerDTO{
			CellID: cellID,
			Team:   owner.Team,
			Role:   owner.Role,
		},
		Selectors: buildSelectors(cellID),
	}, nil
}

// buildSelectors constructs the metric and alert selector hints for a cell ID.
// The metric selector is a PromQL label-selector string; the alert selector is
// a human-readable hint referencing the cell label convention documented in
// .claude/rules/gocell/observability.md §HTTP Metrics cell Label.
func buildSelectors(cellID string) selectorsDTO {
	return selectorsDTO{
		Metric: fmt.Sprintf(`{cell=%q}`, cellID),
		Alert:  fmt.Sprintf("filter alerts by label cell=%q (see docs/ops/alerting-rules.md)", cellID),
	}
}

// toAuditEntryDTOs maps a slice of *ledger.Entry to []auditEntryDTO.
// Sensitive fields (SessionID, Payload, TenantID, SeqNo, Hash, PrevHash,
// EventID) are deliberately excluded from the DTO.
func toAuditEntryDTOs(entries []*ledger.Entry) []auditEntryDTO {
	out := make([]auditEntryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, toAuditEntryDTO(e))
	}
	return out
}

func toAuditEntryDTO(e *ledger.Entry) auditEntryDTO {
	return auditEntryDTO{
		ID:            e.ID,
		EventType:     e.EventType,
		ActorID:       e.ActorID,
		SubjectID:     e.SubjectID,
		OccurredAt:    e.OccurredAt,
		Timestamp:     e.Timestamp,
		CorrelationID: e.CorrelationID,
	}
}
