package devicecommand

import (
	"net/http"
	"time"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Contract specs — examples not backed by contracts/ YAML; used solely for
// Mount registration shape.
var (
	specCommandEnqueue = wrapper.ContractSpec{
		ID: "http.iotdevice.devices.commands.enqueue.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/devices/{id}/commands",
	}
	specCommandList = wrapper.ContractSpec{
		ID: "http.iotdevice.devices.commands.list.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/devices/{id}/commands",
	}
	specCommandAck = wrapper.ContractSpec{
		ID: "http.iotdevice.devices.commands.ack.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/devices/{id}/commands/{cmdId}/ack",
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
		CompletedAt: e.CompletedAt,
	}
}

// Handler provides HTTP endpoints for device commands.
//
// WARNING: command endpoints run in demo mode — no route-level auth policy.
// For production, wire WithAuthDiscovery() on the assembly and attach
// Policy: auth.AnyRole("operator") to auth.Route.
type Handler struct {
	svc *Service
}

// NewHandler creates a device-command Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers device-command routes on the given mux. DEMO
// MODE: no route-level policy — pre-F3 devicecell had no policy wrapping.
// Deployments wanting authz wire WithAuthDiscovery() and add a Policy or
// rely on AuthMiddleware's baseline JWT check.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) {
	auth.Mount(mux, auth.Route{Contract: specCommandEnqueue, Handler: http.HandlerFunc(h.HandleEnqueue)})
	auth.Mount(mux, auth.Route{Contract: specCommandList, Handler: http.HandlerFunc(h.HandleListPending)})
	auth.Mount(mux, auth.Route{Contract: specCommandAck, Handler: http.HandlerFunc(h.HandleAck)})
}

// enqueueRequest is the JSON body for POST /api/v1/devices/{id}/commands.
type enqueueRequest struct {
	CommandType string `json:"commandType,omitempty"` // optional; defaults to "default"
	Payload     string `json:"payload"`
}

// HandleEnqueue handles POST /api/v1/devices/{id}/commands.
// This is an operator/management endpoint — only admin or operator roles
// may enqueue commands. ListPending and Ack are device-facing (subject == deviceID).
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

// HandleListPending handles GET /api/v1/devices/{id}/commands?limit=N&cursor=TOKEN.
// Devices poll this endpoint to retrieve pending commands (L4 latent model).
//
// Trust boundary: subject must match deviceID (device authenticates as itself)
// or hold admin role.
// Deferred (S43, tracked by PERMISSION-BASED-AUTHZ-01): role-name literals are migrated to permission-based authz when that backlog item lands.
func (h *Handler) HandleListPending(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")

	pageReq, ok := httputil.ParsePageParamsOrWrite(w, r)
	if !ok {
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
// Returns the full command DTO with the terminal status after acknowledgement.
//
// Trust boundary: subject must match deviceID or hold admin role.
// Deferred (S43, tracked by PERMISSION-BASED-AUTHZ-01): role-name literals are migrated to permission-based authz when that backlog item lands.
func (h *Handler) HandleAck(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	cmdID := r.PathValue("cmdId")

	if err := h.svc.Ack(r.Context(), deviceID, cmdID); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	// Fetch the updated entry to return the terminal state.
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
