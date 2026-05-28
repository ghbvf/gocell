// Package value_shape_double_underscore_red 是 PROBENAME-SEALED-FUNNEL-01/A1
// value-shape double-underscore 子分支的独立 RED fixture。
package value_shape_double_underscore_red

import "github.com/ghbvf/gocell/kernel/healthz"

// VIOLATION A1/value-shape: double underscore fails (?:_[a-z0-9]+)* group.
const DoubleUnderProbe healthz.ProbeName = "double__under_ready"
