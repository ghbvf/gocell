package devicecommand

import (
	"net/http"
	"strings"
	"time"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Contract specs — examples are exempt from FMT-18 scanning, but keeping the
// IDs aligned with contract.yaml makes tests and traces easier to follow.
var (
	specCommandEnqueue = wrapper.ContractSpec{
		ID: "http.device.command.enqueue.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/devices/{id}/commands",
	}
	specCommandDequeue = wrapper.ContractSpec{
		ID: "http.device.command.dequeue.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/devices/{id}/commands",
	}
	specCommandReport = wrapper.ContractSpec{
		ID: "http.device.command.report.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/devices/{id}/commands/{cmdId}/report",
	}
	specCommandAck = wrapper.ContractSpec{
		ID: "http.device.command.ack.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/devices/{id}/commands/{cmdId}/ack",
	}
	specCommandExtendLease = wrapper.ContractSpec{
		ID: "http.device.command.extend-lease.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/devices/{id}/commands/{cmdId}/extend-lease",
	}
	specInternalCommandScanActive = wrapper.ContractSpec{
		ID: "http.internal.devicecommands.scan-active.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/internal/v1/devicecommands",
	}
)

// commandResponse is the public DTO for a kernel command.Entry, isolating
// the API contract from the kernel model.
type commandResponse struct {
	ID          string     `json:"id"`
	DeviceID    string     `json:"deviceId"`
	CommandType string     `json:"commandType"`
	Payload     string     `json:"payload"`
	Status      string     `json:"status"`
	Attempt     int        `json:"attempt"`
	CreatedAt   time.Time  `json:"createdAt"`
	SentAt      *time.Time `json:"sentAt,omitempty"`
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

func toCommandResponse(e command.Entry) commandResponse {
	return commandResponse{
		ID:          e.ID,
		DeviceID:    e.DeviceID,
		CommandType: e.CommandType,
		Payload:     string(e.Payload),
		Status:      e.Status.String(),
		Attempt:     e.Attempt,
		CreatedAt:   e.CreatedAt,
		SentAt:      e.SentAt,
		DeliveredAt: e.DeliveredAt,
		CompletedAt: e.CompletedAt,
	}
}

// Handler provides HTTP endpoints for device commands.
type Handler struct {
	svc *Service
}

// NewHandler creates a device-command Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers device-command routes on the given mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) {
	auth.Mount(mux, auth.Route{
		Contract: specCommandEnqueue,
		Handler:  http.HandlerFunc(h.HandleEnqueue),
		Policy:   auth.AnyRole("admin", "operator"),
	})
	auth.Mount(mux, auth.Route{
		Contract: specCommandDequeue,
		Handler:  http.HandlerFunc(h.HandleDequeue),
		Policy:   auth.SelfOr("id", "admin"),
	})
	auth.Mount(mux, auth.Route{
		Contract: specCommandReport,
		Handler:  http.HandlerFunc(h.HandleReport),
		Policy:   auth.SelfOr("id", "admin"),
	})
	auth.Mount(mux, auth.Route{
		Contract: specCommandAck,
		Handler:  http.HandlerFunc(h.HandleAck),
		Policy:   auth.SelfOr("id", "admin"),
	})
	auth.Mount(mux, auth.Route{
		Contract: specCommandExtendLease,
		Handler:  http.HandlerFunc(h.HandleExtendLease),
		Policy:   auth.SelfOr("id", "admin"),
	})
}

// RegisterInternalRoutes registers device-command ops routes on the internal
// listener. The bootstrap internal middleware authenticates service tokens;
// the route policy then requires the built-in internal admin role.
func (h *Handler) RegisterInternalRoutes(mux kcell.RouteHandler) {
	auth.Mount(mux, auth.Route{
		Contract:  specInternalCommandScanActive,
		Handler:   http.HandlerFunc(h.HandleScanActive),
		Policy:    auth.AnyRole(auth.RoleInternalAdmin),
		Delegated: true,
	})
}

// enqueueRequest is the JSON body for POST /api/v1/devices/{id}/commands.
type enqueueRequest struct {
	CommandType string `json:"commandType,omitempty"` // optional; defaults to "default"
	Payload     string `json:"payload"`
}

// HandleEnqueue handles POST /api/v1/devices/{id}/commands.
// This is an operator/management endpoint — only admin or operator roles
// may enqueue commands. Dequeue, Report, Ack, and ExtendLease are device-facing
// (subject == deviceID) with admin bypass.
// Deferred (S43, tracked by PERMISSION-BASED-AUTHZ-01): role-name literals are migrated to permission-based authz when that backlog item lands.
func (h *Handler) HandleEnqueue(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")

	var req enqueueRequest
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	entry, err := h.svc.Enqueue(r.Context(), deviceID, req.CommandType, req.Payload)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toCommandResponse(entry)})
}

// HandleDequeue handles GET /api/v1/devices/{id}/commands?limit=N.
// Devices poll this endpoint to claim pending commands (L4 latent model).
//
// Trust boundary: subject must match deviceID (device authenticates as itself)
// or hold admin role.
// Deferred (S43, tracked by PERMISSION-BASED-AUTHZ-01): role-name literals are migrated to permission-based authz when that backlog item lands.
func (h *Handler) HandleDequeue(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")

	pageReq, ok := httputil.ParsePageParamsOrWrite(w, r)
	if !ok {
		return
	}

	entries, err := h.svc.Dequeue(r.Context(), deviceID, pageReq.Limit, command.DefaultLeaseDuration)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	data := make([]commandResponse, 0, len(entries))
	for _, entry := range entries {
		data = append(data, toCommandResponse(entry))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"data":       data,
		"nextCursor": "",
		"hasMore":    false,
	})
}

func (h *Handler) HandleReport(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	cmdID := r.PathValue("cmdId")

	if err := h.svc.Report(r.Context(), deviceID, cmdID); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	h.writeCommand(w, r, cmdID)
}

type ackRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) HandleAck(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	cmdID := r.PathValue("cmdId")

	var req ackRequest
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}
	reason, err := parseAckReason(req.Reason)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	if err := h.svc.Ack(r.Context(), deviceID, cmdID, reason); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	h.writeCommand(w, r, cmdID)
}

type extendLeaseRequest struct {
	ExtensionSeconds int `json:"extensionSeconds"`
}

func (h *Handler) HandleExtendLease(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	cmdID := r.PathValue("cmdId")

	var req extendLeaseRequest
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}
	if err := h.svc.ExtendLease(r.Context(), deviceID, cmdID, time.Duration(req.ExtensionSeconds)*time.Second); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	h.writeCommand(w, r, cmdID)
}

func (h *Handler) HandleScanActive(w http.ResponseWriter, r *http.Request) {
	statuses, err := parseStatusFilter(r.URL.Query().Get("statuses"))
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	pageReq, ok := httputil.ParsePageParamsOrWrite(w, r)
	if !ok {
		return
	}
	result, err := h.svc.ScanActive(r.Context(), command.ScanFilter{
		DeviceID: r.URL.Query().Get("deviceId"),
		Statuses: statuses,
	}, pageReq)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, query.MapPageResult(result, toCommandResponse))
}

func (h *Handler) writeCommand(w http.ResponseWriter, r *http.Request, cmdID string) {
	entry, err := h.svc.queue.GetCommand(r.Context(), cmdID)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	if entry == nil {
		httputil.WriteError(r.Context(), w, http.StatusInternalServerError,
			string(errcode.ErrInternal),
			"devicecommand: ack succeeded but entry not found")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toCommandResponse(*entry)})
}

func parseAckReason(raw string) (command.AckReason, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "success":
		return command.AckSuccess, nil
	case "failure", "failed":
		return command.AckFailed, nil
	case "rejected":
		return command.AckRejected, nil
	default:
		return 0, errcode.New(errcode.ErrValidationFailed, "devicecommand: invalid ack reason")
	}
}

func parseStatusFilter(raw string) ([]command.Status, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	statuses := make([]command.Status, 0, len(parts))
	for _, part := range parts {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "", "all":
			continue
		case "pending":
			statuses = append(statuses, command.StatusPending)
		case "sent":
			statuses = append(statuses, command.StatusSent)
		case "delivered":
			statuses = append(statuses, command.StatusDelivered)
		default:
			return nil, errcode.New(errcode.ErrValidationFailed, "devicecommand: invalid status filter")
		}
	}
	return statuses, nil
}
