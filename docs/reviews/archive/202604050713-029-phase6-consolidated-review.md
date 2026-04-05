# 全仓基线 Code Review 合流报告

**审查日期**: 2026-04-05
**审查基线**: develop 分支 commit 2014298
**方法**: 六角色独立审查 × 5 阶段 + 跨阶段合流裁决

---

## Executive Summary

| 指标 | 值 |
|------|-----|
| 原始 finding 总数 | 256 |
| 去重后 finding 数 | 约 196（60 条跨阶段/跨角色重复） |
| P0（阻塞） | 9 |
| P1（当前迭代修） | 110 |
| P2（tech-debt） | 137 |
| 跨角色共识（3+ 角色独立发现） | 18 个主题 |

---

## 一、P0 阻塞项清单（9 条，去重后 7 个独立问题）

### P0-1: Parser 接受空字符串 ID — pm.Cells[""] 注册
- **来源**: 阶段 2 F-2S-04
- **影响**: 所有下游消费者假设 ID 非空，空 ID 导致 map key 碰撞、governance 误判、generator 崩溃
- **修复**: parser 中 unmarshal 后验证 ID 非空 + 匹配 schema pattern `^[a-z][a-z0-9-]*$`

### P0-2: Contract OwnerCell 必填性 — schema 声明可选但 governance/generator 强制必填
- **来源**: 阶段 2 F-2P-01
- **影响**: 文档与实现不一致。generator 依赖 ownerCell 推导 boundary，若为空会 NPE
- **修复**: 在 contract.schema.json 的 required 数组中添加 ownerCell

### P0-3: Validator / TargetSelector / Runner 对 nil project 直接 panic（3 处）
- **来源**: 阶段 3 F-3S-01/02/03
- **影响**: 若 parser 返回 nil（如空目录），所有 governance/verify 命令 panic
- **修复**: 在 Validate()/SelectFromFiles()/VerifySlice() 开头添加 nil 守卫

### P0-4: DEP-02 环检测未验证契约对象存在
- **来源**: 阶段 3 F-3D-05
- **影响**: endpoints 字段为空时图构建不完整，可能漏掉真实循环依赖导致部署死锁
- **修复**: 构建图前验证所有被引用契约存在且 endpoints 非空

### P0-5: CLI 未展示 Severity/IssueType — CI 无法精细控制
- **来源**: 阶段 3 F-3P-11
- **影响**: CI pipeline 无法区分 error/warning，无法实现 audit vs enforce 模式
- **修复**: CLI 输出添加 `[ERROR]`/`[WARNING]` 前缀 + 支持 `--output=json`

### P0-6: validate 未被强制为前置条件
- **来源**: 阶段 3 F-3P-12 + 阶段 4 F-4P-02（同一问题）
- **影响**: scaffold/generate 可在非法 metadata 上执行，生成错误代码
- **修复**: 在 scaffold/generate 执行前调用 Validator，失败则拒绝

### P0-7: 孤立 contract http.x.v1 — ownerCell=test-cell 不存在
- **来源**: 阶段 5 F-5A-01
- **影响**: 污染契约注册表，governance 应检出但未检出（ownerCell 验证缺陷）
- **修复**: 删除 `contracts/http/x/v1/` 目录

---

## 二、P1 主题聚类（110 条，聚类为 15 个主题）

### 主题 A: 并发安全（6 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-1A-03/04/09/15 | 1 | Health()/BaseCell/Dependencies/Register 无 mutex |
| F-1S-01/02/03/06/07 | 1 | 魔鬼代言人独立确认同一组问题 |

**裁决**: 合并为 1 个工作项。为 CoreAssembly 和 BaseCell 添加 sync.RWMutex，或在文档中明确"单线程初始化"约束。

### 主题 B: metadata 与 cell 类型冗余（4 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-2A-01/07 | 2 | CellMeta vs CellMetadata、SliceVerifyMeta vs VerifySpec 平行定义 |
| F-2D-04 | 2 | 一致性等级映射缺失 |

**裁决**: 选择 metadata.*Meta 为权威源，cell 包中的 CellMetadata/VerifySpec 降级为 runtime-only 接口，不做持久化。

### 主题 C: Parser 缺字段验证（8 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-2T-01/02 | 2 | belongsToCell 空 key + 重复 ID 无检测 |
| F-2S-01/02/03/05 | 2 | 魔鬼代言人确认同一组 |
| F-2P-03 | 2 | PM 确认下游影响 |

**裁决**: 在 parser unmarshal 后添加验证阶段——检查 ID 非空、格式合法、无重复。考虑集成 JSON Schema 验证。

### 主题 D: Governance 规则缺陷（7 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-3T-01 | 3 | 缺 contract.kind 枚举验证 |
| F-3T-02 | 3 | TOPO-03 通配符 "*" 未处理 |
| F-3T-03 | 3 | VERIFY-01 空 expiresAt waiver 被接受 |
| F-3D-01 | 3 | TOPO-01 未验证 provider role 与实际 provider 匹配 |
| F-3T-05 | 3 | 硬编码 "L0" 字符串 |

**裁决**: 优先修 VERIFY-01（安全问题）和 TOPO-03 通配符（false positive），再补 kind 枚举规则。

### 主题 E: 路径遍历 / 注入风险（8 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-3A-02, F-3S-05/06/07 | 3 | os.Stat 路径遍历（REF-11/12/contractDirFromID）|
| F-3S-09 | 3 | resolveCheckRef go test -run pattern 注入 |
| F-4S-02/03/05 | 4 | YAML 模板注入 + contract ID 路径 + module path |

**裁决**: (1) Validator 注入 FileChecker 接口替代 os.Stat；(2) 所有 YAML 模板值加引号；(3) 验证 ID/module 格式。

### 主题 F: CLI 错误处理 / 帮助文本（9 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-4A-01/05 | 4 | errcode 信息丢失、error propagation gap |
| F-4X-01/02/04/05 | 4 | FlagSet 无 Usage、required 标注不一致、printUsage 缺示例 |
| F-3P-02 | 3 | warnings exit 0 |
| F-4T-05 | 4 | 退出码全为 1 |

**裁决**: 统一 CLI 错误格式化函数，保留 errcode；定义退出码常量（0/1/2）；补 FlagSet.Usage。

### 主题 G: verify 命令未实现（4 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-4A-14, F-4T-04, F-4X-06, F-4P-01 | 4 | 100% 占位符 |

**裁决**: 标记为 Phase 5 延迟交付，CLI 输出明确提示 "test execution not implemented"。

### 主题 H: Registry 与 ProjectMeta 冗余（4 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-3A-04/05/09 | 3 | Registry 重复 ProjectMeta 索引 + governance 耦合 registry |
| F-3X-13 | 3 | Provider/Consumers 逻辑在两处重复 |

**裁决**: 短期保留 Registry 作为查询接口，但消除 governance 对 registry 的直接依赖——DependencyChecker 应直接操作 ProjectMeta。

### 主题 I: EndpointsMeta union-type 无运行时约束（4 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-2A-09, F-2D-02, F-2S-11, F-2P-04 | 2+3 | 混杂字段，编译/运行时都不检查 |

**裁决**: 在 parser 后添加 ContractMeta.Validate() 方法检查 endpoints 与 kind 一致性。

### 主题 J: Schema embed 未消费（3 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-2A-03, F-2T-04, F-2S-02 | 2 | 7 个 JSON Schema 嵌入但零使用 |

**裁决**: 短期删除 embed.go 和 .json 文件，或添加 TODO 追踪。若要启用需集成 JSON Schema 库。

### 主题 K: 测试覆盖缺口（6 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-1T-10 | 1 | Assembly 异常序列测试缺失 |
| F-2T-06 | 2 | 端到端集成测试不完整 |
| F-3T-08 | 3 | 无效 kind、通配符场景缺失 |
| F-4T-01 | 4 | 缺 roundtrip 测试（scaffold→parse→validate）|
| F-3T-06/09 | 3 | 验证结果排序不稳定 + 无排序测试 |

**裁决**: 优先补 roundtrip 测试（跨层保证）和并发测试。

### 主题 L: Generator 确定性（2 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-4T-02 | 4 | time.Now() 破坏输出确定性 |
| F-4A-18 | 4 | sourceFingerprint 未哈希 deploy 配置 |

**裁决**: 注入 TimeNowFunc；扩展 fingerprint 哈希范围。

### 主题 M: 禁用字段名残留（2 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-2D-01 | 2 | generator_test.go 和 errcode_test.go 使用 assemblyId/sliceId/cellId |
| F-4D-01/03 | 4 | boundary.yaml 模板使用 assemblyId |

**裁决**: 修改模板和测试，统一使用 V3 字段名。

### 主题 N: 业务资产不一致（5 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-5D-02/03 | 5 | journey 声明的参与 cell 与 contract subscribers 不匹配 |
| F-5S-04 | 5 | boundary.yaml 导出内部审计事件 |
| F-5A-02, F-5S-01 | 5 | http.auth.me.v1 无实现 |

**裁决**: 对齐 contract subscribers 与 journey.cells；boundary 生成逻辑应区分内部/外部事件。

### 主题 O: 文档/DX 缺失（12 条）
| Finding | 阶段 | 描述 |
|---------|------|------|
| F-3X-08/10 | 3 | 导出类型缺 godoc、缺规则注册表 |
| F-2X-01/02/03 等 | 2 | 解析错误缺行号、字段无映射文档 |
| F-4X-03/04/05 | 4 | scaffold 冲突无修复建议、printUsage 缺示例 |

**裁决**: 归入 tech-debt，不阻塞合流。

---

## 三、修复优先级路线图

### Wave 1: 阻塞修复（P0，立即）

| # | 问题 | 工作量 | 修复方式 |
|---|------|--------|---------|
| 1 | 删除孤立 contract http.x.v1 | 5min | `rm -rf contracts/http/x/` |
| 2 | Parser 验证 ID 非空 + 格式 | 30min | parser.go unmarshal 后添加检查 |
| 3 | Contract schema 添加 ownerCell required | 5min | contract.schema.json |
| 4 | Validator/TargetSelector/Runner nil 守卫 | 15min | 3 个方法开头添加 nil check |
| 5 | DEP-02 验证契约对象存在 | 20min | depcheck.go 图构建前检查 |
| 6 | CLI 展示 Severity 前缀 | 30min | helpers.go formatResults 添加标签 |
| 7 | scaffold/generate 添加 validate 前置 | 30min | 调用 Validator，失败返回 error |

**预计总工作量**: ~2.5h

### Wave 2: 安全修复（P1 安全相关，本周）

| # | 问题 | 工作量 |
|---|------|--------|
| 1 | YAML 模板值加引号 | 30min |
| 2 | VERIFY-01 空 expiresAt 不视为有效 | 15min |
| 3 | TOPO-03 通配符 "*" 处理 | 15min |
| 4 | Validator 注入 FileChecker 替代 os.Stat | 1h |
| 5 | 验证 contract ID/module path 格式 | 30min |

**预计总工作量**: ~2.5h

### Wave 3: 结构性改进（P1 设计相关，本迭代）

| # | 问题 | 工作量 |
|---|------|--------|
| 1 | 并发安全（BaseCell/Assembly mutex） | 2h |
| 2 | Parser 重复 ID 检测 + belongsToCell 推断 | 1h |
| 3 | 补 kind 枚举验证规则 | 30min |
| 4 | CLI 错误格式统一 + 退出码 | 1h |
| 5 | 禁用字段名修复（模板 + 测试） | 30min |
| 6 | 业务资产对齐（subscribers vs journey.cells） | 1h |
| 7 | Roundtrip 测试 scaffold→parse→validate | 1h |

**预计总工作量**: ~7h

### Wave 4: Tech-debt（P2，后续迭代）

归入 tech-debt 跟踪，共 137 条，主要类别：
- 类型冗余消除（metadata vs cell 包统一）
- Registry 重构
- EndpointsMeta 类型化
- Schema embed 清理或启用
- DX 改进（godoc、帮助文本、规则文档）
- verify 命令实现
- Generator 确定性

---

## 四、跨阶段耦合点总图

```
metadata (阶段2)
  ├─→ governance (阶段3): 规则假设 ID 非空、kind 有效
  ├─→ registry (阶段3): 重复 ProjectMeta 索引
  ├─→ scaffold (阶段4): 模板基于 metadata 类型
  ├─→ generator (阶段4): 依赖 ownerCell 推导 boundary
  └─→ assets (阶段5): YAML 由 parser 加载

cell (阶段1)
  └─→ assembly (阶段1): 状态机管理 Cell 生命周期

governance (阶段3)
  ├─→ CLI validate (阶段4): Severity 信息需透传
  ├─→ CLI scaffold/generate (阶段4): 应前置调用
  └─→ assets (阶段5): 规则应检出 subscribers vs 实现不一致

generator (阶段4)
  └─→ boundary.yaml (阶段5): 字段名、导出列表一致性
```

---

## 五、最终 Signoff

| 阶段 | 状态 | 条件 |
|------|------|------|
| 1 基础原语 | 带条件通过 | Wave 3 #1 并发安全修复后 |
| 2 元数据 | 阻塞→通过 | Wave 1 #2/#3 修复后 |
| 3 治理 | 阻塞→通过 | Wave 1 #4/#5/#6 修复后 |
| 4 CLI/脚手架 | 带条件通过 | Wave 1 #7 + Wave 2 修复后 |
| 5 业务资产 | 带条件通过 | Wave 1 #1 + Wave 3 #6 修复后 |
| **合流** | **通过** | Wave 1 全部 + Wave 2 全部完成 |

`go test ./...` 基线已全绿。Wave 1 + Wave 2 合计 ~5h 即可解除所有阻塞。
