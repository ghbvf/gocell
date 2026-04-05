# 阶段 2 六角色基线审查报告

**审查日期**: 2026-04-05
**审查基线**: develop 分支 commit 2014298
**范围**: `kernel/metadata/`（types.go, parser.go, schemas/）、`actors.yaml`

## Executive Summary

- 总 finding 数: 61（P0: 1, P1: 33, P2: 27）
- 合流��塞项: 1（F-2S-04 空字符串 ID / F-2P-01 OwnerCell 必填性不一致）
- Signoff: **阻塞** — 1 个 P0 + 大量 P1 需在阶段 3 启动前解决

## 跨角色共识

1. **空 ID / 缺失字段静默通过**（6/6 角色）— 架构师 F-2A-02 + 工具 F-2T-01/02 + 魔鬼 F-2S-01/03/04 + DX F-2X-03 + PM F-2P-02/03
2. **JSON Schema 嵌入但未消费**（4/6 角色）— 架构师 F-2A-03 + 工具 F-2T-04 + 魔鬼 F-2S-02 + PM F-2P-07
3. **metadata.CellMeta vs cell.CellMetadata 类型冗余**（3/6 角色）— 架构师 F-2A-01/07 + 领域 F-2D-04 + PM
4. **EndpointsMeta union-type 无运行时约束**（4/6 角色）— 架构师 F-2A-09 + 领域 F-2D-02 + 魔鬼 F-2S-11 + PM F-2P-04
5. **一致性等级/枚举值无验证**（4/6 角色）— 架构师 F-2A-02 + 领域 F-2D-04 + 魔鬼 F-2S-12 + DX F-2X-04

---

## 架构师 Findings（10 条）

### F-2A-01: 平行冗余类型层次 — metadata.CellMeta vs cell.CellMetadata
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:6` vs `kernel/cell/interfaces.go:28`
- **描述**: 两套完全平行的 struct 映射同一 cell.yaml，字段设计不同（string vs 强类型）。违反 DRY，不是单一事实源。

### F-2A-02: metadata 类型使用 string 字段无验证
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:6-14`
- **描述**: CellMeta.Type/ConsistencyLevel 为原始 string，无编译时或运行时类型安全。

### F-2A-03: Schema embed 文件嵌入但从未消费
- **严重度**: P2 | **分类**: NIT
- **文件**: `kernel/metadata/schemas/embed.go:5-6`
- **描述**: 7 个 JSON Schema 文件嵌入二进制但零处使用，增加体积约 17KB，schema 与实际类型可能偏离。

### F-2A-04: ProjectMeta 聚合不完整 — Actors/StatusBoard 为数组非 map
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:152-161`
- **描述**: Actors 和 StatusBoard 存储为 `[]` 而非 `map[id]*`，消费者需二次索引（如 journey.Catalog）。

### F-2A-05: 包依赖方向正确但缺文档化约束
- **严重度**: P2 | **分类**: NIT
- **文件**: `kernel/metadata/parser.go:1-12`

### F-2A-06: Registry 包重复 ProjectMeta 的聚合逻辑
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `kernel/registry/cell.go:17-46`, `kernel/registry/contract.go:19-37`
- **描述**: CellRegistry/ContractRegistry 每次调用都重新构建二级索引。应由 ProjectMeta 预计算或提供方法。

### F-2A-07: SliceVerifyMeta vs cell.VerifySpec 类型冗余
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:53-66` vs `kernel/cell/interfaces.go:13-25`

### F-2A-08: Parser 缺少规范化步骤
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `kernel/metadata/parser.go:32-71`
- **描述**: unmarshalFile() 后无验证/规范化，ProjectMeta 可能包含无效数据（如 type:"invalid"）。

### F-2A-09: EndpointsMeta 字段设计过于宽泛
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:81-96`
- **描述**: 所有 kind 的端点字段混在一个 struct 中，无编译时约束。

### F-2A-10: metadata 包使用模式缺文档
- **严重度**: P2 | **分类**: NIT
- **文件**: `kernel/metadata/types.go:1-3`

---

## 领域专家 Findings（9 条）

### F-2D-01: 禁用字段名在生成代码/测试中仍被使用
- **严重度**: P1 | **分类**: BUG
- **文件**: `kernel/assembly/generator_test.go:313,345`, `pkg/errcode/errcode_test.go:104,109`
- **描述**: 测试中使用 `assemblyId`, `sliceId`, `cellId` 等 CLAUDE.md 明确禁止的旧字段名。

### F-2D-02: EndpointsMeta union-type 缺乏运行时类型安全
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:81-96`

### F-2D-03: SliceVerifyMeta unit/contract 必填性标记不一致
- **严重度**: P1 | **分类**: BUG
- **文件**: `kernel/metadata/types.go:52-57`, `schemas/slice.schema.json:39-88`

### F-2D-04: 一致性级别在 metadata 层与 cell 层映射缺失
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:9,72` vs `kernel/cell/types.go:19-58`

### F-2D-05: Journey.Contracts 策展语义无代码层面标注
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:106-120`

### F-2D-06: Actor 与 Cell 在 contract endpoints 中无类型区分
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:145-150`

### F-2D-07: 六条真相无代码层面保证
- **严重度**: P1 | **分类**: DESIGN
- **描述**: ProjectMeta 为纯数据结构，无 Validate() 方法。六条真相仅在外部 governance 可选检查。

### F-2D-08: Schema 中 events 的 schemaRefs 字段定义不完整
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `schemas/contract.schema.json:38-43`

### F-2D-09: ProjectMeta.Slices compound key 设计脆弱
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:155`, `kernel/metadata/parser.go:135`

---

## 工具工程师 Findings（8 条）

### F-2T-01: belongsToCell 缺失导致错误 Slice key
- **严重度**: P1 | **分类**: BUG
- **文件**: `kernel/metadata/parser.go:135`
- **描述**: BelongsToCell 为空时 key 为 `"/sliceID"`，多个缺失 slice 互相覆盖。

### F-2T-02: 缺少重复 ID 检测
- **严重度**: P1 | **分类**: BUG
- **文件**: `kernel/metadata/parser.go:126,145,154,163`
- **描述**: 所有 parse*() 直接向 map 写入，同名 ID 无声覆盖。

### F-2T-03: 错误包装缺少字段级位置信息
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/parser.go:187-198`

### F-2T-04: Schema 验证与 Go Struct 验证脱离
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/schemas/` + `parser.go`

### F-2T-05: 畸形 YAML 处理不完整
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/parser.go`
- **描述**: 缺少字段类型错误、缺失必填字段、多余字段、无效枚举值等测试。

### F-2T-06: 缺少完整的端到端集成测试
- **严重度**: P1 | **分类**: NIT
- **文件**: `kernel/metadata/parser_integration_test.go`

### F-2T-07: splitPath() 路���规范化边界
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `kernel/metadata/parser.go:113-117`
- **描述**: 多余斜杠或尾部斜杠可能产生空元素导致匹配失败。

### F-2T-08: ActorMeta.MaxConsistencyLevel 无运行时使用
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:146-150`

---

## DX Findings（12 条）

### F-2X-01: Contract Endpoint 字段无 Kind 映射文档
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:83-96`

### F-2X-02: Slice Key 复合键约定无文档
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:154-155`

### F-2X-03: YAML 解析错误缺行号
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `kernel/metadata/parser.go:193-195`

### F-2X-04: 无有效值常量定义
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go`

### F-2X-05: 条件必填字段无文档
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:76-78`

### F-2X-06: BelongsToCell derived-anchor 性质无注释
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:40-41`

### F-2X-07: ContractUsage.Role 值按 Kind 的映射无文档
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:46-50`

### F-2X-08: YAML tag 命名约定无说明
- **严重度**: P1 | **分类**: NIT
- **文件**: `kernel/metadata/types.go`

### F-2X-09: Parser Root 目录约定文档不清
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `kernel/metadata/parser.go:19-22`
- **描述**: 传入错误 root 时 parser 静默返回空 ProjectMeta，无错误。

### F-2X-10: Contract ID 格式无运行时验证
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:69`

### F-2X-11: PassCriterion.CheckRef 条件必填无文档
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:115-120`

### F-2X-12: ActorMeta.MaxConsistencyLevel 语义无文档
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:146-150`

---

## 魔鬼代言人 Findings（16 条）

### F-2S-01: belongsToCell 缺失导致空 key
- **严重度**: P1 | **分类**: BUG | SECURITY
- **文件**: `kernel/metadata/parser.go:130-137`

### F-2S-02: 无 JSON Schema 验证 — 关键字段可静默缺失
- **严重度**: P1 | **分类**: DESIGN | SECURITY
- **文件**: `kernel/metadata/parser.go:185-198`

### F-2S-03: 重复 ID 无冲突检测
- **严重度**: P1 | **分类**: BUG
- **文件**: `kernel/metadata/parser.go:121-127`

### F-2S-04: 空字符串 ID 被接受 — pm.Cells[""]
- **严重度**: P0 | **分类**: SECURITY
- **文件**: `kernel/metadata/types.go:6-14`, `parser.go:121-127`
- **描述**: ID 为空字符串或 null 时被接受并注册为 `pm.Cells[""]`。Schema pattern 约束被完全绕过。

### F-2S-05: 无 YAML Bomb 防御
- **严重度**: P1 | **分类**: SECURITY
- **文件**: `kernel/metadata/parser.go:185-198`
- **描述**: yaml.Unmarshal 无 size limit/recursion depth/alias 限制，恶意 YAML 可导致 OOM/DoS。

### F-2S-06: 路径遍历防御缺失（schemaRefs 字段）
- **严重度**: P1 | **分类**: SECURITY
- **文件**: `kernel/metadata/parser.go:113-117`, `types.go`

### F-2S-07: 类型强制转换歧义
- **严重度**: P1 | **分类**: NIT
- **文件**: `types.go`, `parser.go`

### F-2S-08: Contract ID 格式无校验
- **严重度**: P1 | **分类**: NIT
- **文件**: `types.go:68-79`, `parser.go:140-147`

### F-2S-09: Journey Pattern 大小写敏感跨平台不一致
- **严重度**: P2 | **分类**: NIT
- **文件**: `parser.go:96-104`

### F-2S-10: Symlink 攻击面
- **严重度**: P2 | **分类**: SECURITY
- **文件**: `parser.go:25-71`

### F-2S-11: EndpointsMeta Kind-Specific 字段无强制
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `types.go:81-96`

### F-2S-12: ConsistencyLevel 无枚举检查
- **严重度**: P1 | **分类**: NIT
- **文件**: `types.go`

### F-2S-13: ActorMeta Type 字段无枚举检查
- **严重度**: P1 | **分类**: NIT
- **文件**: `types.go:146-150`

### F-2S-14: PassCriteria auto 条件无验证
- **严重度**: P1 | **分类**: NIT
- **文件**: `types.go:115-120`

### F-2S-15: Waiver ExpiresAt 无日期格式验证
- **严重度**: P1 | **分类**: NIT
- **文件**: `types.go:59-65`

### F-2S-16: additionalProperties 无运行时强制
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `parser.go`, `types.go`

---

## PM Findings（12 条）

### F-2P-01: Contract Schema OwnerCell 必填性与实现不一致
- **严重度**: P0 | **分类**: DESIGN
- **文件**: `schemas/contract.schema.json:1-30`, `types.go:68-79`, `governance/rules_ref.go:49-64`
- **描述**: Schema 声明 ownerCell 可选，但 governance REF-03 强制要求非空，scaffold 也强制 --owner 必填。Generator 依赖 ownerCell 推导边界。

### F-2P-02: BelongsToCell 可选声明与实现不符
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `schemas/slice.schema.json:15-18`, `parser.go:130-137`

### F-2P-03: Parser 缺乏字段验证，允许不完整 ProjectMeta 返回
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `kernel/metadata/parser.go:32-71`

### F-2P-04: EndpointsMeta 条件约束无法在 Go 类型中表达
- **严重度**: P1 | **分类**: DESIGN
- **文件**: `types.go:83-96`, `schemas/contract.schema.json:59-166`

### F-2P-05: StatusBoard/Actors 为数组不支持大规模扩展
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/types.go:152-161`

### F-2P-06: Parse() vs ParseFS() 文档不清
- **严重度**: P2 | **分类**: NIT
- **文件**: `kernel/metadata/parser.go:14-29`

### F-2P-07: Embed 指令无文件范围验证
- **严重度**: P1 | **分类**: BUG
- **文件**: `kernel/metadata/schemas/embed.go:1-6`

### F-2P-08: 缺少元数据类型版本管理和演进策略
- **严重度**: P2 | **分类**: DESIGN

### F-2P-09: splitPath() 路径规范化文档不足
- **严重度**: P2 | **分类**: NIT
- **文件**: `kernel/metadata/parser.go:112-117`

### F-2P-10: 新增元数据类型无扩展点设计
- **严重度**: P2 | **分类**: DESIGN
- **文件**: `kernel/metadata/parser.go:41-71`

### F-2P-11: 集成测试覆盖有限
- **严重度**: P1 | **分类**: NIT
- **文件**: `kernel/metadata/parser_integration_test.go:24-128`

### F-2P-12: 包级文档缺失
- **严重度**: P2 | **分类**: NIT

---

## 跨阶段依赖

| Finding | 来��� | 依赖阶段 | 性质 |
|---------|------|---------|------|
| F-2A-01 类型冗余 | 阶段 2 | 阶段 1 (cell.CellMetadata) | 两套类型需统一 |
| F-2D-01 禁用字段名 | 阶段 2 | 阶段 1 (generator_test) | 生成代码使用旧字段名 |
| F-2S-04 空 ID | 阶段 2 | 阶段 3 (governance) | governance 假设 ID 非空 |
| F-2P-01 OwnerCell | 阶段 2 | 阶段 3+4 (governance+generator) | 必填性需统一 |
