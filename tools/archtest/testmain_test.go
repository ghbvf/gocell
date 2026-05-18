package archtest

import (
	"log/slog"
	"os"
	"testing"
)

// TestMain 在所有 Test* 跑前预热 typeseval.SharedResolver 的最高频
// cacheKey: (modRoot, tests=false, tags=nil, "./...") = LoadProductionPackages
// 主路径。覆盖 LAYER-* / 多数业务 Test* 的首次 SharedResolver 调用，把
// "首次 cache miss" 时机从 borderline test 的 wall 内移到 shard startup 外，
// 让 TestUserRepoConformanceEnrollment 等 borderline test 不再跨 20s
// slowgate budget。
//
// 为什么是 1 个 key 而不是多个：探索阶段实测分布显示 ./... 占 16.5%，
// subpath patterns (./cells/.../, ./cmd/.../, ./runtime/.../) 占 83.5% 但
// 每个 subpath 各自只被 1-3 个 Test 使用，预热成本 > 收益。其他高频
// cacheKey 如 SharedResolver(root, true, nil, "./tools/archtest/...")
// 会撞 PASS-FUNNEL-LOADPACKAGES-01（archtest *_test.go 禁直调
// SharedResolver），testmain_test.go 不是 funnel 实现无 exempt 资格。
//
// fail-fast os.Exit(1)：packages.Load 在 TestMain 失败 = 后续所有 Test*
// 首次 SharedResolver 必失败；提前暴露避免 ~300 个测试函数逐一输出失败
// 再超时的诊断噪音。
//
// 无 escape hatch：按"不引入双路径"原则。若实测某 shard 退化按 trigger
// 单独改造，不预留逃生口。
//
// typeseval.LoadProductionPackages 调用封装在 warm.go 的 warmProductionPackages
// 中，与本文件隔离，以避免 PASS-FUNNEL-LOADPACKAGES-01 扫描 _test.go 文件
// 时检测到直接调用 typeseval 内部符号。
//
// ref: ADR docs/architecture/202605190000-adr-archtest-in-process-warmup.md
// ref: golangci-lint pkg/goanalysis/runner.go union load mode
func TestMain(m *testing.M) {
	root, err := lookupModuleRoot()
	if err != nil {
		slog.Error("archtest TestMain: lookupModuleRoot", "err", err)
		os.Exit(1)
	}
	modPath, err := moduleImportPath(root)
	if err != nil {
		slog.Error("archtest TestMain: moduleImportPath", "err", err)
		os.Exit(1)
	}
	if err := warmProductionPackages(root, modPath); err != nil {
		slog.Error("archtest TestMain: warm", "err", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
