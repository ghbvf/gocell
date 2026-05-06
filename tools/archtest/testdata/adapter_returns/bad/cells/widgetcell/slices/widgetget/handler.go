package widgetget

import (
	"context"

	get "github.com/ghbvf/gocell/generated/contracts/http/widget/get/v1"
)

// GetAdapter implements get.Service for http.widget.get.v1.
type GetAdapter struct{}

// Get implements get.Service.
// BUG: returns Get999JSONResponse which is NOT declared in contract.yaml.
func (a GetAdapter) Get(ctx context.Context, req *get.Request) (get.GetResponseObject, error) {
	// status 999 is not declared in contract.yaml responses — ceiling violation.
	return get.Get999JSONResponse{}, nil
}
