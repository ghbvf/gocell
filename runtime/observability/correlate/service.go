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
	// trace reverse-lookup. 500 intentionally matches the global list-pagination
	// cap (CLAUDE.md §"列表接口强制分页，limit 上限 500"); any increase here
	// must also relax that cap.
	traceQueryLimit = 500
)

// auditEntryDTO is the wire shape for a single audit entry in trace-mode
// responses. Only the fields needed for trace correlation are included:
// id, eventId, eventType, actorId, occurredAt, timestamp, correlationId.
// subjectId (OAuth sub, end-user PII), tenantId, sessionId, payload, hash,
// and prevHash are deliberately excluded — minimal-PII, fail-closed for a
// no-caller-cell ops endpoint.
//
// eventId (the audit Entry.EventID) is a non-PII UUID that enables
// forward-correlation to outbox entries and application logs; it is
// intentionally included to aid incident investigation.
type auditEntryDTO struct {
	ID            string    `json:"id"`
	EventID       string    `json:"eventId"`
	EventType     string    `json:"eventType"`
	ActorID       string    `json:"actorId"`
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
	// Alert is a stable label-selector string for finding alerts by cell label,
	// e.g. alertname=~".+",cell="<id>". Programmatically parseable; symmetric
	// with the Metric selector.
	Alert string `json:"alert"`
}

// TraceResult is the result type for CorrelateByTrace.
//
// HasMore signals that more than traceQueryLimit audit entries share this
// trace_id; the returned set has been truncated to traceQueryLimit. When
// HasMore is true, callers should narrow the time window or use the
// JWT-authed auditquery endpoint for full pagination. Returned holds the
// count of entries actually returned (len(AuditEntries)).
type TraceResult struct {
	TraceID      string          `json:"traceId"`
	AuditEntries []auditEntryDTO `json:"auditEntries"`
	HasMore      bool            `json:"hasMore"`
	Returned     int             `json:"returned"`
}

// CellResult is the result type for CorrelateByCell.
type CellResult struct {
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
// and returns a TraceResult. Returns KindNotFound when no entries match.
//
// The query passes Limit=traceQueryLimit so the store returns up to
// traceQueryLimit+1 rows (N+1 hasMore detection per QueryStore.Query godoc).
// When more than traceQueryLimit entries exist, the +1 sentinel is trimmed,
// TraceResult.HasMore is set to true, and TraceResult.Returned reflects the
// capped count. For production-scale deployments with very high trace entry
// counts, callers should use the full auditquery endpoint for pagination.
func (s *Service) CorrelateByTrace(ctx context.Context, traceID string) (TraceResult, error) {
	params := query.ListParams{
		Limit: traceQueryLimit,
		Sort:  ledger.QuerySort(),
	}
	entries, err := s.store.Query(ctx, ledger.AuditFilters{TraceID: traceID}, params)
	if err != nil {
		s.logger.ErrorContext(
			ctx, "correlate: query by trace",
			slog.String("trace_id", traceID),
			slog.Any("error", err),
		)
		return TraceResult{}, fmt.Errorf("correlate: query by trace: %w", err)
	}
	if len(entries) == 0 {
		return TraceResult{}, errcode.New(errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
			"no audit entries found for trace ID",
			errcode.WithDetails(errcode.PublicString("traceId", traceID)))
	}
	hasMore := len(entries) > traceQueryLimit
	if hasMore {
		entries = entries[:traceQueryLimit]
	}
	dtos := toAuditEntryDTOs(entries)
	return TraceResult{
		TraceID:      traceID,
		AuditEntries: dtos,
		HasMore:      hasMore,
		Returned:     len(dtos),
	}, nil
}

// CorrelateByCell resolves the owner of cellID from the topology and returns
// metric/alert selector hints. Returns KindNotFound when cellID is not in the
// topology map.
func (s *Service) CorrelateByCell(ctx context.Context, cellID string) (CellResult, error) {
	owner, ok := s.topo.Owner(cellID)
	if !ok {
		return CellResult{}, errcode.New(errcode.KindNotFound, errcode.ErrCellNotFound,
			"cell not found in topology",
			errcode.WithDetails(errcode.PublicString("cellId", cellID)))
	}
	return CellResult{
		Owner: ownerDTO{
			CellID: cellID,
			Team:   owner.Team,
			Role:   owner.Role,
		},
		Selectors: buildSelectors(cellID),
	}, nil
}

// buildSelectors constructs the metric and alert selector hints for a cell ID.
// Both selectors use stable, programmatically parseable label-selector strings.
func buildSelectors(cellID string) selectorsDTO {
	return selectorsDTO{
		Metric: fmt.Sprintf(`{cell=%q}`, cellID),
		Alert:  fmt.Sprintf(`alertname=~".+",cell=%q`, cellID),
	}
}

// toAuditEntryDTOs maps a slice of *ledger.Entry to []auditEntryDTO.
// Included fields: id, eventId, eventType, actorId, occurredAt, timestamp,
// correlationId. subjectId (end-user PII), tenantId, sessionId, payload,
// hash, and prevHash are deliberately excluded from the DTO.
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
		EventID:       e.EventID,
		EventType:     e.EventType,
		ActorID:       e.ActorID,
		OccurredAt:    e.OccurredAt,
		Timestamp:     e.Timestamp,
		CorrelationID: e.CorrelationID,
	}
}
