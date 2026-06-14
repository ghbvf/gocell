// Package value_shape_uppercase_red 是 PROBENAME-SEALED-FUNNEL-01/A1
// value-shape uppercase 子分支的独立 RED fixture（与 hyphen / double_underscore
// 拆开以单独证明每个 value-shape 子分支可被 scanner 命中）。
package value_shape_uppercase_red

import "github.com/ghbvf/gocell/framework/kernel/healthz"

// VIOLATION A1/value-shape: uppercase letters fail lowercase regex.
const UppercaseProbe healthz.ProbeName = "BadCase_ready"
