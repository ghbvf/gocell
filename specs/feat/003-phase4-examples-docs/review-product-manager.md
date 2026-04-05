# Product Manager Review -- Phase 4: Examples + Documentation

> Reviewer: PM Agent
> Date: 2026-04-06
> Input: spec.md, product-context.md, phase-charter.md
> Scope: 验收标准完备性、开发者体验、范围一致性、兼容性风险

---

## 总体评价

Phase 4 spec 准确地将 product-context 的 persona 需求和成功标准转化为功能需求。3 个梯度示例 + README Getting Started 的设计从评估者视角出发，Phase 3 must-fix 债务的纳入也体现了质量闭环意识。以下 10 条建议聚焦于验收标准的可验证性缺口、开发者体验盲区和潜在范围偏移。

---

## 建议清单

### PM-01 [验收标准缺失] P0

**建议内容**: spec US-1 的验收场景仅覆盖"服务启动"和"curl 返回 201"两步，缺少事件消费验证的验收条件。product-context S4 明确要求 todo-order 示例演示"outbox 事件发布、RabbitMQ 消费、幂等处理"，但 US-1 的 Acceptance Scenarios 在 curl 返回 201 后就停止了，没有验证事件是否被消费。

**影响分析**: 评估者按 US-1 走完后可能看到 HTTP 201 但 RabbitMQ consumer 静默失败，无法判断事件驱动是否正常工作。这直接影响 S4 成功标准（"outbox 事件发布、RabbitMQ 消费、幂等处理"）的验证完整性。作为 P1 用户场景，此缺口不可接受。

**建议修改方式**: 在 US-1 的 Acceptance Scenarios 中追加第三条：

```
3. Given 订单已创建, When 等待 3 秒后检查应用日志, Then 日志包含 "event.order.created consumed" 或等效消费确认信息
```

同时在 FR-2.8 curl 命令文档中要求包含"验证事件消费"步骤（如查看 docker logs 或查询审计端点）。

---

### PM-02 [验收标准缺失] P0

**建议内容**: FR-7.1 RS256 默认化要求"无 RSA key pair 时 fail-fast"，但 spec 中没有对应的验收场景验证 fail-fast 行为。FR-7.3 outboxWriter fail-fast 同样缺少验收场景。这两项是跨 2 个 Phase 延迟的安全债务（P2-SEC-04, P3-TD-09, K2, K3），必须有可验证的验收条件。

**影响分析**: 如果 fail-fast 行为没有明确的 Given/When/Then 验收条件，实现者可能只添加日志而不阻断启动，或者错误码不符合规范。平台架构师（persona P2）需要这些证据才能信任框架的安全承诺。

**建议修改方式**: 在 US-3 或新增 US-3a 中添加安全加固验收场景：

```
RS256 fail-fast:
  Given NewJWTIssuer 被调用且未提供 RSA private key,
  When 构造 JWTIssuer,
  Then 返回包含明确错误信息的 error（非 nil），不降级到 HS256

outboxWriter fail-fast:
  Given Cell 声明 consistencyLevel >= L2,
  When Cell.Init 被调用且 outboxWriter == nil,
  Then 返回 ERR_CELL_MISSING_OUTBOX 错误，Cell 不注册到 Assembly
```

---

### PM-03 [开发者体验] P1

**建议内容**: FR-4.4 快速开始要求"go get 安装 + 运行 todo-order 示例 + 看到 HTTP 200 响应"，标注耗时 5 分钟。但 product-context 假设 7 中说明"示例项目使用根目录 go.mod（module 路径 github.com/ghbvf/gocell），不创建独立 module"。这意味着评估者不能直接 `go get` 安装框架并在自己的项目中使用，而是必须 `git clone` 整个仓库。spec 的 FR-4.4 用词"go get 安装"与实际体验不符，会误导评估者。

**影响分析**: 框架评估者（persona P4）的第一个动作是尝试 `go get github.com/ghbvf/gocell`。如果这个命令因私有仓库而失败，且 README 没有清晰说明 clone 路径，评估者在第一步就会受挫。Edge Cases 节已提到"go get 路径指向私有仓库，需说明认证方式"，但 FR-4.4 的标题仍写"go get 安装"。

**建议修改方式**: FR-4.4 标题改为"快速开始（5 分钟）-- git clone + 运行示例"。明确快速开始路径为 `git clone -> cd examples/todo-order -> docker compose up -d -> go run .`，将 `go get` 场景作为"已有项目集成"的独立子节，并附认证配置说明（GOPRIVATE / .netrc）。

---

### PM-04 [验收标准缺失] P1

**建议内容**: US-2（从零创建自定义 Cell，30 分钟教程）的验收场景只到"HTTP handler 响应 200"就结束。但 product-context S1 的量化指标是"从 git clone 到第一个自定义 Cell 注册到 Assembly 并返回 HTTP 200 响应，总耗时 <= 30 分钟"，spec 没有定义如何验证"30 分钟"这个时间约束。

**影响分析**: "30 分钟"是 Phase 4 Gate 的核心量化指标。如果没有明确的验证方法（谁来计时？什么算起点？什么算终点？是否需要录屏？），这个成功标准在 Gate 验证时会变成主观判断。

**建议修改方式**: 在 US-2 的验收场景中增加时间验证条件：

```
3. Given 评估者已完成 US-1 快速开始,
   When 按 README 30 分钟教程从创建目录到 HTTP 200,
   Then 教程步骤总数 <= 15 步，每步有明确预期输出，无需外部文档跳转
```

补充说明：时间约束通过"步骤数量上限 + 每步自包含"间接保证，而非真实计时。Gate 验证时由审查者按步骤执行并记录实际耗时。

---

### PM-05 [开发者体验] P1

**建议内容**: FR-1 sso-bff 和 FR-3 iot-device 的复杂度较高（分别涉及 3 个内建 Cell 协作和 L4 一致性 + WebSocket），但 spec 缺少"降级运行"模式的验收条件。product-context 示例设计约束第 3 条要求"演示构造时注入（Option pattern）和环境变量切换（in-memory vs real adapter）"，Edge Cases 也提到"无 Docker 环境下应能切换到 in-memory adapter 运行"。然而 spec 的 FR-1 至 FR-3 没有将降级模式作为功能子项。

**影响分析**: 评估者在公司网络可能无法拉取 Docker 镜像，或笔记本 Docker 资源不足。如果示例只能在 Docker 全量启动后运行，评估者可能在环境准备阶段就放弃。这直接影响 persona P4 的首次体验。product-context 的 NFR-6 明确要求"示例演示 Option pattern 注入 + 环境变量切换（in-memory vs real adapter）"，但 spec FR 中未具体化为功能子项。

**建议修改方式**: 在 FR-1、FR-2、FR-3 各增加一个子项（如 FR-1.8、FR-2.9、FR-3.7）：

```
FR-x.N In-Memory 降级模式:
  通过环境变量（如 GOCELL_MODE=memory）或无 Docker 检测自动切换到 in-memory adapter。
  README 包含 "无 Docker 快速体验" 节，说明降级运行步骤和功能限制。
```

---

### PM-06 [范围偏移] P1

**建议内容**: spec FR-7.1 要求"HS256 保留为显式 Option（WithHS256(secret)）用于测试场景"。但查看当前代码（`src/runtime/auth/jwt.go`），`NewJWTIssuer` 和 `NewJWTVerifier` 的函数签名已经是接收 `*rsa.PrivateKey` / `*rsa.PublicKey` 参数，没有 HS256 路径。Phase 3 的实际实现已经完成了 RS256 迁移——`JWTIssuer` 只接受 RSA private key，`JWTVerifier` 在 KeyFunc 中显式拒绝非 RSA 签名方法。

spec FR-7.1 描述的"默认使用 RS256 + 保留 HS256 Option"与代码现状不一致。当前代码中不存在 HS256 路径，也没有 `WithHS256` Option。如果 spec 要求新增 `WithHS256`，反而是引入一个新的公开 API（范围扩大）。

**影响分析**: 如果实现者按 spec 字面执行，会新增 `WithHS256(secret)` Option，这实际上是在已完成 RS256 强制迁移的代码上"回退"一个 HS256 入口。这与 P3-TD-09 的修复目标矛盾，也增加了安全攻击面。此外，tech-debt-registry 中 P2-SEC-04 的状态为 PARTIAL（"RS256 迁移为 Option 注入，默认仍 HS256"），但代码现实已经超越了 PARTIAL 状态。

**建议修改方式**: FR-7.1 修改为：确认当前 `NewJWTIssuer(privateKey, issuer, ttl)` 和 `NewJWTVerifier(publicKey)` 已强制 RS256。验收条件改为：（1）单元测试确认传入 nil key 时返回错误；（2）验证 JWTVerifier.Verify 拒绝 HS256 签名的 token；（3）access-core 三个 slice 的测试使用 RSA test key pair（FR-7.2 保持不变）。删除"保留 WithHS256 Option"要求。同步更新 tech-debt-registry P2-SEC-04 / P3-TD-09 状态为 RESOLVED。

---

### PM-07 [验收标准缺失] P1

**建议内容**: FR-7.4 要求 S3 `ConfigFromEnv()` 读取 `GOCELL_S3_*` 前缀的环境变量。查看当前代码（`src/adapters/s3/client.go` 第 41-51 行），`ConfigFromEnv()` 当前读取的是无前缀变量（`S3_ENDPOINT`、`S3_REGION` 等）。这个修改是 breaking change——任何已使用 `S3_ENDPOINT` 环境变量的消费者在升级后配置将失效。

spec 中没有为此 breaking change 提供迁移路径的验收条件，也没有说明是否需要双读（先读 `GOCELL_S3_*`，fallback 到 `S3_*`）。

**影响分析**: 虽然 Phase 4 仍为内部开发阶段（非目标声明中已排除 pkg.go.dev 发布），但 CLAUDE.md 的 API 版本策略明确要求"删除/重命名字段 -> 需要 v2"且"Deprecation 至少保留 2 个 Sprint"。环境变量名变更等同于配置 API 的 breaking change。

**建议修改方式**: 在 FR-7.4 添加迁移策略子项：

```
FR-7.4.1 向后兼容迁移:
  ConfigFromEnv() 优先读取 GOCELL_S3_* 前缀变量；
  若 GOCELL_S3_* 不存在，fallback 读取旧名 S3_*，并输出 slog.Warn 提示迁移。
  .env.example 更新为 GOCELL_S3_* 前缀。
```

添加验收条件：Given 环境中仅设置旧 `S3_ENDPOINT`, When 调用 `ConfigFromEnv()`, Then 正常返回 Config 且日志包含 deprecation 警告。

---

### PM-08 [开发者体验] P1

**建议内容**: spec FR-5.6 定义 Grafana Dashboard 模板为 JSON 文件（`templates/grafana-dashboard.json`），但没有说明 dashboard 如何导入 Grafana、需要哪些数据源（Prometheus? VictoriaMetrics? Loki?）、面板的 metric 名称是否与框架的实际 metric 输出对齐。product-context 非目标声明中明确说"VictoriaMetrics adapter Phase 4 不补实现，以 InMemoryCollector 替代"。

**影响分析**: Tech Lead（persona P3）拿到 Grafana dashboard JSON 后，如果面板引用了 `gocell_outbox_lag_seconds` 等 metric 名称，但框架当前只有 InMemoryCollector 而无实际 metric 导出，dashboard 导入后所有面板将显示 "No Data"。模板的实用价值为零，反而会造成"框架可观测性不成熟"的负面印象。

**建议修改方式**: 两种方案二选一：
- (A) 降级方案：FR-5.6 改为 Grafana dashboard 模板 README，描述推荐的面板结构和 metric 命名规范，附 JSON 骨架和 `// TODO: replace with actual metric names` 注释，标注"需配合 Prometheus exporter adapter 使用"。
- (B) 删除方案：鉴于无实际 metric 导出，将 FR-5.6 从 Phase 4 范围移除，在 templates/ 目录中用 `grafana-dashboard-placeholder.md` 说明计划，DEFERRED 到有 metric exporter 时实现。

---

### PM-09 [范围偏移] P2

**建议内容**: spec FR-3.1 要求 device-cell 的 type 为 "edge"，但 CLAUDE.md 一致性等级表只定义了 L0-L4 五个等级和场景说明，未提及 Cell type "edge"。查看 phase-charter 描述 iot-device 演示 "L4 DeviceLatent 一致性"。如果 "edge" 是一个新的 Cell type（不同于现有的 "core"），spec 需要说明 kernel 层是否需要扩展来支持此类型，否则示例代码中的 `type: edge` 在 `gocell validate` 时可能校验失败。

**影响分析**: 如果 kernel 的 Cell type 校验只允许 "core" 等已知类型，FR-3.1 的 `type: edge` 会导致校验错误。这不是示例代码的问题，而是需要 kernel 层配合扩展。如果需要 kernel 改动，这超出了 Phase 4 "examples + docs" 的范围定义。

**建议修改方式**: 确认 kernel `cell.yaml` 校验是否支持 `type: edge`。如果已支持（开放枚举），在 spec 中注明。如果未支持，有两个选项：
- (A) 将 device-cell 改为 `type: core, consistencyLevel: L4`，避免 kernel 改动
- (B) 在 FR-3 中明确增加子项"kernel/cell 扩展 type: edge 枚举值"，并评估对现有 API 的影响

---

### PM-10 [验收标准缺失] P2

**建议内容**: spec 成功标准 SC-2 要求"3 示例可编译可运行"，验证方式为 `go build`。但 product-context S2 的完整指标是 "每个示例有 README.md 说明运行步骤；docker compose up -d && go run . 可启动并响应 HTTP 请求"。spec 的 SC-2 只用 `go build` 验证，缺少 `go run .` + HTTP 响应的自动化验收手段。

此外，spec 没有定义示例项目 HTTP 端口的分配策略。如果三个示例都默认监听 `:8080`，评估者无法同时运行多个示例（端口冲突）。README 也需要说明端口配置方式。

**影响分析**: 编译通过不等于可运行。示例可能编译成功但 `go run .` 时因 adapter 初始化失败、端口冲突、缺少环境变量等原因 panic。如果 Gate 验证只用 `go build`，会遗漏运行时问题。

**建议修改方式**:
1. SC-2 的验证方式扩展为：`go build ./examples/...` PASS + 每个示例 `docker compose up -d && go run . &` 后 `curl localhost:{port}/healthz` 返回 200。
2. 在 FR-1/FR-2/FR-3 中定义默认端口分配（如 sso-bff: 8081, todo-order: 8082, iot-device: 8083），或统一通过 `PORT` 环境变量配置并在 README 中说明。

---

## 评审维度总结

| 维度 | 评级 | 说明 |
|------|------|------|
| A. 验收标准覆盖率 | YELLOW | P1 用户场景（US-1/US-2/US-3）的验收条件不够细致：事件消费未验证、30 分钟无量化手段、fail-fast 无 AC |
| B. UI 合规检查 | N/A | GoCell 为纯后端框架，无 UI 交付物 |
| C. 错误路径覆盖率 | YELLOW | fail-fast（RS256 / outboxWriter）缺少验收条件；降级运行（in-memory）未具体化为 FR 子项 |
| D. 文档链路完整性 | GREEN | README Getting Started + 示例 README + godoc + CHANGELOG + 能力清单，链路完整 |
| E. 功能完整度 | YELLOW | RS256 现状与 spec 描述有偏差（PM-06）；Grafana dashboard 无实际 metric 支撑（PM-08） |
| F. 成功标准达成度 | YELLOW | S1/S2/S4 的验证方式过于粗放，需细化验收条件才能在 Gate 时客观判定 |
| G. 产品 Tech Debt | GREEN | 15 条活跃债务中 8 条纳入 Phase 4，4 条合理 DEFERRED，处置策略清晰 |

---

## 优先级汇总

| 优先级 | 编号 | 核心问题 |
|--------|------|---------|
| P0 | PM-01 | US-1 缺少事件消费验证的验收条件 |
| P0 | PM-02 | RS256 / outboxWriter fail-fast 无验收场景 |
| P1 | PM-03 | FR-4.4 "go get" 与实际 "git clone" 体验不符 |
| P1 | PM-04 | US-2 的 30 分钟时间约束无量化验证方法 |
| P1 | PM-05 | In-memory 降级运行未具体化为 FR 子项 |
| P1 | PM-06 | FR-7.1 RS256 描述与代码现状矛盾，WithHS256 是范围扩大 |
| P1 | PM-07 | S3 环境变量前缀变更是 breaking change，缺迁移路径 |
| P1 | PM-08 | Grafana dashboard 模板无实际 metric 支撑 |
| P2 | PM-09 | Cell type "edge" 可能需要 kernel 扩展，超出 Phase 4 范围 |
| P2 | PM-10 | SC-2 验证方式过于粗放；示例端口分配未定义 |

---

## 结论

**产品判定: CONDITIONAL PASS**

spec 的整体设计方向正确，persona 覆盖完整，scope 边界清晰。但 2 条 P0 建议（PM-01 验收条件缺失、PM-02 安全 fail-fast 无 AC）必须在 spec 定稿前修复。6 条 P1 建议中，PM-06（RS256 现状偏差）和 PM-07（S3 breaking change 迁移）影响实现方向，建议优先处理。

修复 P0 + P1 后可进入实现阶段。
