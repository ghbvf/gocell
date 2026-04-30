# GoCell 延伸落点评估候选（E11-E14）

> 日期：2026-04-30
> 状态：**评估候选 backlog**，不在 `202604300600-radical-lightweight-revision.md` 核心 10 落点（E1-E10）承诺范围内
> 性质：每个落点 ROI 中-低，需要权衡才能决定是否纳入排期；与核心 10 落点解耦，可独立评估
> 关联：
> - `202604300600-radical-lightweight-revision.md`（核心 10 落点 E1-E10，已承诺）
> - `202604300430-engineering-research-cross-cut.md`（数据底稿）
> - `202604300800-final-form-capability-overview.md`（E1-E10 + E14 完成后的形态能力讲解）

---

## §0 编号历史与变更说明

早期对话中提及但本文未保留的延伸落点：

| 早期编号 | 议题 | 去向 | 理由 |
|---|---|---|---|
| 早期 E14 | Cell interface 12 方法精简 | **砍掉** | BaseCell embed 已处理 90% 默认实现，开发者实际只覆写 3-5 个；ROI 极低不立项 |
| 早期 E15 | Journey 概念降级为可选 plugin | **砍掉** | 用户表态 "GoCell 必须保持强结构化"，Journey 是 E2E 验收一级承诺，不能降级 |
| 早期 E16 | pkg/ 库化承诺 + Tier B 独立化路径 | **重命名为现 E14**，并在 grep 实证后大幅扩展（5 包 → 25 包） | runtime/ 14 个子包中 12 个零 framework 契约依赖，原方案严重低估 GoCell 内部库矩阵规模 |

**说明**：本文最终编号 E11-E14，与早期讨论的 E11-E16 有错位。如需恢复早期 E14 / E15 议题，记入 backlog 评估，但目前不建议执行（ROI 评估见上表）。

---

## §1 总览

| # | 落点 | 维度 | 现状 | ROI | 风险 | 建议状态 |
|---|---|---|---|---|---|---|
| **E11** | errcode 体系收敛（5 构造器 → 3） | 错误处理 | `New / Safe / Wrap / WithDetails / NewAssertion` 5 个 | 中 | 中（影响所有 cell 错误返回路径） | **评估**：与 E7-3 错误库 PII 增强协同；同 PR 切更经济 |
| **E12** | Actor 合并入 Cell（type=external） | 领域词汇 | actors.yaml 单独维护，与 cell.yaml 平级 | 中 | 中（破坏"Cell 是有界服务单元，Actor 是外部参与者"语义清晰度） | **倾向不做**：语义清晰度 > 词汇收敛 |
| **E13** | assembly.yaml 极简 | metadata | cells/entrypoint/binary/deployTemplate 4 类信息 | 中 | 低 | **可做**：必填只剩 `cells: [...]` 一行，其余从 cell.yaml 推导 |
| **E14** | pkg/ 库化承诺 + Tier B 独立化路径 | 库化定位 | pkg/ 已是库形态，但未明确文档化 | 中 | 低（纯文档动作） | **可做**：与 E5 positioning 协同；E1+E2 完成后 Tier B 自然解耦 |

---

## §2 E11 — errcode 体系收敛（5 构造器 → 3）

### 当前现状

`pkg/errcode` 暴露 5 个构造器：

```go
errcode.New(code Code, msg string) *Error
errcode.Safe(code Code, msg string) *Error          // PII-safe 标注
errcode.Wrap(err error, code Code, msg string) *Error
errcode.WithDetails(err *Error, details map[string]any) *Error
errcode.NewAssertion(code Code, format string, args ...any) *Error  // E7-3 后新增
errcode.WithSafeDetails(err *Error, safe map[string]any) *Error     // E7-3 后新增（CRDB 风格 PII 分层）
```

E7-3 完成后实际有 6 个，开发者要记忆使用场景。

### 改进方案

**收敛到 3 个**：
```go
errcode.New(code Code, msg string, attrs ...slog.Attr) *Error  // 默认 PII-safe，slog.Attr 替代 map
errcode.Wrap(err error, code Code, msg string, attrs ...slog.Attr) *Error
errcode.Assertion(format string, args ...any) *Error            // 编程不变量违反，自动用 ERR_INTERNAL_ASSERTION code
```

**变化**：
- 删 `Safe`：合并入 `New`，默认就 PII-safe（要 unsafe 必须显式 `WithUnsafeDetails(unsafe map[string]any)`，让 unsafe 路径成为「显式选择」而非「默认行为」）
- 删 `WithDetails / WithSafeDetails`：用 `slog.Attr` 替代（与 E2 中 `pkg/logtag` 对齐）
- `Assertion` 简化签名：开发者不传 code，自动用 ERR_INTERNAL_ASSERTION（断言失败本质都是同一类错误，没有领域分类）

### 风险与评估

- 影响所有 cell 错误返回路径：当前 80+ 错误码使用点都要扫一遍
- 与 E7-3（PII 增强）冲突：E7-3 加 `WithSafeDetails`，E11 又删除——**应该 E11 替代 E7-3 的部分动作**，不要先做 E7-3 再做 E11
- `slog.Attr` 替代 `map[string]any`：好处是与 logtag 对齐，但要求 errcode 与 slog 强绑定（GoCell 已用 slog，不是问题）

### 建议

**纳入 Batch 3 装备期，与 E7-3 合并执行**：
- 把 E7-3 改名为 E7-3'（错误库重构 + PII 收敛）
- 同 PR 完成 5→3 收敛 + slog.Attr 迁移
- 不单立 E11

ROI：中 — 减一些表面积，但单独看不值得一个 PR。

---

## §3 E12 — Actor 合并入 Cell（type=external）

### 当前现状

`actors.yaml` 单独维护外部系统注册（OIDC provider / S3 / SMTP server 等），与 `cells/<id>/cell.yaml` 平级但格式不同。

```yaml
# actors.yaml
actors:
  - id: oidc-provider
    type: external
    description: External OIDC IdP
    referencedBy:
      - cells: [accesscore]
        contracts: [http/oidc/login/v1]
```

### 改进方案

把 Actor 用 `cell.yaml: type=external` 表达，与 core/edge/support 同枚举：

```yaml
# cells/oidc-provider/cell.yaml（新增 external 类型 cell）
id: oidc-provider
type: external
description: External OIDC IdP
# 不需要 consistencyLevel（外部系统不归 GoCell 管理）
```

**变化**：
- 删 `actors.yaml`
- `cell.yaml type` 枚举：`core / edge / support / external`
- archtest 区分：external cell 不参与 LAYER 守卫，不能 import GoCell 包，不出现在 boundary.yaml exportedContracts

### 风险与评估

**最大风险**：破坏语义清晰度。Cell 当前的核心承诺是「有界服务单元」（runs your code），Actor 是「外部参与者」（you don't control it）。把 external system 也叫 Cell：
- 新人会困惑：accesscore（你写的代码）和 oidc-provider（外部 IdP）都是 Cell？
- archtest 要加复杂条件分支（type==external 时跳过大部分检查）
- contract 双向校验（exporter / importer）要区分 cell 类型

**收益**：词汇 6 → 5（减一个 Actor）

**判断**：**不建议做**。词汇收敛带来的 ~5% 体感改进，远不及破坏 Cell 语义清晰度的代价。Actor 的存在意义是显式区分「我们的代码」和「外部依赖」——这是治理 + 文档的真实需求。

### 建议

**留在 backlog 不动**。如果后续发现 actors.yaml 维护成本高，再单独评估其他简化方案（例如把 actors.yaml 字段从 5 个减到 2 个）。

ROI：低 - 负 — 不建议执行。

---

## §4 E13 — assembly.yaml 极简

### 当前现状

```yaml
# assemblies/corebundle/assembly.yaml
id: corebundle
cells:
  - accesscore
  - auditcore
  - configcore
entrypoint: cmd/corebundle
binary: bin/corebundle
deployTemplate:
  kind: kubernetes
  manifest: deploy/k8s/corebundle.yaml
```

4 类信息：cells / entrypoint / binary / deployTemplate

### 改进方案

**最小必填**只剩 `cells` 一行：
```yaml
id: corebundle
cells: [accesscore, auditcore, configcore]
```

其他从 cell.yaml type 自动推导：
- `entrypoint`：默认 `cmd/<assembly-id>/`，存在则用，否则报错
- `binary`：默认 `bin/<assembly-id>`
- `deployTemplate`：cell type 全是 core 时默认 standard k8s template；含 device 类型 cell 时默认 device template

**复杂部署**（多 listener / 自定义资源限额 / 多 namespace）才显式声明 `deployTemplate`。

### 风险与评估

- 风险低：assembly.yaml 是部署时配置，不影响代码路径
- 收益：assembly.yaml 从 ~30 行降到 ~5 行（典型场景）
- 与 E5 positioning 的 Service Weaver 风味卖点协同：「代码不变，部署形态由 assembly 决定」—— assembly.yaml 越简单，这个卖点越锋利

### 建议

**纳入 Batch 3 装备期或单独小 PR**。技术上简单，与其他落点解耦。

ROI：中 — 0 风险，体感改进明确，但不在核心摩擦数据点上。

---

## §5 E14 — pkg/ 库化承诺 + Tier B 独立化路径

### 当前现状（grep 实证修订，2026-04-30）

**关键发现**：runtime/ 14 个子包中 **10 个零 framework 契约依赖**，加 http/observability 的二级子包则有 **13 个零或近零依赖**。GoCell 内部「自带轻库」规模远超之前估计，绝大多数 runtime/ 子包都可独立使用。

**实证方法**：grep `import "github.com/ghbvf/gocell/(kernel/(cell|assembly|metadata|wrapper|governance)|runtime/bootstrap)"` 计数（这些是 GoCell-specific framework 契约的核心包）。

#### Tier A — 已经是库形态（`pkg/` 整层）

| 包 | 内容 | framework 契约引用 |
|---|---|---|
| `pkg/errcode` | 错误体系（80+ Code + Category + PII-safe）| 0 |
| `pkg/httputil` | JSON 编解码 + pagination + 5xx mask | 0 |
| `pkg/ctxkeys` | request_id / trace_id / span_id 上下文 key | 0 |
| `pkg/query` | DB query builder helpers | 0 |
| `pkg/secutil` | TLS endpoint validation + 密码学 helpers | 0 |

#### Tier B — runtime/ 完全独立子包（grep 实证零 framework 契约）

| 包 | 内容 | framework 契约引用 |
|---|---|---|
| `runtime/config` | 配置加载 + fsnotify 热更新 + ConfigMap symlink pivot | **0** |
| `runtime/crypto` | 密码学辅助 | **0** |
| `runtime/distlock` | 分布式锁原语 | **0** |
| `runtime/eventbus` | In-memory pub/sub | **0** |
| `runtime/outbox` | Transactional outbox 实现 | **0** |
| `runtime/shutdown` | NotifyContext + signal handling | **0** |
| `runtime/websocket` | WebSocket helpers | **0** |
| `runtime/worker` | Worker pool | **0** |
| `runtime/observability/logging` | slog 结构化日志包装 | **0** |
| `runtime/observability/metrics` | Provider 接口抽象 | **0** |
| `runtime/observability/poolstats` | Pool 统计指标采集 | **0** |
| `runtime/http/healthtest` | health 测试辅助 | **0** |

#### Tier B — kernel/ + runtime/ 可独立化的领域中间件（E1+E2 完成后自然解耦）

| 包 | 内容 | 当前契约引用 | 解耦动作 |
|---|---|---|---|
| `kernel/idempotency` | Claimer/Receipt 两阶段幂等控制 | 与 `cell.HandleResult` 耦合 | E1 删除外露 Disposition 后解耦 |
| `kernel/outbox` | Transactional outbox 接口定义 | 与 `cell.EventRegistrar` 耦合 | E2 删除自动发现后解耦 |
| `kernel/persistence/tx` | TxRunner | 含 NoopTxRunner 冗余 | E1 删除 NoopTxRunner 后更干净 |
| `kernel/observability/metrics` | Provider 抽象 | 0 | 已可用 |
| `kernel/metricschema` | OBS-01 typed gate | 0 | 已可用（独立 lint 工具） |
| `runtime/observability/tracing` | OTel adapter | 2 处轻度引用 | 小改动可解耦 |
| `adapters/postgres/migrator` | goose wrapper + invalid-index 检测 | 0 | 已可用 |
| `adapters/postgres/pool` | Pool + PoolStats | 0 | 已可用 |

#### Tier C — Framework 核心，不应作为独立库

| 包 | framework 契约引用 | 理由 |
|---|---|---|
| `kernel/cell / kernel/metadata / kernel/assembly` | 自身定义契约 | 是 framework 契约本身 |
| `runtime/bootstrap` | 3 处（assembly + cell + wrapper） | 10-phase 是 GoCell 启动模型 |
| `runtime/auth` | 2 处（cell + wrapper） | 通过 authProvider Cell 自动发现 JWT verifier |
| `runtime/command` | 2 处（assembly + cell） | CLI 子命令编排，依赖 assembly 模型 |
| `runtime/eventrouter` | 2 处（cell + wrapper） | 依赖 cell.EventRegistrar 自动发现机制（E2 后会减少） |
| `runtime/http/health` | 6 处 | 与 cell.HealthContributor 集成 |
| `runtime/http/middleware` | 2 处（cell + wrapper） | 部分中间件需要 cell 上下文 |
| `runtime/http/router` | 12 处 | RouteGroupContributor 集成层（E2 后会大幅减少） |

#### 数量对比

| 层级 | 包数（修订前估计） | 包数（grep 实证） |
|---|---|---|
| Tier A 通用库 | 5 | 5 |
| Tier B 可独立中间件 | 5-8 | **20**（+ runtime/ 12 个零依赖子包） |
| Tier C framework 核心 | "runtime/ 整层" | 8（仅强耦合的 8 个子包） |

**修正认识**：runtime/ 不是「framework 核心一部分」，而是「framework 核心（8 个子包）+ 大量独立中间件（12 个子包）」的混合层。其中**完全独立的 12 个子包**性质与 pkg/ 等同，已经是库形态。

### 改进方案

**纯文档 + godoc + archtest 动作**：

1. **README 加「GoCell 自带通用库清单」段**（基于 grep 实证扩展）：
   ```markdown
   ## 自带通用库（无需用全套 GoCell framework 即可独立使用）

   GoCell 单 go.mod，所有以下包均可被外部 Go 项目直接 `import`，
   无需引入 framework 本身。v1.0 后按 SemVer 守 API。

   ### Tier A — 通用工具（pkg/）
   - pkg/errcode：结构化错误处理（PII-safe + Code 枚举 + HTTP 状态码映射）
   - pkg/httputil：HTTP JSON 处理 + pagination + 5xx mask
   - pkg/ctxkeys：request_id / trace_id / span_id 上下文 key
   - pkg/query：DB query builder
   - pkg/secutil：TLS endpoint validation

   ### Tier B — 完全独立的 runtime 工具（runtime/，零 framework 契约依赖）
   - runtime/config：配置加载 + fsnotify 热更新 + ConfigMap symlink pivot
   - runtime/crypto：密码学辅助
   - runtime/distlock：分布式锁原语
   - runtime/eventbus：In-memory pub/sub
   - runtime/outbox：Transactional outbox 实现
   - runtime/shutdown：NotifyContext + signal handling
   - runtime/websocket：WebSocket helpers
   - runtime/worker：Worker pool
   - runtime/observability/logging：slog 结构化日志包装
   - runtime/observability/metrics：Provider 接口抽象
   - runtime/observability/poolstats：Pool 统计指标采集
   - runtime/observability/tracing：OTel adapter（轻度耦合，可独立用）
   - runtime/http/healthtest：health 测试辅助

   ### Tier B — 领域中间件（E1+E2 完成后自然解耦）
   - kernel/idempotency：分布式幂等控制（Claim/Commit/Release）
   - kernel/outbox：Transactional outbox 接口定义
   - kernel/persistence/tx：TxRunner（事务运行器）
   - kernel/observability/metrics：Provider 抽象
   - kernel/metricschema：OBS-01 typed gate（独立 lint 工具）
   - adapters/postgres/migrator：goose wrapper + invalid-index 检测
   - adapters/postgres/pool：Pool + PoolStats

   合计 25 个独立可用包。
   ```

2. **每个 Tier B 包加 godoc 顶部声明**：
   ```go
   // Package idempotency provides distributed idempotency primitives
   // (Claim/Commit/Release) suitable for any Go service that needs
   // exactly-once semantics over message queues or HTTP retries.
   //
   // This package can be used independently of the GoCell framework.
   package idempotency
   ```

3. **E5 positioning 文档协同**：在 positioning.md 加一段「GoCell = 重 framework + 自带轻库」，明确两种入口都被支持。

4. **archtest 守护 Tier A/B 不引 Cell**：写 `tools/archtest/library_independence_test.go`，确保 `pkg/...` 和声明为 Tier B 的包不 import `kernel/cell`、`kernel/metadata`、`kernel/assembly`、`runtime/bootstrap`。

### 风险与评估

- 0 代码改动（仅文档 + godoc + 1 个 archtest）
- 收益：让外部理解 GoCell 提供「重 framework + 轻库」双形态，**不是把 GoCell 改轻，而是承认内部已有轻库可独立用**——这是从重框架向轻库感受度移动的另一维度
- 与 E5 positioning 协同
- E1+E2 完成后 Tier B 自然解耦，可同时把这些包标记为 "library independent"

### 建议

**与 E5 positioning 一起做（Batch 1 PR 1）**：

- E5 写 positioning.md
- 同 PR 在 README 加「自带通用库清单」段
- Tier B 包的 godoc 标注延后到 E1+E2 完成后（自然时机）
- archtest 守护放 Batch 3 装备期

ROI：中 — 0 成本，立即受益。

---

## §6 评估候选与核心 10 落点的关系

| 关系 | 落点 | 处理 |
|---|---|---|
| **可与核心落点合并** | E11（与 E7-3 合并）、E14（与 E5 合并 README + godoc 部分） | 合并执行，不单立 PR |
| **可单独执行** | E13（assembly.yaml 极简） | Batch 3 末期或单独小 PR |
| **不建议执行** | E12（Actor 合并入 Cell） | 留 backlog 不动；语义清晰度 > 词汇收敛 |

---

## §7 数据来源

延伸落点的来源：
- E11：来自 §3 抽象过载实证（agent `adc5ed5850f25d22e`）+ E7-3 PII 增强方案
- E12：来自 agent `adc5ed5850f25d22e` 词汇分层（一级 6 词中可否减一个）
- E13：来自 agent `a917888423ecc6710` yaml 字段统计（assembly.yaml 4 类信息）
- E14：来自 agent `af8ebe8f929a7e4f1` § 反射使用 + import 统计（pkg/ 已是库形态的实证）+ §3 与 SoT 项目 pkg 库化对比
