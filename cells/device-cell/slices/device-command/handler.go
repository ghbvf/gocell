package devicecommand

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
)

// CommandResponse is the public DTO for Command, isolating the API contract
// from the domain model.
type CommandResponse struct {
	ID        string     `json:"id"`
	DeviceID  string     `json:"deviceId"`
	Payload   string     `json:"payload"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
	AckedAt   *time.Time `json:"ackedAt,omitempty"`
}

func toCommandResponse(c *domain.Command) CommandResponse {
	return CommandResponse{
		ID: c.ID, DeviceID: c.DeviceID, Payload: c.Payload,
		Status: c.Status, CreatedAt: c.CreatedAt, AckedAt: c.AckedAt,
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

// enqueueRequest is the JSON body for POST /api/v1/devices/{id}/commands.
type enqueueRequest struct {
	Payload string `json:"payload"`
}

// HandleEnqueue handles POST /api/v1/devices/{id}/commands.
func (h *Handler) HandleEnqueue(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")

	var req enqueueRequest
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	cmd, err := h.svc.Enqueue(r.Context(), deviceID, req.Payload)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toCommandResponse(cmd)})
}

// HandleListPending handles GET /api/v1/devices/{id}/commands?limit=N&cursor=TOKEN.
// Devices poll this endpoint to retrieve pending commands (L4 latent model).
func (h *Handler) HandleListPending(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")

	pageReq, err := httputil.ParsePageRequest(r)
	if err != nil {
		slog.Warn("pagination: request validation failed",
			slog.String("error", err.Error()),
			slog.String("path", r.URL.Path),
		)
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	result, err := h.svc.ListPending(r.Context(), deviceID, pageReq)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, query.MapPageResult(result, toCommandResponse))
}

// HandleAck handles POST /api/v1/devices/{id}/commands/{cmdId}/ack.
// Returns a status-only response (not a full CommandResponse) because
// Ack is a fire-and-forget action — the service does not return the
// updated entity.
func (h *Handler) HandleAck(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	cmdID := r.PathValue("cmdId")

	if err := h.svc.Ack(r.Context(), deviceID, cmdID); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"status": "acked",
		},
	})
}
