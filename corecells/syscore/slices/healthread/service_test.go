package healthread

import (
	"context"
	"testing"

	cells "github.com/ghbvf/gocell/generated/contracts/http/admin/health/cells/v1"
	"github.com/ghbvf/gocell/runtime/syshealth"
)

// fakeView is a test HealthView returning a canned Report. Shared across the
// healthread test files (same package).
type fakeView struct{ rep syshealth.Report }

func (f fakeView) Report(context.Context) syshealth.Report { return f.rep }

// sampleReport is a representative aggregate used by the slice tests.
func sampleReport() syshealth.Report {
	return syshealth.Report{
		Overall: "degraded",
		Cells: []syshealth.CellHealth{
			{
				ID: "accesscore", Live: true, Ready: true, Status: "healthy",
				Deps: []syshealth.ProbeHealth{{Name: "accesscore_repo_ready", Status: "healthy", DurationMs: 2}},
			},
			{
				ID: "configcore", Live: true, Ready: false, Status: "degraded",
				Deps: []syshealth.ProbeHealth{{Name: "configcore_repo_ready", Status: "degraded", DurationMs: 5}},
			},
		},
		Adapters: []syshealth.ProbeHealth{
			{Name: "postgres_ready", Status: "healthy", DurationMs: 1},
		},
	}
}

// TestService_Cells_OK pins the projection: a HealthView in context ⇒ 200 with
// the report mapped onto the wire DTO (overall + per-cell live/ready/status/deps
// + assembly-level adapters).
func TestService_Cells_OK(t *testing.T) {
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := syshealth.WithHealthView(context.Background(), fakeView{sampleReport()})

	resp, err := svc.Cells(ctx, &cells.Request{})
	if err != nil {
		t.Fatalf("Cells returned err: %v", err)
	}
	ok, isOK := resp.(cells.Cells200JSONResponse)
	if !isOK {
		t.Fatalf("response type = %T, want Cells200JSONResponse", resp)
	}
	d := ok.Data
	if d.Overall != "degraded" {
		t.Errorf("Overall = %q, want degraded", d.Overall)
	}
	if len(d.Cells) != 2 {
		t.Fatalf("Cells len = %d, want 2", len(d.Cells))
	}
	if d.Cells[0].ID != "accesscore" || !d.Cells[0].Live || !d.Cells[0].Ready || d.Cells[0].Status != "healthy" {
		t.Errorf("cell[0] projection wrong: %+v", d.Cells[0])
	}
	if len(d.Cells[0].Deps) != 1 || d.Cells[0].Deps[0].Name != "accesscore_repo_ready" || d.Cells[0].Deps[0].DurationMs != 2 {
		t.Errorf("cell[0] deps wrong: %+v", d.Cells[0].Deps)
	}
	if d.Cells[1].ID != "configcore" || d.Cells[1].Ready || d.Cells[1].Status != "degraded" {
		t.Errorf("cell[1] projection wrong: %+v", d.Cells[1])
	}
	if len(d.Adapters) != 1 || d.Adapters[0].Name != "postgres_ready" || d.Adapters[0].Status != "healthy" {
		t.Errorf("adapters projection wrong: %+v", d.Adapters)
	}
}

// TestService_Cells_FailClosed pins the fail-closed contract: NO HealthView in
// context ⇒ typed 503, never a silent empty 200 (an empty cells array would read
// as "all cells gone").
func TestService_Cells_FailClosed(t *testing.T) {
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	resp, err := svc.Cells(context.Background(), &cells.Request{})
	if err != nil {
		t.Fatalf("Cells returned err: %v", err)
	}
	if _, ok := resp.(cells.Cells503ErrorResponse); !ok {
		t.Fatalf("response type = %T, want Cells503ErrorResponse (fail-closed)", resp)
	}
}
