package identitymanage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Path constants — extracted so each user-resource path appears once in
// source. FMT-18 resolves const string references at scan time so the YAML
// cross-check still sees the effective path literal.
const (
	pathUsers        = "/api/v1/access/users"
	pathUserByID     = "/api/v1/access/users/{id}"
	pathUserLock     = "/api/v1/access/users/{id}/lock"
	pathUserUnlock   = "/api/v1/access/users/{id}/unlock"
	pathUserPassword = "/api/v1/access/users/{id}/password"
)

// Contract spec literals — one per route; cross-checked against
// contracts/http/auth/user/**/contract.yaml by FMT-18 governance.
var (
	specUserCreate = wrapper.ContractSpec{
		ID: "http.auth.user.create.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: pathUsers,
	}
	specUserGet = wrapper.ContractSpec{
		ID: "http.auth.user.get.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: pathUserByID,
	}
	specUserUpdate = wrapper.ContractSpec{
		ID: "http.auth.user.update.v1", Kind: "http", Transport: "http",
		Method: "PUT", Path: pathUserByID,
	}
	specUserPatch = wrapper.ContractSpec{
		ID: "http.auth.user.patch.v1", Kind: "http", Transport: "http",
		Method: "PATCH", Path: pathUserByID,
	}
	specUserDelete = wrapper.ContractSpec{
		ID: "http.auth.user.delete.v1", Kind: "http", Transport: "http",
		Method: "DELETE", Path: pathUserByID,
	}
	specUserLock = wrapper.ContractSpec{
		ID: "http.auth.user.lock.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: pathUserLock,
	}
	specUserUnlock = wrapper.ContractSpec{
		ID: "http.auth.user.unlock.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: pathUserUnlock,
	}
	specUserChangePassword = wrapper.ContractSpec{
		ID: "http.auth.user.change-password.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: pathUserPassword,
	}
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
	if u == nil {
		return UserResponse{}
	}
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

// RegisterRoutes registers identity-manage routes on the given mux via
// auth.Mount so every request emits a contract-tagged span. Policy is
// declared at registration time; handler bodies contain only business logic.
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) error {
	if err := auth.Mount(mux, auth.Route{
		Contract: specUserCreate,
		Handler:  http.HandlerFunc(h.handleCreate),
		Policy:   auth.AnyRole(domain.RoleAdmin),
	}); err != nil {
		return err
	}
	if err := auth.Mount(mux, auth.Route{
		Contract: specUserGet,
		Handler:  http.HandlerFunc(h.handleGet),
		Policy:   auth.SelfOr("id", domain.RoleAdmin),
	}); err != nil {
		return err
	}
	if err := auth.Mount(mux, auth.Route{
		Contract: specUserUpdate,
		Handler:  http.HandlerFunc(h.handleUpdate),
		Policy:   auth.SelfOr("id", domain.RoleAdmin),
	}); err != nil {
		return err
	}
	if err := auth.Mount(mux, auth.Route{
		Contract: specUserPatch,
		Handler:  http.HandlerFunc(h.handlePatch),
		Policy:   auth.SelfOr("id", domain.RoleAdmin),
	}); err != nil {
		return err
	}
	if err := auth.Mount(mux, auth.Route{
		Contract: specUserDelete,
		Handler:  http.HandlerFunc(h.handleDelete),
		Policy:   auth.AnyRole(domain.RoleAdmin),
	}); err != nil {
		return err
	}
	if err := auth.Mount(mux, auth.Route{
		Contract: specUserLock,
		Handler:  http.HandlerFunc(h.handleLock),
		Policy:   auth.AnyRole(domain.RoleAdmin),
	}); err != nil {
		return err
	}
	if err := auth.Mount(mux, auth.Route{
		Contract: specUserUnlock,
		Handler:  http.HandlerFunc(h.handleUnlock),
		Policy:   auth.AnyRole(domain.RoleAdmin),
	}); err != nil {
		return err
	}
	// POST /{id}/password: SelfOr policy + PasswordResetExempt so a user whose
	// token carries password_reset_required=true can still reach this endpoint
	// to satisfy the reset requirement. Router.FinalizeAuth aggregates this
	// declaration alongside all other Cell declarations at Bootstrap phase 5.
	if err := auth.Mount(mux, auth.Route{
		Contract:            specUserChangePassword,
		Handler:             http.HandlerFunc(h.handleChangePassword),
		Policy:              auth.SelfOr("id", domain.RoleAdmin),
		PasswordResetExempt: true,
	}); err != nil {
		return err
	}
	return nil
}

func (h *Handler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username             string `json:"username"`
		Email                string `json:"email"`
		Password             string `json:"password"`
		RequirePasswordReset bool   `json:"requirePasswordReset"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	user, err := h.svc.Create(r.Context(), CreateInput{
		Username:             req.Username,
		Email:                req.Email,
		Password:             req.Password,
		RequirePasswordReset: req.RequirePasswordReset,
	})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toUserResponse(user)})
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	if !ok {
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
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	if !ok {
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
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	if !ok {
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
	if v, ok := raw["requirePasswordReset"]; ok {
		var flag bool
		if err := json.Unmarshal(v, &flag); err != nil {
			httputil.WriteError(r.Context(), w, http.StatusBadRequest,
				string(errcode.ErrValidationFailed), fmt.Sprintf("field 'requirePasswordReset' must be a boolean: %v", err))
			return
		}
		input.RequirePasswordReset = &flag
	}

	user, err := h.svc.Update(r.Context(), input)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toUserResponse(user)})
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	// Prevent admin self-deletion — removing own account would lock out the
	// operator with no recovery path if this is the last admin.
	if p, ok := auth.FromContext(r.Context()); ok && p.Subject == id {
		httputil.WriteDomainError(r.Context(), w,
			errcode.New(errcode.ErrAuthSelfDelete, "cannot delete own account"))
		return
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleLock(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Lock(r.Context(), id); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": StatusResponse{Status: "locked"}})
}

func (h *Handler) handleUnlock(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Unlock(r.Context(), id); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": StatusResponse{Status: "active"}})
}

func (h *Handler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	pair, err := h.svc.ChangePassword(r.Context(), ChangePasswordInput{
		UserID:      id,
		OldPassword: req.OldPassword,
		NewPassword: req.NewPassword,
	})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": dto.ToTokenPairResponse(pair)})
}
