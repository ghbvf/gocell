// Package phases_with_health_checker_red 是 PROBENAME-SEALED-FUNNEL-01/A2
// `bootstrap.WithHealthChecker` callsite 的 RED fixture。
//
// PR #1187 落地后 WithHealthChecker(name healthz.ProbeName, fn ...) 是
// ProbeName funnel 的第 4 个 callsite。旧 scanner 三 callee allowlist
// (RegisterReadiness / NewProbe / HealthToProbe) 漏扫 WithHealthChecker，
// 任何裸 string 字面量被 Go untyped-const 隐式转 ProbeName 编译通过 →
// false negative。本 fixture 用裸 string 触发，新 scanner 命中。
//
// 期望 diagnostic：A2 在 WithHealthChecker 调用点 fire，contains
// "WithHealthChecker name arg"。
package phases_with_health_checker_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// VIOLATION A2/WithHealthChecker: bare untyped string literal implicitly
// converts to healthz.ProbeName at compile time — this fixture proves
// the scanner catches it.
var _ = bootstrap.WithHealthChecker("raw_literal_ready", func(_ context.Context) error { return nil })
