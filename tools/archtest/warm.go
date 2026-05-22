package archtest

import (
	"fmt"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// warmProductionPackages pre-loads the two SharedResolver cacheKey entries
// covering production scans and the highest-impact Tests:true shape. Each
// row is the cacheKey tuple (tests, tags, patterns):
//
//  1. (false, nil,                  "./...") — production-only scans
//  2. (true,  FlatNonDefaultTags(), "./...") — Tests:true cross-tag union
//
// Both cacheKeys remain in SharedResolver cache for the lifetime of the
// process so subsequent Test* calls hit cache.
//
// 串行执行（不并行）：packages.Load 已 CPU/IO bound，并行只会抢资源；同时
// 这是 ADR 202605190000 §"B 轴 RSS 峰值"已验证的 2×全模块安全 RSS 形态
// （GHA 2-CPU 7GB shard 限制），并行或加更多 cacheKey 会复发 OOM SIGTERM。
//
// 为什么只预热这 2 个 cacheKey 而非 issue #860 / Amendment 初版的 4 个：
// CI 实证 4-key 预热在 GHA shard 13 触发 SIGTERM（exit 143） — 4× 全模块
// *types.Info 同时驻留 cache 累积 RSS 超 7GB shard 限制。Amendment 2026-05-22
// 二轮修订（详见 ADR）回退到 2 keys，对齐 ADR §"B 轴" 已验证的 2-Load 形态。
//
// 关掉的 issue Trigger 失败 test:
//   - TestNotFoundTestStrict        (cacheKey #2 cache hit)
//   - TestCellIDPatternSingleSource (cacheKey #2 cache hit)
//
// 未关掉的 issue Trigger 失败 test (仍走 allowlist):
//   - TestExternalReasonLiteral / _NoIndirectReferences
//     (ProductionFlatTags 路径不预热 — 2 site 边际收益 << RSS 风险)
//
// 6 个 (true, nil, "./...") site (TestImplementsFunnel / TestOutboxInvariants
// 等) 当前未触发 SLOW，按 ADR §66 "按 trigger 单独改造而非预留逃生口"
// 留待将来触发后单独评估扩展。
//
// 5 个 (true, *, "./tools/archtest/...") subpath site (scanner_framework_usage /
// eval_predicate_centralization) 同上原则。
//
// warm.go 直调 typeseval 是 ADR §80 既有 carve-out（PASS-FUNNEL-LOADPACKAGES-01
// 只扫 _test.go）；FlatNonDefaultTags() 是 resolve.go 已导出的 helper。
func warmProductionPackages(modRoot, modulePath string) error {
	if _, err := typeseval.LoadProductionPackages(modRoot, modulePath, false, nil); err != nil {
		return fmt.Errorf("warmProductionPackages[production]: %w", err)
	}
	if _, err := typeseval.LoadProductionPackages(modRoot, modulePath, true, FlatNonDefaultTags()); err != nil {
		return fmt.Errorf("warmProductionPackages[tests-flat-tags]: %w", err)
	}
	return nil
}
