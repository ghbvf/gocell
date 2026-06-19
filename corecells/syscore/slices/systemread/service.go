// Package systemread serves the admin system metadata contract
// http.admin.system.v1 (#1861). It reads a framework-provided runtime SystemView
// from request context and never imports sibling cells.
package systemread

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/sysinfo"
	system "github.com/ghbvf/gocell/generated/contracts/http/admin/system/v1"
)

const msgSystemViewUnavailable = "runtime system view unavailable"

// Service implements the generated system.Service.
type Service struct{}

// NewService constructs the stateless systemread service.
func NewService() (*Service, error) {
	s := &Service{}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// System implements system.Service. Absence of the runtime view is fail-closed:
// a silent empty system report would mislead operators.
func (s *Service) System(ctx context.Context, _ *system.Request) (system.SystemResponseObject, error) {
	view, ok := sysinfo.SystemViewFromContext(ctx)
	if !ok {
		return system.System503ErrorResponse{
			Body: *errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, msgSystemViewUnavailable),
		}, nil
	}
	return system.System200JSONResponse{Data: toResponseData(view.Report(ctx))}, nil
}

func toResponseData(rep sysinfo.Report) *system.ResponseData {
	return &system.ResponseData{
		Build: &system.ResponseDataBuild{
			Version:    rep.Build.Version,
			Commit:     rep.Build.Commit,
			CommitDate: rep.Build.CommitDate,
			BuildDate:  rep.Build.BuildDate,
			GoVersion:  rep.Build.GoVersion,
			Dirty:      rep.Build.Dirty,
		},
		Runtime: &system.ResponseDataRuntime{
			UptimeSeconds:    rep.Runtime.UptimeSeconds,
			Goroutines:       rep.Runtime.Goroutines,
			MemoryMB:         rep.Runtime.MemoryMB,
			MemoryAllocBytes: rep.Runtime.MemoryAllocBytes,
			GcPauseTotalNs:   rep.Runtime.GCPauseTotalNs,
			CPUPercent:       rep.Runtime.CPUPercent,
		},
		Assembly: &system.ResponseDataAssembly{
			Name:  rep.Assembly.Name,
			Cells: cloneStrings(rep.Assembly.Cells),
		},
		Environment: &system.ResponseDataEnvironment{
			Env:           rep.Environment.Env,
			Containerized: rep.Environment.Containerized,
		},
		Deployment: &system.ResponseDataDeployment{
			Available:      rep.Deployment.Available,
			LastDeployedAt: rep.Deployment.LastDeployedAt,
			Source:         rep.Deployment.Source,
		},
	}
}

func cloneStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// Handler wires the generated system.Handler with the contract-derived
// system:read PDP gate.
type Handler struct {
	h *system.Handler
}

// NewHandler builds the route handler with the cell-level permission resolver.
func NewHandler(svc *Service, resolver authz.MethodPolicyResolver) *Handler {
	return &Handler{h: system.NewHandler(svc, resolver)}
}

// RegisterRoutes mounts the contract on mux via the generated handler.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	return h.h.RegisterRoutes(mux)
}
