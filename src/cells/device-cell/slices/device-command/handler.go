package devicecommand

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

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

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"id":       cmd.ID,
			"deviceId": cmd.DeviceID,
			"payload":  cmd.Payload,
			"status":   cmd.Status,
		},
	})
}

// HandleListPending handles GET /api/v1/devices/{id}/commands.
// Devices poll this endpoint to retrieve pending commands (L4 latent model).
func (h *Handler) HandleListPending(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")

	cmds, err := h.svc.ListPending(r.Context(), deviceID)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"data":  cmds,
		"total": len(cmds),
	})
}

// HandleAck handles POST /api/v1/devices/{id}/commands/{cmdId}/ack.
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
