package identitymanage

import (
	"encoding/json"
	"net/http"
	"time"

	kcell "github.com/ghbvf/gocell/kernel/cell"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// UserResponse is the public DTO for User, excluding sensitive fields like
// PasswordHash.
type UserResponse struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// toUserResponse converts a domain.User to a UserResponse DTO.
func toUserResponse(u *domain.User) UserResponse {
	return UserResponse{
		ID:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		Status:    string(u.Status),
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

// Handler provides HTTP endpoints for identity management.
type Handler struct {
	svc *Service
}

// NewHandler creates an identity-manage Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers identity-manage routes on the given mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) {
	mux.Handle("POST /", http.HandlerFunc(h.handleCreate))
	mux.Handle("GET /{id}", http.HandlerFunc(h.handleGet))
	mux.Handle("PUT /{id}", http.HandlerFunc(h.handleUpdate))
	mux.Handle("PATCH /{id}", http.HandlerFunc(h.handlePatch))
	mux.Handle("DELETE /{id}", http.HandlerFunc(h.handleDelete))
	mux.Handle("POST /{id}/lock", http.HandlerFunc(h.handleLock))
	mux.Handle("POST /{id}/unlock", http.HandlerFunc(h.handleUnlock))
}

func (h *Handler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	user, err := h.svc.Create(r.Context(), CreateInput{
		Username: req.Username, Email: req.Email, Password: req.Password,
	})
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toUserResponse(user)})
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toUserResponse(user)})
}

func (h *Handler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Email string `json:"email"`
	}
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	input := UpdateInput{ID: id}
	if req.Email != "" {
		input.Email = &req.Email
	}
	user, err := h.svc.Update(r.Context(), input)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toUserResponse(user)})
}

func (h *Handler) handlePatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// JSON merge patch: only fields present in the JSON body are updated.
	var raw map[string]json.RawMessage
	if err := httputil.DecodeJSON(r, &raw); err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	input := UpdateInput{ID: id}
	if v, ok := raw["name"]; ok {
		var name string
		if err := json.Unmarshal(v, &name); err == nil {
			input.Name = &name
		}
	}
	if v, ok := raw["email"]; ok {
		var email string
		if err := json.Unmarshal(v, &email); err == nil {
			input.Email = &email
		}
	}
	if v, ok := raw["status"]; ok {
		var status string
		if err := json.Unmarshal(v, &status); err == nil {
			input.Status = &status
		}
	}

	user, err := h.svc.Update(r.Context(), input)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toUserResponse(user)})
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.Delete(r.Context(), id); err != nil {
		httputil.WriteDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleLock(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.Lock(r.Context(), id); err != nil {
		httputil.WriteDomainError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"status": "locked"}})
}

func (h *Handler) handleUnlock(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.Unlock(r.Context(), id); err != nil {
		httputil.WriteDomainError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"status": "active"}})
}
