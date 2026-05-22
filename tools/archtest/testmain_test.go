// INVARIANT: ARCHTEST-TESTMAIN-01

package archtest

import (
	"log/slog"
	"os"
	"testing"
)

// TestMain 在所有 Test* 跑前预热 typeseval.SharedResolver 的 4 个高频
// cacheKey（详见 warm.go::warmProductionPackages 顶部清单）：
//
//  1. (false, nil, "./...")                  — production-only scans (LAYER-*)
//  2. (true,  nil, "./...")                  — Tests:true 默认 6 个调用点
//  3. (true,  FlatNonDefaultTags(), "./...") — Tests:true 跨 tag 10 个调用点
//  4. (true,  ProductionFlatTags(), "./...") — Tests:true 跨 tag 2 个调用点
//
// 把首次 cache miss 时机从 borderline test 的 wall 内移到 shard startup 外，
// 让 TestUserRepoConformanceEnrollment / TestNotFoundTestStrict /
// TestCellIDPatternSingleSource / TestExternalReasonLiteral 等不再跨 20s
// slowgate budget。
//
// 为什么是这 4 个而不是更多：探索阶段实测分布显示 ./... 占 16.5%，subpath
// patterns (./cells/.../, ./cmd/.../, ./runtime/.../, ./tools/archtest/...)
// 占 83.5% 但每个 subpath 各自只被 1-3 个 Test 使用，预热成本 > 收益；按
// ADR §80 复合触发条件 (单 PR ≥ 2 modulo-shift SLOW + 单一根因) 未达。
//
// (Tests:true, *, "./tools/archtest/...") 5 个 site (scanner_framework_usage /
// eval_predicate_centralization) 当前未触发 SLOW，按 ADR §66 "按 trigger
// 单独改造而非预留逃生口" 留待将来触发后扩。
//
// fail-fast os.Exit(1)：packages.Load 在 TestMain 失败 = 后续所有 Test*
// 首次 SharedResolver 必失败；提前暴露避免 ~300 个测试函数逐一输出失败
// 再超时的诊断噪音。
//
// 无 escape hatch：按"不引入双路径"原则。本地单测 (e.g.
// `go test ./tools/archtest/ -run TestFoo`) 仍付完整 45-75s warm 成本——
// 这是开发者本地 wash，CI 16-shard parallel 总 wall = max(shard wall) 由
// 重 shard 决定，warmup 在重 shard 净赚。开发循环加速推荐 -run 多个 test
// 一次跑完（warmup 摊销）。
//
// typeseval.LoadProductionPackages 调用封装在 warm.go 的 warmProductionPackages
// 中，与本文件隔离，以避免 PASS-FUNNEL-LOADPACKAGES-01 扫描 _test.go 文件
// 时检测到直接调用 typeseval 内部符号。
//
// ref: ADR docs/architecture/202605190000-adr-archtest-in-process-warmup.md
// §65 (重写) + Amendment 2026-05-22 (4-cacheKey 扩展)
// ref: golangci-lint pkg/goanalysis/runner.go union load mode
func TestMain(m *testing.M) {
	root, err := lookupModuleRoot()
	if err != nil {
		slog.Error("archtest TestMain: lookupModuleRoot", "err", err,
			"cwd_resolution", "getwd-or-walkup-failure")
		os.Exit(1)
	}
	modPath, err := moduleImportPath(root)
	if err != nil {
		slog.Error("archtest TestMain: moduleImportPath", "err", err, "modRoot", root)
		os.Exit(1)
	}
	slog.Info("archtest TestMain: warming SharedResolver (may take 45-75s on cold run, 4 cacheKeys)")
	if err := warmProductionPackages(root, modPath); err != nil {
		slog.Error("archtest TestMain: warm", "err", err, "modRoot", root, "modPath", modPath)
		os.Exit(1)
	}
	slog.Info("archtest TestMain: warm-up complete")
	os.Exit(m.Run())
}
