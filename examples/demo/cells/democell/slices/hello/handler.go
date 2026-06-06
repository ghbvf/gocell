package hello

import (
	"context"

	hellov1 "github.com/ghbvf/gocell/generated/contracts/http/demo/hello/v1"
)

// Handler adapts the domain Service to the generated hellov1.Service interface
// (the typed-response-envelope strict-server pattern). The compile-time
// assertion below fails if the generated interface ever drifts from this shape.
var _ hellov1.Service = (*Handler)(nil)

// Handler bridges the generated http.demo.hello.v1 contract to the domain Service.
type Handler struct {
	svc *Service
}

// NewHandler creates a Handler wrapping the domain Service.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Hello implements hellov1.Service. The hello endpoint has no error path, so it
// always returns 200 with the fixed greeting. (The contract declares a 400 only
// because the typed error envelope requires at least one error response; this
// handler never constructs it.)
func (h *Handler) Hello(_ context.Context, _ *hellov1.Request) (hellov1.HelloResponseObject, error) {
	return hellov1.Hello200JSONResponse(hellov1.Response{
		Data: &hellov1.ResponseData{Message: h.svc.Greeting()},
	}), nil
}
