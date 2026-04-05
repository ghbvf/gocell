# 阶段 4 六角色基线审查报告

**审查日期**: 2026-04-05
**审查基线**: develop 分支 commit 2014298
**范围**: `kernel/scaffold/`、`kernel/assembly/generator.go + gentpl/`、`cmd/gocell/`

## Executive Summary

- 总 finding 数: 45（P0: 2, P1: 14, P2: 29）
- 合流阻塞项: 2 个 P0（verify 未实现 + validate 未前置）
- Signoff: **带条件通过** — P0 可通过文档明确为 Phase 5 延迟交付

## 跨角色共识

1. **verify 命令未实现**（4/6 角色）— 架构 F-4A-14 + 工具 F-4T-04 + DX F-4X-06 + PM F-4P-01
2. **validate 未被前置**（3/6）— PM F-4P-02 + 架构 F-4A-05（error propagation gap）
3. **YAML 模板未转义用户输入**（2/6）— 魔鬼 F-4S-02 + 领域
4. **contract ID 前缀 != kind 未校验**（3/6）— 架构 F-4A-06 + 魔鬼 F-4S-03 + PM F-4P-07
5. **CLI 错误码/帮助不完整**（3/6）— 工具 F-4T-05 + DX F-4X-01/04 + PM F-4P-04
6. **scaffold 与 generator 模板基础设施重复**（2/6）— 架构 F-4A-02 + F-4A-06

---

## 架构师 Findings

### F-4A-01: CLI 错误码信息丢失——fmt.Errorf 丢弃 errcode.Code
- P1 | DESIGN | `cmd/gocell/*.go`

### F-4A-02: scaffold 与 generator 模板基础设施重复
- P1 | DESIGN | `scaffold/templates.go` vs `assembly/gentpl/embed.go`

### F-4A-03: Generator module path 无格式验证
- P2 | DESIGN | `cmd/gocell/generate.go:57-65`

### F-4A-04: Scaffold skip-on-conflict 无 --force 选项
- P2 | DESIGN | `scaffold/scaffold.go:184-189`

### F-4A-05: CLI-to-Kernel 错误传播不完整——generate 直接做 I/O
- P1 | DESIGN | `cmd/gocell/generate.go:87-105`

### F-4A-06: Contract ID 解析规则硬编码且无完全验证
- P2 | DESIGN | `scaffold/scaffold.go:129-143`

### F-4A-07: Generate 输出目录硬编码
- P2 | DESIGN | `cmd/gocell/generate.go:87-89`

### F-4A-08: 子命令参数命名不一致
- P2 | NIT | `cmd/gocell/scaffold.go`, `check.go`

### F-4A-09: Generator 未检查 Assembly.Cells 都存在于元数据中
- P2 | BUG | `assembly/generator.go:85-91`

### F-4A-10: CreateSlice 只检查目录存在不检查 cell.yaml
- P1 | BUG | `scaffold/scaffold.go:96-101`

### F-4A-11: 模板字段不完整——缺少多个可选字段
- P1 | DESIGN | `scaffold/templates/*.tpl`

### F-4A-12: CLI 缺 version 命令
- P2 | NIT | `cmd/gocell/main.go`

### F-4A-13: findRoot() 不适合 monorepo
- P2 | DESIGN | `cmd/gocell/helpers.go:15-31`

### F-4A-14: verify 命令只是占位符
- P2 | NIT | `cmd/gocell/verify.go:40-72`

### F-4A-15: check 命令占位符风格与 verify 不一致
- P2 | NIT | `cmd/gocell/check.go:78-90`

### F-4A-16: main.go.tpl 生成代码含 TODO 注释
- P2 | DESIGN | `assembly/gentpl/main.go.tpl:12-15`

### F-4A-17: CLI 无 --verbose/--debug 模式
- P2 | NIT | `cmd/gocell/main.go`

### F-4A-18: sourceFingerprint 未哈希 deploy 配置
- P1 | DESIGN | `assembly/generator.go:173-205`

---

## 领域专家 Findings

### F-4D-01: boundary.yaml 使用禁用字段名 assemblyId
- P1 | BUG | `assembly/gentpl/boundary.yaml.tpl:4`

### F-4D-02: Contract 模板一致性等级硬编码
- P2 | DESIGN | `scaffold/templates/contract-*.yaml.tpl`

### F-4D-03: boundary.yaml 字段名混用 camelCase
- P1 | BUG | `assembly/gentpl/boundary.yaml.tpl`

### F-4D-05: verify 命令未实现
- P1 | DESIGN | `cmd/gocell/verify.go`

### F-4D-06: Contract ID 前缀与 kind 不做匹配校验
- P1 | DESIGN | `scaffold/scaffold.go:112-144`

### F-4D-07: Command contract 模板缺 idempotencyKey 字段
- P2 | DESIGN | `scaffold/templates/contract-command.yaml.tpl`

---

## 工具工程师 Findings

### F-4T-01: 缺 roundtrip 测试（scaffold→parse→validate）
- P1 | TEST | `scaffold_test.go`

### F-4T-02: Generator time.Now() 破坏输出确定性
- P1 | BUG | `assembly/generator.go:103`

### F-4T-03: 4 种 contract 模板缺语法测试
- P2 | TEST | `scaffold/templates/*.tpl`

### F-4T-04: verify 命令未连接到 Runner
- P1 | DESIGN | `cmd/gocell/verify.go`

### F-4T-05: CLI 退出码全为 1 无语义区分
- P2 | DESIGN | `cmd/gocell/main.go`

### F-4T-06: Generator 测试使用生产数据名称
- P2 | NIT | `assembly/generator_test.go`

### F-4T-07: sourceFingerprint 变更敏感性测试缺失
- P2 | TEST | `assembly/generator_test.go:374`

---

## DX Findings

### F-4X-01: FlagSet 无自定义 Usage——help 输出差
- P1 | DESIGN | `cmd/gocell/scaffold.go`, `verify.go`, `generate.go`

### F-4X-02: required vs optional flags 标注不一致
- P1 | DESIGN | `cmd/gocell/scaffold.go`

### F-4X-03: scaffold 冲突错误无修复建议
- P2 | DESIGN | `scaffold/scaffold.go:187-188`

### F-4X-04: printUsage() 缺示例和子命令详情
- P2 | DESIGN | `cmd/gocell/main.go:33-42`

### F-4X-05: findRoot 失败消息不可操作
- P2 | DESIGN | `cmd/gocell/helpers.go:15-31`

### F-4X-06: verify 命令名误导——不实际执行验证
- P2 | DESIGN | `cmd/gocell/verify.go`

### F-4X-07: FlagSet 错误格式与 main 错误格式不一致
- P2 | NIT | `cmd/gocell/scaffold.go`, `main.go`

---

## 魔鬼代言人 Findings

### F-4S-01: 模板 kind 参数注入——当前靠白名单缓解但脆弱
- P2 | SECURITY | `scaffold/scaffold.go:141`

### F-4S-02: YAML 模板未转义用户值——含 `:` `#` `\n` 可注入
- P1 | SECURITY | 所有 `scaffold/templates/*.tpl`
- 描述: `{{.ID}}` `{{.Goal}}` 等无引号保护，`id: test: malicious` 或换行可注入结构

### F-4S-03: Contract ID 路径遍历——ID 前缀与 kind 不做匹配
- P1 | SECURITY | `scaffold/scaffold.go:130-138`

### F-4S-04: CreateSlice cellID 只查目录不查 cell.yaml
- P2 | SECURITY | `scaffold/scaffold.go:97-101`

### F-4S-05: --module flag 无格式验证——可注入恶意 import path
- P1 | SECURITY | `cmd/gocell/generate.go:42-65`

### F-4S-06: scaffold 拒绝覆盖 vs generator 无条件覆盖——不一致
- P2 | DESIGN | `scaffold/scaffold.go:186` vs `cmd/gocell/generate.go:94-100`

---

## PM Findings

### F-4P-01: verify 命令 100% 占位符——阻塞交付
- **P0** | DESIGN | `cmd/gocell/verify.go`
- 所有 verifySlice/Cell/Journey 只展示 metadata，不执行测试

### F-4P-02: validate 未被强制为 scaffold/generate 的前置条件
- **P0** | DESIGN | `cmd/gocell/scaffold.go`, `generate.go`
- 与阶段 3 F-3P-12 同一问题在此阶段确认

### F-4P-03: 仅支持 4 种 contract kind——无扩展点
- P2 | DESIGN | `scaffold/scaffold.go:123`

### F-4P-04: CLI 退出码全为 1——CI 无法区分错误类型
- P2 | DESIGN | `cmd/gocell/main.go`

### F-4P-05: Journey scaffold 生成空 passCriteria 无示例
- P2 | DESIGN | `scaffold/templates/journey.yaml.tpl`

### F-4P-06: 生成文件无 .gitignore 指导
- P2 | NIT | `cmd/gocell/generate.go`

### F-4P-07: Contract ID 前缀不校验 kind——生成目录与内容不匹配
- P1 | DESIGN | `scaffold/scaffold.go:112-143`

---

## 跨阶段依赖

| Finding | 来源 | 依赖阶段 | 性质 |
|---------|------|---------|------|
| F-4P-02 validate 未前置 | 阶段 4 | 阶段 3 (F-3P-12) | 同一问题 |
| F-4D-01 禁用字段名 | 阶段 4 | 阶段 2 (F-2D-01) | boundary.yaml 用 assemblyId |
| F-4T-01 roundtrip 测试 | 阶段 4 | 阶段 2+3 (parser+governance) | scaffold 输出需通过验证 |
| F-4S-02 YAML 注入 | 阶段 4 | 阶段 2 (parser) | 注入的 YAML 可能绕过 parser |
