package identitymanage

import (
	"context"
	"errors"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	changepassgen "github.com/ghbvf/gocell/generated/contracts/http/auth/user/change-password/v1"
	creategen "github.com/ghbvf/gocell/generated/contracts/http/auth/user/create/v1"
	deletegen "github.com/ghbvf/gocell/generated/contracts/http/auth/user/delete/v1"
	getgen "github.com/ghbvf/gocell/generated/contracts/http/auth/user/get/v1"
	lockgen "github.com/ghbvf/gocell/generated/contracts/http/auth/user/lock/v1"
	patchgen "github.com/ghbvf/gocell/generated/contracts/http/auth/user/patch/v1"
	unlockgen "github.com/ghbvf/gocell/generated/contracts/http/auth/user/unlock/v1"
	updategen "github.com/ghbvf/gocell/generated/contracts/http/auth/user/update/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// toUserResponseData converts a domain.User to the shared user DTO shape.
// The generated contracts all use the same response field names; this helper
// avoids repetition across 5 adapters.
func toUserResponseData(u *domain.User) (id, username, email, status, createdAt, updatedAt string) {
	if u == nil {
		return
	}
	return u.ID, u.Username, u.Email, string(u.Status),
		u.CreatedAt.UTC().Format(time.RFC3339),
		u.UpdatedAt.UTC().Format(time.RFC3339)
}

// toTokenPairResponseData converts an internal TokenPair to the change-password
// contract response DTO.
func toTokenPairResponseData(p dto.TokenPair) *changepassgen.ResponseData {
	return &changepassgen.ResponseData{
		AccessToken:           p.AccessToken,
		RefreshToken:          p.RefreshToken,
		ExpiresAt:             p.ExpiresAt.UTC().Format(time.RFC3339),
		SessionId:             p.SessionID,
		UserId:                p.UserID,
		PasswordResetRequired: p.PasswordResetRequired,
	}
}

// strPtr is a nil-safe helper: returns nil for empty string (treat as "not provided"
// in PATCH semantics), non-nil for non-empty strings.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// CreateAdapter implements creategen.Service for http.auth.user.create.v1.
type CreateAdapter struct{ S *Service }

func (a CreateAdapter) Create(ctx context.Context, req *creategen.Request) (creategen.CreateResponseObject, error) {
	var requireReset bool
	if req.RequirePasswordReset != nil {
		requireReset = *req.RequirePasswordReset
	}
	user, err := a.S.Create(ctx, CreateInput{
		Username:             req.Username,
		Email:                req.Email,
		Password:             req.Password,
		RequirePasswordReset: requireReset,
	})
	if err != nil {
		return nil, err
	}
	id, username, email, status, createdAt, updatedAt := toUserResponseData(user)
	return creategen.Create201JSONResponse{Data: &creategen.ResponseData{
		ID:        id,
		Username:  username,
		Email:     email,
		Status:    status,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}}, nil
}

// GetAdapter implements getgen.Service for http.auth.user.get.v1.
type GetAdapter struct{ S *Service }

func (a GetAdapter) Get(ctx context.Context, req *getgen.Request) (getgen.GetResponseObject, error) {
	user, err := a.S.GetByID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	id, username, email, status, createdAt, updatedAt := toUserResponseData(user)
	return getgen.Get200JSONResponse{Data: &getgen.ResponseData{
		ID:        id,
		Username:  username,
		Email:     email,
		Status:    status,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}}, nil
}

// UpdateAdapter implements updategen.Service for http.auth.user.update.v1.
type UpdateAdapter struct{ S *Service }

func (a UpdateAdapter) Update(ctx context.Context, req *updategen.Request) (updategen.UpdateResponseObject, error) {
	user, err := a.S.Update(ctx, UpdateInput{
		ID:    req.ID,
		Email: strPtr(req.Email),
	})
	if err != nil {
		return nil, err
	}
	id, username, email, status, createdAt, updatedAt := toUserResponseData(user)
	return updategen.Update200JSONResponse{Data: &updategen.ResponseData{
		ID:        id,
		Username:  username,
		Email:     email,
		Status:    status,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}}, nil
}

// PatchAdapter implements patchgen.Service for http.auth.user.patch.v1.
//
// PATCH semantics: nil = absent (no change), &true = set, &false = clear.
// The generated Request.RequirePasswordReset is *bool so the handler can
// distinguish an explicit false (clear the flag) from an absent field (no change).
type PatchAdapter struct{ S *Service }

func (a PatchAdapter) Patch(ctx context.Context, req *patchgen.Request) (patchgen.PatchResponseObject, error) {
	user, err := a.S.Update(ctx, UpdateInput{
		ID:                   req.ID,
		Name:                 strPtr(req.Name),
		Email:                strPtr(req.Email),
		Status:               strPtr(req.Status),
		RequirePasswordReset: req.RequirePasswordReset, // *bool: nil=absent, &false=clear, &true=set
	})
	if err != nil {
		return nil, err
	}
	id, username, email, status, createdAt, updatedAt := toUserResponseData(user)
	return patchgen.Patch200JSONResponse{Data: &patchgen.ResponseData{
		ID:        id,
		Username:  username,
		Email:     email,
		Status:    status,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}}, nil
}

// DeleteAdapter implements deletegen.Service for http.auth.user.delete.v1.
// Prevents admin self-deletion using the caller principal from context.
type DeleteAdapter struct{ S *Service }

func (a DeleteAdapter) Delete(ctx context.Context, req *deletegen.Request) (deletegen.DeleteResponseObject, error) {
	// Prevent admin self-deletion — removing own account would lock out the
	// operator with no recovery path if this is the last admin.
	if p, ok := auth.FromContext(ctx); ok && p.Subject == req.ID {
		return nil, errcode.New(errcode.KindConflict, errcode.ErrAuthSelfDelete, "cannot delete own account")
	}
	if err := a.S.Delete(ctx, req.ID); err != nil {
		return nil, err
	}
	return deletegen.Delete204NoContentResponse{}, nil
}

// LockAdapter implements lockgen.Service for http.auth.user.lock.v1.
type LockAdapter struct{ S *Service }

func (a LockAdapter) Lock(ctx context.Context, req *lockgen.Request) (lockgen.LockResponseObject, error) {
	if err := a.S.Lock(ctx, req.ID); err != nil {
		return nil, err
	}
	return lockgen.Lock200JSONResponse{Data: &lockgen.ResponseData{Status: "locked"}}, nil
}

// UnlockAdapter implements unlockgen.Service for http.auth.user.unlock.v1.
type UnlockAdapter struct{ S *Service }

func (a UnlockAdapter) Unlock(ctx context.Context, req *unlockgen.Request) (unlockgen.UnlockResponseObject, error) {
	if err := a.S.Unlock(ctx, req.ID); err != nil {
		return nil, err
	}
	return unlockgen.Unlock200JSONResponse{Data: &unlockgen.ResponseData{Status: "active"}}, nil
}

// ChangePasswordAdapter implements changepassgen.Service for http.auth.user.change-password.v1.
//
// Route-level PasswordResetExempt: the generated handler emits
// auth.Route{PasswordResetExempt: true} so a user whose token carries
// password_reset_required=true can still reach this endpoint.
type ChangePasswordAdapter struct{ S *Service }

func (a ChangePasswordAdapter) ChangePassword(
	ctx context.Context, req *changepassgen.Request,
) (changepassgen.ChangePasswordResponseObject, error) {
	pair, err := a.S.ChangePassword(ctx, ChangePasswordInput{
		UserID:      req.ID,
		OldPassword: req.OldPassword,
		NewPassword: req.NewPassword,
	})
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) && ce.Code == errcode.ErrVersionConflict {
			return changepassgen.ChangePassword409ErrorResponse{Body: *ce}, nil
		}
		return nil, err
	}
	return changepassgen.ChangePassword200JSONResponse{Data: toTokenPairResponseData(pair)}, nil
}

// Handler is the composite route handler for the identitymanage slice.
// It wires 8 generated contract handlers (create/get/update/patch/delete/lock/unlock/change-password).
type Handler struct {
	createH         *creategen.Handler
	getH            *getgen.Handler
	updateH         *updategen.Handler
	patchH          *patchgen.Handler
	deleteH         *deletegen.Handler
	lockH           *lockgen.Handler
	unlockH         *unlockgen.Handler
	changePasswordH *changepassgen.Handler
}

// NewHandler creates an identity-manage Handler wiring all 8 contract handlers.
// Policy is declared at registration time; handler bodies contain only DTO conversion.
func NewHandler(svc *Service) *Handler {
	adminPolicy := auth.AnyRole(auth.RoleAdmin)
	selfOrAdminPolicy := auth.SelfOr("id", auth.RoleAdmin)
	return &Handler{
		createH:         creategen.NewHandler(CreateAdapter{svc}, adminPolicy),
		getH:            getgen.NewHandler(GetAdapter{svc}, selfOrAdminPolicy),
		updateH:         updategen.NewHandler(UpdateAdapter{svc}, selfOrAdminPolicy),
		patchH:          patchgen.NewHandler(PatchAdapter{svc}, selfOrAdminPolicy),
		deleteH:         deletegen.NewHandler(DeleteAdapter{svc}, adminPolicy),
		lockH:           lockgen.NewHandler(LockAdapter{svc}, adminPolicy),
		unlockH:         unlockgen.NewHandler(UnlockAdapter{svc}, adminPolicy),
		changePasswordH: changepassgen.NewHandler(ChangePasswordAdapter{svc}, selfOrAdminPolicy),
	}
}

// RegisterRoutes mounts all identity-manage contract handlers on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.createH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.getH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.updateH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.patchH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.deleteH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.lockH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.unlockH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.changePasswordH.RegisterRoutes(mux)
}
