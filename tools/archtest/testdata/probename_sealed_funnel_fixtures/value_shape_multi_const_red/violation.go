// Package value_shape_multi_const_red 是 PROBENAME-SEALED-FUNNEL-01/A1
// value-shape sub-rule 多 const ValueSpec 漏检的合成 RED fixture。
//
// 旧 scanner 在循环 vs.Names 时只读 vs.Values[0]（OkProbe），
// 第二项 BadProbe 的 hyphen 永远不会经过 NewProbeName 验证 —
// false negative。修复后 scanner 用 vs.Values[i] 配对索引，第二项命中。
//
// 期望 diagnostic：A1 value-shape 在 BadProbe 行 fire，contains
// "fails healthz.NewProbeName validator"。
package value_shape_multi_const_red

import "github.com/ghbvf/gocell/kernel/healthz"

// VIOLATION A1/value-shape on second const: BadProbe 含 hyphen,
// 旧 scanner 取 vs.Values[0]=OkProbe 通过, BadProbe 漏检。
const (
	OkProbe, BadProbe healthz.ProbeName = "ok_ready", "bad-hyphen_ready"
)
