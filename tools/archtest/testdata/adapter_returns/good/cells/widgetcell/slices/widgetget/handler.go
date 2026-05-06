package widgetget

import (
	"context"
	"errors"

	get "github.com/ghbvf/gocell/generated/contracts/http/widget/get/v1"
)

// GetAdapter implements get.Service for http.widget.get.v1.
type GetAdapter struct{}

// Get implements get.Service.
// Returns only status codes declared in contract.yaml (200, 404).
func (a GetAdapter) Get(ctx context.Context, req *get.Request) (get.GetResponseObject, error) {
	if req.ID == "" {
		return get.Get404ErrorResponse{Message: "not found"}, nil
	}
	if req.ID == "error" {
		// Framework fallback path — undeclared infrastructure 5xx.
		return nil, errors.New("infrastructure fault")
	}
	return get.Get200JSONResponse{ID: req.ID, Name: "widget"}, nil
}
