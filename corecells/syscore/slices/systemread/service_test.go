package systemread

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/runtime/sysinfo"
	system "github.com/ghbvf/gocell/generated/contracts/http/admin/system/v1"
)

type fakeSystemView struct{ rep sysinfo.Report }

func (f fakeSystemView) Report(context.Context) sysinfo.Report { return f.rep }

func sampleSystemReport() sysinfo.Report {
	last := "2026-06-19T09:00:00Z"
	return sysinfo.Report{
		Build: sysinfo.BuildInfo{
			Version: "v0.3.1", Commit: "8c3a4b9", CommitDate: "2026-06-18T00:00:00Z",
			BuildDate: "2026-06-19T00:00:00Z", GoVersion: "go1.25.0", Dirty: false,
		},
		Runtime: sysinfo.RuntimeInfo{
			UptimeSeconds: 34200, Goroutines: 142,
			MemoryAllocBytes: 91750400, GCPauseTotalNs: 12450000,
		},
		Assembly:    sysinfo.AssemblyInfo{Name: "corebundle", Cells: []string{"accesscore", "syscore"}},
		Environment: sysinfo.EnvironmentInfo{Env: "qa", Containerized: true},
		Deployment:  sysinfo.DeploymentInfo{Available: true, LastDeployedAt: &last, Source: "env"},
	}
}

func TestService_System_OK(t *testing.T) {
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := sysinfo.WithSystemView(context.Background(), fakeSystemView{sampleSystemReport()})

	resp, err := svc.System(ctx, &system.Request{})
	if err != nil {
		t.Fatalf("System returned err: %v", err)
	}
	ok, isOK := resp.(system.System200JSONResponse)
	if !isOK {
		t.Fatalf("response type = %T, want System200JSONResponse", resp)
	}
	if ok.Data.Build.Version != "v0.3.1" || ok.Data.Build.Commit != "8c3a4b9" {
		t.Fatalf("build projection wrong: %+v", ok.Data.Build)
	}
	if ok.Data.Runtime.UptimeSeconds != 34200 || ok.Data.Runtime.MemoryAllocBytes != 91750400 {
		t.Fatalf("runtime projection wrong: %+v", ok.Data.Runtime)
	}
	if got := ok.Data.Assembly.Cells; len(got) != 2 || got[0] != "accesscore" || got[1] != "syscore" {
		t.Fatalf("assembly cells = %+v", got)
	}
	if ok.Data.Environment.Env != system.ResponseDataEnvironmentEnvUnknown || !ok.Data.Environment.Containerized {
		t.Fatalf("environment projection wrong: %+v", ok.Data.Environment)
	}
	if ok.Data.Deployment.LastDeployedAt == nil || *ok.Data.Deployment.LastDeployedAt != "2026-06-19T09:00:00Z" {
		t.Fatalf("deployment projection wrong: %+v", ok.Data.Deployment)
	}
	body, err := json.Marshal(ok.Data.Runtime)
	if err != nil {
		t.Fatalf("marshal runtime: %v", err)
	}
	if strings.Contains(string(body), "cpuPercent") || strings.Contains(string(body), "memoryMB") {
		t.Fatalf("runtime wire must not expose unavailable/display-only metrics: %s", body)
	}
}

func TestService_System_EmptyArraysSerializeEmpty(t *testing.T) {
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	rep := sampleSystemReport()
	rep.Assembly.Cells = nil
	resp, err := svc.System(sysinfo.WithSystemView(context.Background(), fakeSystemView{rep}), &system.Request{})
	if err != nil {
		t.Fatalf("System: %v", err)
	}
	ok, isOK := resp.(system.System200JSONResponse)
	if !isOK {
		t.Fatalf("response type = %T, want System200JSONResponse", resp)
	}
	if ok.Data.Assembly.Cells == nil {
		t.Fatal("assembly.cells must be a non-nil empty slice")
	}
	b, err := json.Marshal(ok.Data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"cells":[]`) {
		t.Fatalf("cells must serialize as []; got %s", b)
	}
}

func TestService_System_FailClosed(t *testing.T) {
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	resp, err := svc.System(context.Background(), &system.Request{})
	if err != nil {
		t.Fatalf("System returned err: %v", err)
	}
	if _, ok := resp.(system.System503ErrorResponse); !ok {
		t.Fatalf("response type = %T, want System503ErrorResponse", resp)
	}
}
