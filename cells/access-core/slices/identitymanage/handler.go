package identitymanage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	kcell "github.com/ghbvf/gocell/kernel/cell"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// StatusResponse is a single-field DTO for lock/unlock responses.
type StatusResponse struct {
	Status string `json:"status"`
}

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
	if err := auth.RequireAnyRole(r.Context(), domain.RoleAdmin); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	user, err := h.svc.Create(r.Context(), CreateInput{
		Username: req.Username, Email: req.Email, Password: req.Password,
	})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toUserResponse(user)})
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := auth.RequireSelfOrRole(r.Context(), id, domain.RoleAdmin); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	user, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toUserResponse(user)})
}

func (h *Handler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := auth.RequireSelfOrRole(r.Context(), id, domain.RoleAdmin); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	input := UpdateInput{ID: id}
	if req.Email != "" {
		input.Email = &req.Email
	}
	user, err := h.svc.Update(r.Context(), input)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toUserResponse(user)})
}

func (h *Handler) handlePatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := auth.RequireSelfOrRole(r.Context(), id, domain.RoleAdmin); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	// JSON merge patch: only fields present in the JSON body are updated.
	// Patchable fields: name, email, status. Other fields are silently ignored.
	// Uses DecodeJSON (not strict) because map targets accept any key by design.
	var raw map[string]json.RawMessage
	if err := httputil.DecodeJSON(r, &raw); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	input := UpdateInput{ID: id}
	if v, ok := raw["name"]; ok {
		var name string
		if err := json.Unmarshal(v, &name); err != nil {
			httputil.WriteError(r.Context(), w, http.StatusBadRequest,
				string(errcode.ErrValidationFailed), fmt.Sprintf("field 'name' must be a string: %v", err))
			return
		}
		input.Name = &name
	}
	if v, ok := raw["email"]; ok {
		var email string
		if err := json.Unmarshal(v, &email); err != nil {
			httputil.WriteError(r.Context(), w, http.StatusBadRequest,
				string(errcode.ErrValidationFailed), fmt.Sprintf("field 'email' must be a string: %v", err))
			return
		}
		input.Email = &email
	}
	if v, ok := raw["status"]; ok {
		var status string
		if err := json.Unmarshal(v, &status); err != nil {
			httputil.WriteError(r.Context(), w, http.StatusBadRequest,
				string(errcode.ErrValidationFailed), fmt.Sprintf("field 'status' must be a string: %v", err))
			return
		}
		input.Status = &status
	}

	user, err := h.svc.Update(r.Context(), input)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toUserResponse(user)})
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := auth.RequireAnyRole(r.Context(), domain.RoleAdmin); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	id := r.PathValue("id")

	// Prevent admin self-deletion — removing own account would lock out the
	// operator with no recovery path if this is the last admin.
	if subject, ok := ctxkeys.SubjectFrom(r.Context()); ok && subject == id {
		httputil.WriteJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]any{
				"code":    "ERR_AUTH_SELF_DELETE",
				"message": "cannot delete own account",
			},
		})
		return
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleLock(w http.ResponseWriter, r *http.Request) {
	if err := auth.RequireAnyRole(r.Context(), domain.RoleAdmin); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	id := r.PathValue("id")
	if err := h.svc.Lock(r.Context(), id); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": StatusResponse{Status: "locked"}})
}

func (h *Handler) handleUnlock(w http.ResponseWriter, r *http.Request) {
	if err := auth.RequireAnyRole(r.Context(), domain.RoleAdmin); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	id := r.PathValue("id")
	if err := h.svc.Unlock(r.Context(), id); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": StatusResponse{Status: "active"}})
}
