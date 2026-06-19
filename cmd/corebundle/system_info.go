package main

import (
	"os"
	"strings"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
	"github.com/ghbvf/gocell/framework/runtime/sysinfo"
)

var (
	buildVersion    = "dev"
	buildCommit     = ""
	buildCommitDate = ""
	buildDate       = ""
)

func systemInfoOption(shared *composition.SharedDeps, asm *assembly.CoreAssembly) bootstrap.Option {
	return bootstrap.WithSystemInfo(systemInfoConfig(shared, asm))
}

func systemInfoConfig(shared *composition.SharedDeps, asm *assembly.CoreAssembly) sysinfo.Config {
	cfg := sysinfo.Config{
		Build: sysinfo.BuildInfoFromRuntime(buildVersion, buildCommit, buildCommitDate, buildDate),
		Environment: sysinfo.EnvironmentInfo{
			Env:           os.Getenv("GOCELL_ENV"),
			Containerized: isContainerized(),
		},
		Deployment: deploymentInfoFromEnv(),
	}
	if shared != nil && shared.Clock != nil {
		cfg.StartTime = shared.Clock.Now()
	}
	if asm != nil {
		cfg.Assembly = sysinfo.AssemblyInfo{Name: asm.ID(), Cells: asm.CellIDs()}
	}
	return cfg
}

func deploymentInfoFromEnv() sysinfo.DeploymentInfo {
	deployedAt := strings.TrimSpace(os.Getenv("GOCELL_DEPLOYED_AT"))
	if deployedAt == "" {
		return sysinfo.DeploymentInfo{Available: false, Source: "unavailable"}
	}
	parsed, err := time.Parse(time.RFC3339Nano, deployedAt)
	if err != nil {
		return sysinfo.DeploymentInfo{Available: false, Source: "invalid:GOCELL_DEPLOYED_AT"}
	}
	normalized := parsed.UTC().Format(time.RFC3339Nano)
	return sysinfo.DeploymentInfo{
		Available:      true,
		LastDeployedAt: &normalized,
		Source:         "env:GOCELL_DEPLOYED_AT",
	}
}

func isContainerized() bool {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}
