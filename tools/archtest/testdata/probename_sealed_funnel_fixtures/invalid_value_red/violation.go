// Package invalid_value_red 是 PROBENAME-SEALED-FUNNEL-01/A1 value-shape
// hyphen 子分支的 RED fixture。
//
// 拆分自原 invalid_value_red（uppercase / double-underscore 已迁出到平级
// value_shape_uppercase_red / value_shape_double_underscore_red fixture）。
// 单一形态 per fixture，便于 reverse-fixture case 表用 contains 字段做
// 分支断言。
package invalid_value_red

import "github.com/ghbvf/gocell/kernel/healthz"

// VIOLATION A1/value-shape: hyphen fails ^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$.
const HyphenProbe healthz.ProbeName = "bad-hyphen_ready"
