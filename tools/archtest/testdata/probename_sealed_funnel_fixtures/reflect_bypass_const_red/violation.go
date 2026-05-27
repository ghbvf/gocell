// Package reflect_bypass_const_red 是 PROBENAME-SEALED-FUNNEL-01/B1
// 反射绕过的 const 拼接子分支 RED fixture。
//
// 旧 scanner 仅识别 reflect.MethodByName 的 BasicLit 字面量入参，
// const M = "Register" + "Readiness" 的拼接常量漏过 → false negative。
// 修复后 scanner 用 EvaluateConstString 解析 const fold + 拼接，命中。
//
// 期望 diagnostic：B1 在 MethodByName 调用点 fire。
package reflect_bypass_const_red

import (
	"reflect"
)

// methodNameViaConst 是 const folding 形态——旧 scanner 字面量识别漏过。
const methodNameViaConst = "Register" + "Readiness"

func bypassViaConst(target any) reflect.Value {
	return reflect.ValueOf(target).MethodByName(methodNameViaConst)
}
