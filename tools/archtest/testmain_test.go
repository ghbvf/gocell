// INVARIANT: ARCHTEST-TESTMAIN-01

package archtest

import (
	"log/slog"
	"os"
	"testing"
)

// TestMain 在所有 Test* 跑前预热 typeseval.SharedResolver 的 2 个高频
// cacheKey（详见 warm.go::warmProductionPackages 顶部清单）。每行是
// cacheKey 元组 (tests, tags, patterns):
//
//  1. (false, nil,                  "./...") — production-only (LAYER-* + 多数业务 Test*)
//  2. (true,  FlatNonDefaultTags(), "./...") — Tests:true 跨 tag 10 site，含
//     TestNotFoundTestStrict + TestCellIDPatternSingleSource
//
// 把首次 cache miss 时机从 borderline test 的 wall 内移到 shard startup 外。
//
// 为什么是 2 个而非 4 个：4-key 初版（issue #860 Amendment 2026-05-22 初版）
// 在 GHA shard 13 触发 SIGTERM (exit 143) — 4× 全模块 *types.Info 同时
// 驻留 cache 累积 RSS 超 GHA 2-CPU 7GB shard 限制。回退到 2 keys 对齐
// ADR §"B 轴" 已验证的 2-Load 安全形态。
//
// 未预热但当前未触发的 cacheKey:
//   - (true, nil, "./...") 6 site (TestImplementsFunnel / TestOutboxInvariants 等)
//   - (true, ProductionFlatTags(), "./...") 2 site (TestExternalReasonLiteral 系列)
//   - (true, *, "./tools/archtest/...") 5 site (scanner_framework_usage 等)
//
// 全部按 ADR §66 "按 trigger 单独改造而非预留逃生口" 留待将来 trigger。
//
// fail-fast os.Exit(1)：packages.Load 在 TestMain 失败 = 后续所有 Test*
// 首次 SharedResolver 必失败；提前暴露避免 ~300 个测试函数逐一输出失败
// 再超时的诊断噪音。
//
// 无 escape hatch：按"不引入双路径"原则。本地单测 (e.g.
// `go test ./tools/archtest/ -run TestFoo`) 仍付完整 warm 成本——这是
// 开发者本地 wash，CI 16-shard parallel 总 wall = max(shard wall) 由
// 重 shard 决定，warmup 在重 shard 净赚。开发循环加速推荐 -run 多个
// test 一次跑完（warmup 摊销）。
//
// typeseval.LoadProductionPackages 调用封装在 warm.go 的 warmProductionPackages
// 中，与本文件隔离，以避免 PASS-FUNNEL-LOADPACKAGES-01 扫描 _test.go 文件
// 时检测到直接调用 typeseval 内部符号。
//
// ref: ADR docs/architecture/202605190000-adr-archtest-in-process-warmup.md
// §65 (重写) + Amendment 2026-05-22 (2-cacheKey 扩展，4-key 回退)
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
	slog.Info("archtest TestMain: warming SharedResolver (may take 30-50s on cold run, 2 cacheKeys)")
	if err := warmProductionPackages(root, modPath); err != nil {
		slog.Error("archtest TestMain: warm", "err", err, "modRoot", root, "modPath", modPath)
		os.Exit(1)
	}
	slog.Info("archtest TestMain: warm-up complete")
	os.Exit(m.Run())
}
