package archtest

import (
	"fmt"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// warmProductionPackages pre-loads the four SharedResolver cacheKey entries
// covering all "./..." archtest scan shapes. Each row is the cacheKey tuple
// (tests, tags, patterns):
//
//  1. (false, nil,                  "./...") — production-only scans
//  2. (true,  nil,                  "./...") — Tests:true production-default
//  3. (true,  FlatNonDefaultTags(), "./...") — Tests:true cross-tag union
//  4. (true,  ProductionFlatTags(), "./...") — Tests:true production-cross-tag
//
// SharedResolver caches per cacheKey = (modRoot, tests, tags, patterns); each
// of the four entries is a fully independent packages.Load result and there
// is no requirement that the four *types.Info values be compatible — they
// never share a *types.Info instance.
//
// 串行执行（不并行）：packages.Load 已 CPU/IO bound，并行只会抢资源，无收益。
//
// 第 2/3/4 个 cacheKey 由 issue #860 / ADR 202605190000 Amendment 2026-05-22
// 引入，关闭 PR #850 fixup 三次 CI 失败的所有 Tests:true ./... cold path
// (TestNotFoundTestStrict / TestCellIDPatternSingleSource /
// TestExternalReasonLiteral / TestExternalReasonLiteral_NoIndirectReferences)。
//
// 不预热 (true, *, "./tools/archtest/...") subpath — 5 个调用点
// (scanner_framework_usage / eval_predicate_centralization) 当前未触发
// modulo-shift SLOW，按 ADR §80 复合触发条件未达；patterns 是独立 cacheKey
// 维度，按 ADR §66 "按 trigger 单独改造而非预留逃生口"。
//
// warm.go 直调 typeseval 是 ADR §80 既有 carve-out（PASS-FUNNEL-LOADPACKAGES-01
// 只扫 _test.go）；FlatNonDefaultTags() / ProductionFlatTags() 是 resolve.go
// 已导出的 helper。
func warmProductionPackages(modRoot, modulePath string) error {
	if _, err := typeseval.LoadProductionPackages(modRoot, modulePath, false, nil); err != nil {
		return fmt.Errorf("warmProductionPackages[production]: %w", err)
	}
	if _, err := typeseval.LoadProductionPackages(modRoot, modulePath, true, nil); err != nil {
		return fmt.Errorf("warmProductionPackages[tests-default]: %w", err)
	}
	if _, err := typeseval.LoadProductionPackages(modRoot, modulePath, true, FlatNonDefaultTags()); err != nil {
		return fmt.Errorf("warmProductionPackages[tests-flat-tags]: %w", err)
	}
	if _, err := typeseval.LoadProductionPackages(modRoot, modulePath, true, ProductionFlatTags()); err != nil {
		return fmt.Errorf("warmProductionPackages[tests-production-flat-tags]: %w", err)
	}
	return nil
}
