# 阶段 3 六角色基线审查报告

**审查日期**: 2026-04-05
**审查基线**: develop 分支 commit 2014298
**范围**: `kernel/governance/`、`kernel/registry/`、`kernel/journey/`、`kernel/slice/`

## Executive Summary

- 总 finding 数: 81（P0: 5, P1: 38, P2: 38）
- 合流阻塞项: 5 个 P0
- Signoff: **阻塞** — nil project panic（3 处）+ validate 未强制前置 + CLI 未展示 Severity

## 跨角色共识

1. **nil project panic**（4/6 角色）— 架构 F-3A-10 + 魔鬼 F-3S-1/2/3 + PM
2. **Registry 与 ProjectMeta 冗余**（3/6）— 架构 F-3A-04/05 + DX F-3X-13 + PM
3. **Provider/Consumers 逻辑重复**（3/6）— 架构 F-3A-09 + DX F-3X-13 + 工具
4. **ValidationResult 字段标记 unused 但被使用**（3/6）— 架构 F-3A-10 + DX F-3X-01 + PM F-3P-01
5. **validate 未强制为前置条件**（2/6）— PM F-3P-12 + F-3P-09
6. **TOPO-03 通配符 "*" 处理缺失**（2/6）— 工具 F-3T-02 + 领域
7. **VERIFY-01 空 expiresAt waiver 被接受**（2/6）— 工具 F-3T-03 + 魔鬼 F-3S-10
8. **路径遍历风险 REF-11/12**（2/6）— 架构 F-3A-02 + 魔鬼 F-3S-5/6/7

---

## 架构师 Findings（14 条）

### F-3A-01: isProviderRole() 重复定义
- P2 | DESIGN | `governance/depcheck.go:214` vs `rules_topo.go`

### F-3A-02: Validator 含 os.Stat() 违反纯校验原则
- P1 | DESIGN | SECURITY | `governance/rules_ref.go:255,309`
- 应注入 FileChecker 接口

### F-3A-03: 辅助函数散落多个规则文件
- P2 | DESIGN | `rules_ref.go`, `rules_topo.go`, `depcheck.go`

### F-3A-04: CellRegistry/ContractRegistry 与 ProjectMeta maps 冗余
- P1 | DESIGN | `registry/cell.go:10-46`, `registry/contract.go:11-37`

### F-3A-05: governance 依赖 registry 造成不必要耦合
- P1 | DESIGN | `governance/depcheck.go:7-8`

### F-3A-06: Errors()/Warnings() 方法使用率低
- P2 | NIT | `governance/validate.go:108-138`

### F-3A-07: 规则文件分组清晰但缺顶部注释
- P2 | NIT | `governance/rules_*.go`

### F-3A-08: DEP-02 环检测算法缺文档
- P2 | DESIGN | `governance/depcheck.go:74-155`

### F-3A-09: Provider/Consumers 逻辑在 registry 和 governance 中重复
- P2 | DESIGN | `registry/contract.go:54-96` vs `governance/rules_topo.go:11-40`

### F-3A-10: ValidationResult IssueType/Severity 有 //nolint:unused
- P1 | BUG | `governance/validate.go:34-42`

### F-3A-11: ByKind()/ByOwner() 索引使用率低
- P2 | DESIGN | `registry/contract.go:44-52`

### F-3A-12: Journey Catalog 缺灵活查询
- P2 | DESIGN | `journey/catalog.go`

### F-3A-13: Slice Verify Runner 不验证 CheckRef 格式
- P2 | DESIGN | `slice/verify.go:109-182`

### F-3A-14: 测试 helper findByCode 归属不清
- P2 | NIT | `governance/validate_test.go:133-142`

---

## 领域专家 Findings（16 条）

### F-3D-01: TOPO-01 未验证 provider role slice 与契约实际 provider 匹配
- P1 | BUG | `governance/rules_topo.go:43-65`

### F-3D-02: ADV-02 IssueType 语义不当（IssueForbidden 用于 deprecated）
- P2 | DESIGN | `governance/rules_advisory.go:45-59`

### F-3D-03: contractIDFromPath 假设条件未覆盖四层路径
- P1 | DESIGN | `governance/targets.go:143-158`

### F-3D-04: expandFromSlices 的 journey 扩展不完备性未标注
- P1 | DESIGN | `governance/targets.go:179-187`

### F-3D-05: DEP-02 环检测未验证契约对象是否存在
- P0 | BUG | `governance/depcheck.go:74-155`
- endpoints 字段为空时图构建不完整，可能漏掉真实循环依赖

### F-3D-06: 缺少 journey.contracts 引用来源验证
- P1 | DESIGN | `governance/validate.go:56-106`

### F-3D-07: Catalog.CellJourneys 排序与业务语义不匹配
- P2 | DESIGN | `journey/catalog.go:56-87`

### F-3D-08: Runner.VerifySlice 测试路径硬编码
- P1 | DESIGN | `slice/verify.go:49-80`

### F-3D-09: VERIFY-01 仅检查 provider 角色，消费方无覆盖
- P2 | DESIGN | `governance/rules_verify.go:18-59`

### F-3D-10: waiver 过期检查时间逻辑重复 + 时区歧义
- P2 | DESIGN | `governance/rules_verify.go:61-133`

### F-3D-11: ContractRegistry.Provider/Consumers 返回值语义不一致
- P1 | DESIGN | `registry/contract.go:54-96`

### F-3D-12: CellRegistry.SlicesFor 返回顺序不确定
- P2 | DESIGN | `registry/cell.go:53-56`

### F-3D-13: NewCatalog statusBoard 指针可能悬挂
- P1 | BUG | `journey/catalog.go:30-34`

### F-3D-14: FMT-08 契约 ID 前缀验证不够严格
- P2 | DESIGN | `governance/rules_fmt.go:207-229`

### F-3D-15: 缺少 slice.allowedFiles 字段定义和验证
- P1 | DESIGN | 全局（metadata/types.go + governance/validate.go）

### F-3D-16: ADV-01/ADV-04 对 status-board 的假设不对称
- P2 | DESIGN | `governance/rules_advisory.go:5-28,87-103`

---

## 工具工程师 Findings（10 条）

### F-3T-01: 缺失 contract.kind 枚举验证规则
- P1 | DESIGN | `governance/rules_fmt.go:10-230`
- 无效 kind 如 "grpc" 可通过全部规则

### F-3T-02: TOPO-03 缺少通配符 "*" 处理
- P2 | BUG | `governance/rules_topo.go:98-127`
- consumers=["*"] 时所有消费方被误报

### F-3T-03: VERIFY-01 空 expiresAt 被视为有效 waiver
- P1 | BUG | `governance/rules_verify.go:14-59`

### F-3T-04: FMT-08 未验证 contract.kind 有效性
- P2 | BUG | `governance/rules_fmt.go:207-229`

### F-3T-05: L0 使用硬编码 "L0" 字符串而非枚举
- P2 | NIT | `rules_topo.go:169`, `rules_verify.go:144`, `rules_fmt.go:161`

### F-3T-06: 验证结果排序不稳定（map 遍历顺序随机）
- P2 | DESIGN | `governance/validate.go:56-106`

### F-3T-07: verify/waiver 闭包只单向验证
- P2 | DESIGN | `governance/rules_verify.go`

### F-3T-08: 测试覆盖不足——无效 kind、通配符场景缺失
- P1 | TEST | `governance/validate_test.go`

### F-3T-09: 排序稳定性测试缺失
- P2 | TEST | `governance/validate_test.go`

### F-3T-10: contractProvider/contractConsumers 对未知 kind 静默返回空
- P2 | BUG | `governance/rules_topo.go:10-40`

---

## DX Findings（13 条）

### F-3X-01: ValidationResult 字段标记 unused 但被使用
- P1 | DESIGN | `governance/validate.go:36-41`

### F-3X-02: HasErrors/Errors/Warnings 是 Validator 方法但操作 []ValidationResult
- P1 | DESIGN | `governance/validate.go:108-138`

### F-3X-03: 错误消息缺 Details 结构化数据
- P1 | DESIGN | `governance/rules_ref.go` 多处

### F-3X-04: File 字段路径格式不一致（filepath.Join vs strings.Join）
- P2 | DESIGN | `governance/rules_ref.go:195-220`

### F-3X-05: Registry/Catalog 缺批量查询接口
- P2 | DESIGN | `registry/*.go`, `journey/catalog.go`

### F-3X-06: Journey Catalog 缺关键导航接口
- P2 | DESIGN | `journey/catalog.go`

### F-3X-07: Slice Verify 错误信息缺细节
- P2 | DESIGN | `slice/verify.go:52-80`

### F-3X-08: 导出类型缺详细 godoc
- P1 | DESIGN | `governance/validate.go:33-42`, `slice/verify.go:18-30`

### F-3X-09: DEP-02 cycle 消息缺结构化数据
- P2 | DESIGN | `governance/depcheck.go:144-152`

### F-3X-10: 缺中央规则注册表/文档
- P1 | DESIGN | `governance/` 整体

### F-3X-11: DEP 规则未集成到 Validator
- P2 | NIT | `governance/depcheck.go:33-37`

### F-3X-12: TargetSelector 返回 nil vs 空切片不一致
- P1 | DESIGN | `governance/targets.go:189-208`

### F-3X-13: Provider/Consumers 逻辑重复
- P1 | DESIGN | `registry/contract.go:54-96` vs `governance/rules_topo.go:10-40`

---

## 魔鬼代言人 Findings（16 条）

### F-3S-01: Validator.Validate() 不检查 nil project — panic
- **P0** | BUG | SECURITY | `governance/validate.go:56`

### F-3S-02: TargetSelector 对 nil project 无防护 — panic
- **P0** | BUG | SECURITY | `governance/targets.go:42-66`

### F-3S-03: Runner 对 nil project 无防护 — panic
- **P0** | BUG | `slice/verify.go:41-46`

### F-3S-04: Catalog.Get() 对 nil project 的行为与其他组件不一致
- P1 | DESIGN | `journey/catalog.go:17-24`

### F-3S-05: REF-11 缺路径边界检查——可能的路径遍历
- P1 | SECURITY | `governance/rules_ref.go:241-267`

### F-3S-06: REF-12 schemaRefs 路径遍历风险
- P1 | SECURITY | `governance/rules_ref.go:282-322`

### F-3S-07: contractDirFromID 路径注入
- P1 | SECURITY | `governance/rules_ref.go:324-329`

### F-3S-08: slice/verify.go cellID/sliceID 路径注入
- P2 | SECURITY | `slice/verify.go:49-107`

### F-3S-09: resolveCheckRef go test -run pattern 注入
- P1 | SECURITY | `slice/verify.go:170-182`

### F-3S-10: VERIFY-01 waiver 空 expiresAt + 格式错误被当有效
- P1 | DESIGN | `governance/rules_verify.go:13-59`

### F-3S-11: VERIFY-02 时区问题
- P2 | DESIGN | `governance/rules_verify.go:61-133`

### F-3S-12: 对抗性 ProjectMeta 中 nil cell/contract/journey 导致 false negative
- P1 | DESIGN | 所有规则文件

### F-3S-13: CellRegistry 对 nil slice 静默跳过
- P2 | DESIGN | `registry/cell.go:31-44`

### F-3S-14: ContractRegistry 对 nil contract 静默跳过
- P2 | DESIGN | `registry/contract.go:18-37`

### F-3S-15: Catalog 对 nil journey 未检查
- P2 | DESIGN | `journey/catalog.go:26-29`

### F-3S-16: FMT-08 ID 无 "." 时静默跳过
- P1 | DESIGN | `governance/rules_fmt.go:207-229`

---

## PM Findings（15 条）

### F-3P-01: ValidationResult Severity/IssueType 被 //nolint:unused
- P1 | DESIGN | `governance/validate.go:36-41`

### F-3P-02: validate-meta warnings 被当 success (exit 0)
- P1 | DESIGN | `cmd/gocell/validate.go:56-68`

### F-3P-03: 规则覆盖度缺陷——Actor/Assembly/StatusBoard/PassCriteria 字段未验证
- P1 | DESIGN | 多文件

### F-3P-04: ValidationResult 消费契约未明确
- P2 | DESIGN | `governance/validate.go` + `cmd/gocell/`

### F-3P-05: REF-13/REF-14 可能过严——外部 actor 引用应允许延迟定义
- P1 | DESIGN | `governance/rules_ref.go:331-376`

### F-3P-06: DEP-02 cycle 重建算法输出可能非最小环
- P2 | DESIGN | `governance/depcheck.go:159-173`

### F-3P-07: verify CLI 命令仅为 stub
- P2 | NIT | `cmd/gocell/verify.go:67-71`, `slice/verify.go`

### F-3P-08: TargetSelector ADVISORY 标签过于保守
- P2 | NIT | `governance/targets.go`

### F-3P-09: scaffold/generate 未消费 ValidationResult
- P1 | DESIGN | `cmd/gocell/scaffold.go`, `generate.go`

### F-3P-10: waiver 机制缺用户文档
- P2 | NIT | `governance/rules_verify.go`

### F-3P-11: CLI 未展示 Severity/IssueType — CI 无法精细控制
- **P0** | DESIGN | `cmd/gocell/validate.go` + `helpers.go`

### F-3P-12: validate 未被强制为前置条件
- **P0** | DESIGN | `cmd/gocell/main.go`

### F-3P-13: 无 meta-level 验证确保每个字段被某条规则覆盖
- P2 | DESIGN | 全局

### F-3P-14: 规则测试覆盖不完整
- P2 | NIT | `governance/validate_test.go`

### F-3P-15: 缺治理规则索引文档
- P2 | NIT | 无对应文档

---

## 跨阶段依赖

| Finding | 来源 | 依赖阶段 | 性质 |
|---------|------|---------|------|
| F-3S-01/02/03 nil panic | 阶段 3 | 阶段 2 (parser 可返回 nil) | parser 应保证非 nil |
| F-3P-11/12 validate 前置 | 阶段 3 | 阶段 4 (CLI/generator) | generator 依赖合法 metadata |
| F-3T-01 kind 枚举缺失 | 阶段 3 | 阶段 2 (parser 不验证 kind) | 两层都不验证 |
| F-3D-15 allowedFiles | 阶段 3 | 阶段 2 (types.go 无此字段) | 模型需先扩展 |
| F-3A-04 Registry 冗余 | 阶段 3 | 阶段 2 (ProjectMeta 设计) | 应在 ProjectMeta 预计算索引 |
