# ADR: RunMode 跨层翻译边界

- 编号: ADR-RUNMODE-TRANSLATION-01
- 日期: 2026-04-18
- 状态: Accepted
- 相关: backlog P1-6 / PR#165 reviewer F1-1 / PR-P-QUERY

## 上下文

GoCell 在两个抽象层上有"模式"（mode）概念：

| 层 | 类型 | 含义 | 位置 |
|----|------|------|------|
| `kernel/cell.DurabilityMode` | `DurabilityDurable` / `DurabilityDemo` | 整个 Cell/Assembly 的持久性契约：demo 允许 in-memory publisher / fail-open 路径；durable 严格 L2 原子性 | `kernel/cell/assembly.go` |
| `pkg/query.RunMode` | `RunModeProd`（零值） / `RunModeDemo` | 分页查询层的 fail-open/fail-closed：demo 在 cursor decode 失败时回落首页；prod 返回错误 | `pkg/query/runmode.go` |

两个枚举互有对应关系（demo↔demo、durable↔prod），但**不是同一个概念**：
- `DurabilityMode` 描述整个 Cell 的 L2 写路径；
- `RunMode` 只描述 `pkg/query.ExecutePagedQuery` 与 `configpublish.WithRunMode` 消费者的"容错姿态"。

问题：这两层在何处、由谁、如何做翻译？错误地散落到 slice/handler/repository 会让 demo 语义四处漂移，回归审查困难，也违反分层规则（`pkg/` 不能依赖 `kernel/`）。

## 分层依赖约束

`CLAUDE.md` 明确规定：

```
pkg/       允许依赖: 标准库         禁止依赖: kernel/ cells/ runtime/ adapters/
```

因此 `pkg/query.RunMode` **不能** `import "github.com/ghbvf/gocell/kernel/cell"`，也不能直接接收 `cell.DurabilityMode` 参数——否则包裹方向倒置、pkg 层不再是叶子。

## 决策

**翻译规则（三条）：**

1. **唯一翻译函数**：`pkg/query.RunModeForDemo(bool) query.RunMode`。该函数是 `DurabilityMode ↔ RunMode` 的唯一入口，签名中**不**出现 `cell.*` 类型，只接受 `bool`。
2. **唯一翻译时机**：Cell 的 `Init(deps cell.InitDeps)` 方法内，按如下样板：
   ```go
   runMode := query.RunModeForDemo(deps.DurabilityMode == cell.DurabilityDemo)
   ```
   翻译在此完成一次，`runMode` 作为不可变参数通过构造函数（`NewService` 或 `With...` Option）向下传播。
3. **禁止二次翻译**：slice service、handler、repository、pkg 内部函数**禁止**：
   - 再次调用 `RunModeForDemo`
   - 重新观察 `DurabilityMode`（`pkg/` 本就无法感知）
   - 为 `bool` 参数额外加 `demoFailOpen`、`isDemo` 等并行旗标

违反第 3 条会出现"两个真相源"，例如 PR#165 之前的 `configpublish.WithDemoFailOpen(bool)` 与 `RunMode` 重复表达同一信号。PR-P-QUERY 已合并为单一 `WithRunMode(query.RunMode)`。

## 对标

| 框架 | 对应设计 | 采纳点 |
|------|---------|--------|
| zeromicro/go-zero `ServiceConf.Mode`（DevMode/TestMode/PreMode/ProMode） | 默认值 = 最严格（ProMode）；`MustSetUp()` 一次翻译，下游组件只读取已翻译的值 | `RunModeProd` 是零值 + 只在 `Init()` 翻译 |
| kube-apiserver `--feature-gates` | 在启动期解析为 `map[Feature]bool`，运行期读只读快照 | 翻译→只读快照的模式 |

## 拒绝的替代方案

- **让 `pkg/query.RunMode` 直接接受 `cell.DurabilityMode`**：破坏 pkg 层依赖方向。
- **把 `RunMode` 放到 `kernel/cell` 下**：`pkg/query` 被 runtime/cell 双向依赖，层级反转。
- **让每个 slice 各自做翻译**：demo 决策散落 5+ 个位置；改 demo 语义时必须扫全仓。
- **保留 `bool demoFailOpen` + 引入 `RunMode` 并存**：两个真相源；新写的 slice 不知道该用哪个。

## 实施追溯

- 引入：`pkg/query/runmode.go`（`RunModeForDemo` godoc 现含 "Do not extend" 警告）
- 统一 configpublish：`WithDemoFailOpen` 删除，改用 `WithRunMode`（PR-P-QUERY）
- 调用点：
  - `cells/config-core/cell.go::Init` — 单次翻译，下发给 config-read / feature-flag / config-publish
  - `cells/audit-core/cell.go::Init`
  - `cells/order-cell/cell.go::Init`
  - `cells/device-cell/cell.go::Init`

## 扩展路径

当需要更复杂的模式演进时，可按如下接口 sketch 扩展，无需修改现有翻译函数：

### N-key rotation（多前一密钥）

当前 `CursorCodec` 支持 `current + previous` 两槽。若将来需同时兼容多个旧密钥
（例如分批轮换，分批老客户端超时更长），可将 `previous []byte` 改为 `previous [][]byte`，
`Decode` 遍历顺序不变（current → previous[0] → previous[1] → …）。接口变更仅在
`pkg/query/cursor.go`，对 Cell/slice 不透明，翻译点（`cell.go::Init`）无需修改。

### 外部 KMS 集成

在 `cell.go::Init` 注入阶段，将 `WithCursorCodec` 的参数来源由 `loadCursorCodec`（env）
改为 KMS SDK 调用：

```go
// 示例演进接口（不影响 NewCursorCodec 签名）
key, err := kmsClient.GetCurrentDataKey(ctx, "cursor/audit")
prevKey, _ := kmsClient.GetPreviousDataKey(ctx, "cursor/audit")
codec, err := query.NewCursorCodec(key, prevKey)
```

KMS 适配器实现在 `adapters/kms/`（待添加），`cell.go` 仅通过接口感知，不直接依赖 SDK。

### HSM 硬件密钥

HSM 场景下 HMAC 签名在硬件内完成，`CursorCodec.signWith` 需替换为 HSM 调用。
演进方式：抽出 `type Signer interface { Sign(data []byte) ([]byte, error) }` 接口，
`CursorCodec` 依赖 `Signer` 而非裸 `[]byte` key，HSM 实现在 `adapters/hsm/`。
当前架构下此扩展不涉及 `RunMode` 或翻译函数，可独立演进。

## 相关不修

- Kratos app 层无 Mode 字段，依赖注入模式；GoCell 维持 opinionated 翻译函数不改用注入，因为 Cell 声明式模型（`cell.yaml`）与 `DurabilityMode` 强绑定，注入只会把复杂度外扩。
