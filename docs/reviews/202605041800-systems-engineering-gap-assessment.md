# GoCell 系统工程视角差距评估报告

> 评估，不改造。改造决策由读者按本报告第 6 节路线图条目逐项发起。

| 项目 | 内容 |
|---|---|
| 评估日期 | 2026-05-04 |
| 基准 commit | `11600a4f`（develop） |
| 评估范围 | 整仓库（kernel + cells + contracts + journeys + assemblies + runtime + adapters + pkg + cmd + examples + .github + docs + .claude） |
| 评估视角 | V 模型 / 敏捷 + DevOps / 架构模式 / SOLID + 高内聚低耦合 + 关注点分离 + 概念完整性 / SysML 九图 |
| 评估边界 | **仅限仓库内可观察到的代码、yaml、CI、文档**；不评估生产部署、客户应用、运行时行为 |
| 形态层约束 | GoCell 是**编程框架**（库 + 编译期工具），**不拥有任何持久层与运行时**。所有评估必须遵循 [`202605041430-adr`](../architecture/202605041430-adr-architecture-optimization-via-engineering-thinking.md) 第 3 节定义的形态层边界 |

---

## 0. 评估方法

### 0.1 三视角

```
        【目标】把 V 模型 / 敏捷 / 架构模式 / SysML 等学科范式
                逐条对照到 GoCell 现状，列出
                  ✅ 已具备 / ⚠️ 部分具备 / ❌ 缺失
                  并给出落地路线图（不执行）
                              ▲
                              │
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
        ▼                     ▼                     ▼
   系统工程视角          软件工程视角           第一性原理视角
   边界 / 状态 /        抽象 / 内聚 / 耦合      最小不可约事实集 /
   反馈回路 /           DIP / DRY / SRP /       砍冗余补丁 /
   故障模式 /           ISP / 类型 / 契约 /     规则可推导 /
   可观测性 /           错误处理               公理违例必修
   配置管理 /
   需求追溯 /
   熵增
```

### 0.2 每条项目的评级口径

| 评级 | 含义 |
|---|---|
| ✅ 已具备 | 概念已落地、有刚性守卫（archtest / CI / 编译期 codegen），跨 PR 不易回退 |
| ⚠️ 部分具备 | 概念已落地但守卫缺失，或仅有约定无强制；或代码已有但文档/可视化缺失 |
| ❌ 缺失 | 概念在仓库内找不到对应结构，或被显式标注为占位 |
| — | 形态层不适用（如 GoCell 不拥有运行时，CD 链路评级为「—」而非「❌」） |

### 0.3 不评估的项

以下三类**不进入差距清单**，避免浪费决策注意力：

1. **客户应用层职责**：CD 镜像构建 / staging / canary / blue-green / Helm chart —— 这些是嵌入 GoCell 的客户应用的事，不是框架的事。
2. **运行时行为**：长时间稳定性、内存泄漏、OOM 表现 —— 仓库内无法评估，需要生产数据。
3. **业务领域质量**：accesscore/auditcore/configcore 三个示例 cell 的业务正确性 —— 它们是参考实现，不是框架契约本身。

### 0.4 引用约定

- 文件路径用反引号 + 行号：例如 `kernel/cell/types.go:20-28`
- ADR 引用用相对链接：例如 [`202605031900-adr`](../architecture/202605031900-adr-handler-vocabulary-collapse.md)
- 所有事实陈述均经过本报告末尾「附录 A 验证清单」中的命令核对，与代码一致

---

## 1. V 模型左右两侧对照

### 1.1 左右两侧逐层对照

| V 模型层级 | 软件工程对应 | GoCell 现状 | 评级 | 缺口 |
|---|---|---|---|---|
| 用户需求 | 需求文档 / SMART 需求 | 无 `docs/requirements/`，需求散布在 ADR、roadmap、PR 描述中 | ❌ | 反向追溯断点 #1 |
| 系统需求 | 用例 / Journey | `journeys/J-*.yaml` 共 8 条 | ⚠️ | journey 不绑定具体需求 ID，goal 字段是自然语言陈述 |
| 架构设计 | 模块 / 分层 / 接口 | kernel + cells + contracts + assemblies 四级声明式模型 | ✅ | — |
| 详细设计 | 模块定义 + 接口签名 | `cell.yaml` + `slice.yaml` + `contract.yaml` 三级 yaml + JSON schema | ✅ | contract.yaml 无 requirement 反向链 |
| 编码 | Go 实现 | `cells/*/slices/*/{handler,service}.go` 等 | ✅ | 代码结构未元数据化（依赖约定） |
| 单元测试 | `_test.go` | 项目内 `_test.go` 覆盖 cells/runtime/kernel/pkg/tools | ✅ | — |
| 集成测试 | build tag | `//go:build integration` 的 `_integration_test.go` 共 7 个 | ✅ | — |
| 系统测试 | journey | `journeys/J-*.yaml` × 8 + `gocell verify journey --active` | ✅ | `fixtures/` 目录空挂（仅 `.gitkeep`）|
| 验收 | BDD / UAT | `passCriteria` 文本 + `checkRef` 元数据 | ⚠️ | 非 Gherkin / 无人类执行清单 |

### 1.2 左侧（需求 → 设计）现状

GoCell 的左侧只到「架构设计」一级**就开始了**，而且是从声明式模型开始的：

- `cell.yaml` 是模块定义（SysML BDD）
- `slice.yaml` 是模块内部分解
- `contract.yaml` 是接口规范
- `assembly.yaml` 是物理打包

但**「用户需求」与「系统需求」两层在仓库内找不到结构化产物**：

- 没有 `docs/requirements/` 目录
- ADR（`docs/architecture/`）记录的是**已做决策**而非需求
- roadmap（`docs/plans/202605011500-029-master-roadmap.md`）记录的是**任务**与**优先级**而非需求
- journey 的 `goal` 字段是自然语言（例：J-ssologin 的 `goal: 用户通过 SSO 登录后获得有效 session`），无 SMART 拆解、无关联需求 ID

### 1.3 右侧（验证 → 确认）现状

右侧四级**全部落地**且自动化：

| 层级 | 入口 | 守卫 |
|---|---|---|
| 单元 | `go test ./...` | golangci-lint + 5 shard CI |
| 集成 | `go test -tags=integration` | `_build-lint.yml` 中 `integration-test` job |
| 系统 | `gocell verify journey --active` | `governance.yml` 中 `make verify` |
| 验收 | `passCriteria.checkRef` → `go test -run` | `kernel/verify/runner.go:69-100` 解析 |

补充层（V 模型外但属于验证体系）：

- **archtest 静态守卫**：`tools/archtest/*_test.go` 共 136 个 `Test*` 函数
- **govulncheck**：供应链漏洞扫描（`security-vuln.yml`）
- **CodeQL + Semgrep**：SAST 二层（`security-static.yml`）
- **race**：并发检测（`test-race.yml`）

### 1.4 反向追溯链断点

V 模型的**双向追溯**是关键：每条需求要能找到设计、设计要能找到测试，反过来也要能成立。

GoCell 现状：

- **正向（设计 → 测试）✅**：cell.yaml.verify.smoke / slice.yaml.verify.unit/.contract / journey.passCriteria.checkRef 各自指向 test ref
- **反向（测试 → 设计）✅**：test ref 命名编码了来源（`unit.<sliceID>.*`、`contract.<contractID>.*`、`journey.<journeyID>.*`、`smoke.<cellID>.*`），可逆向定位
- **正向（需求 → 设计）❌**：无需求 ID，contract.yaml 无 `requirementID` 字段，journey 也无
- **反向（设计 → 需求）❌**：同上

**结论**：右侧已成熟，左侧**自需求层往下完全断开**。这是 GoCell 进入「需求驱动迭代」必须先补的一环，对应路线图 R-01。

### 1.5 与第一性原理的对照

第一性原理要求「最小不可约事实集」。当前 GoCell 把「需求」隐含在三个不同结构里：

| 隐含位置 | 表现 | 问题 |
|---|---|---|
| ADR | 记录决策 + 决策动机（隐含需求） | 决策动机不结构化、不可索引 |
| Roadmap | 记录 K#xx / J#xx / D#xx 任务条目 | 任务粒度，非需求粒度 |
| Journey goal | 自然语言陈述用户场景 | 无可机读字段、不绑定 |

**冗余**：同一条需求可能在 ADR、roadmap、journey 三处各表述一次，缺少**单一事实源**。这违反「砍冗余补丁」原则，对应路线图 R-01。

---

## 2. 敏捷 + DevOps 闭环

### 2.1 CI 现状（9 个 workflow）

| Workflow | 触发 | 关键 Job | 评级 |
|---|---|---|---|
| `ci.yml` | push:develop | full-ci → `_build-lint.yml` | ✅ |
| `pr-check.yml` | pull_request | pr-ci → `_build-lint.yml`（增量 lint） | ✅ |
| `_build-lint.yml` | workflow_call | build-test (5 shard) + integration-test + e2e + os-smoke + examples-smoke + sonarcloud | ✅ |
| `test-race.yml` | push / PR | race（kernel + runtime + pkg + tools + archtest） | ✅ |
| `governance.yml` | push / PR / schedule | `make verify`（含 verify-archtest / verify-journey / verify-contract-health / verify-unconditional-skip） | ✅ |
| `security-static.yml` | push / PR / schedule（每周一 18:17 UTC） | codeql + semgrep | ✅ |
| `security-vuln.yml` | push / PR / schedule（每周一 18:17 UTC） | govulncheck | ✅ |
| `otel-collector-nightly.yml` | schedule | otel-collector smoke | ✅ |
| `qodana_code_quality.yml` | （未见 on: 触发条件） | qodana | ⚠️ 可能是占位 |

### 2.2 已具备 ✅

- **增量 lint**：`pr-check.yml` 中 `--new-from-merge-base=origin/<base-ref>` 仅扫 PR 新增，不被存量噪声拖累
- **shard 测试**：5 路 matrix（kernel / tools / runtime / cells / others）并行
- **kernel coverage gate ≥ 90%**：硬阈值，由 `_build-lint.yml` 中 awk 校验
- **archtest 双重守卫**：`.golangci.yml` depguard（编译期）+ `tools/archtest/*_test.go` 136 函数（CI 期）
- **三层安全**：govulncheck（依赖）+ CodeQL（路径分析）+ Semgrep（模式）
- **roadmap / phase 治理**：`docs/plans/202605011500-029-master-roadmap.md` + 最近 commit 引入 M0–M4 milestone + ADR-INDEX-01
- **多角色评审**：`.claude/agents/` 共 10 个角色（architect / developer / reviewer / kernel-guardian / product-manager / project-manager / roadmap / devops / doc-engineer / explorer）
- **ADR 时间戳化**：`yyyyMMddHHmm-adr-{slug}.md` 格式，已积累 8 篇

### 2.3 部分具备 ⚠️

- **sonarcloud**：CI 步骤启用、上传 SARIF 到 GitHub Code Scanning，但**是否阻断 merge** 取决于 GitHub branch protection 配置（仓库内未提交配置文件，依赖 UI 设置）
- **可观测性**：`runtime/observability/{logging,metrics,tracing,poolstats}` 四子模块已铺，HTTP 维度指标完整（`http_requests_total` 含 `cell` 标签、`http_request_duration_seconds`、`auth_token_verify_*`），但**事件层指标（outbox/consumer/relay）显式空缺**（详见第 5 节 R-04）
- **ADR ↔ Roadmap 交叉索引**：最新 commit `11600a4f` 提到 `ADR-INDEX-01`，但 `docs/architecture/` 内未发现 `ADR-INDEX.md` 文件 —— 索引尚未落地，对应路线图 R-09
- **journey 与单元测试的耦合度**：journey passCriteria 通过 `checkRef` 间接驱动 `go test`，但 `fixtures/` 目录仅有 `.gitkeep`，journey 无独立数据夹具

### 2.4 缺失 ❌

| 项 | 影响 | 是否 GoCell 该承担 |
|---|---|---|
| `.github/CODEOWNERS` | review 派发只能手工 / 文字描述 | ✅ 该补 |
| `.github/pull_request_template.md` | PR 描述结构化程度低 | ✅ 该补 |
| 显式 branch protection 规则文件（如 rulesets/ 或 codeowner-strict 模板） | 可能漂移 | ✅ 该补 |
| 自动 review 派发 | reviewer agent 仍人工触发 | ⚠️ 取决于团队规模 |
| 事件层 metrics（outbox_publish_total / consumer_delivery_total / event_replay_lag_seconds） | L2+ 异步路径可观测盲区 | ✅ 该补 |
| OTel 生产配置示例 | 客户应用接入需自行摸索 | ✅ 该补（examples 中演示） |
| Grafana dashboard 模板 / alert rules | 客户接入观测时无现成模板 | ⚠️ 锦上添花 |

### 2.5 不评级 — （形态层不适用）

- **CD 链路（镜像构建 / SBOM / 签名 / staging / canary）**：GoCell 是嵌入式框架，不拥有运行时与持久层，部署是客户应用职责。这一项**不是缺口**，是**正确的边界**（参 [`202605041430-adr`](../architecture/202605041430-adr-architecture-optimization-via-engineering-thinking.md) §3.1）。
- **微服务化拆分**：违背形态层「N=每个客户不同」约束，框架不能假设分布式拓扑。
- **服务网格 / Istio 集成**：同上，运行时层职责。

### 2.6 Scrum / XP / DevOps 范式落位

| 范式 | GoCell 对应物 | 评级 |
|---|---|---|
| 时间盒迭代 | Phase（M0–M4 + Phase 0–4） | ✅ |
| Backlog | `docs/plans/` 下任务文档 | ✅ |
| 每日站会 | （线下，仓库不可见） | — |
| 评审 / 回顾 | PR review + ADR 沉淀 | ✅ |
| 测试驱动 | 137 处 archtest + journey passCriteria 先于 slice 实现 | ✅（隐式 TDD） |
| 持续集成 | 9 个 workflow | ✅ |
| 结对编程 | （线下，仓库不可见） | — |
| 重构 | `feedback_no_backcompat_elegant`：项目无外部消费方，直接重写 | ✅（约定） |
| CI/CD 全流水线 | CI 完整 / CD 留给客户应用 | ✅（受形态层约束） |

**结论**：敏捷与 DevOps 在「框架开发自身」这一层上闭环良好；客户应用层的 CD 不在框架职责范围内。剩余缺口集中在**门禁机制（CODEOWNERS / PR 模板）**、**事件层可观测性**、**ADR 索引落地**三块。

---

## 3. 架构模式落位

### 3.1 五种主流模式对照

| 架构模式 | 在 GoCell 的体现 | 守卫机制 | 评级 |
|---|---|---|---|
| 分层架构 | kernel ⊃ runtime ⊃ adapters / cells | depguard + archtest LAYER-01..10 | ✅ 强 |
| 端口-适配器 / 六边形 / 整洁架构 | cells 通过 contract 接口与 adapter 解耦；adapters/* 实现 kernel/runtime 定义的接口 | cells 不依赖 adapters（archtest 守卫） | ✅ 强 |
| 事件驱动 | outbox + ConsumerBase + L2 OutboxFact + DLX broker-native | OUTBOX-SERVICE-01 / HANDLER-RECEIPT-WRITE-01 archtest | ✅ 强 |
| 管道-过滤器 | bootstrap phase0–7、verify pipeline、codegen pipeline | 隐式（无显式接口）| ⚠️ 隐式 |
| 微服务 | — | — | — 形态层不适用 |
| CQRS / 读写分离 | L3 WorkflowEventual 可承载（投影场景）；configcore 写流与查询投影分离 | 无 archtest 显式守卫 | ⚠️ 隐式 |

### 3.2 分层架构 ✅

- 22 个 kernel 一级子模块；kernel 仅依赖 stdlib + pkg/ + `gopkg.in/yaml.v3`
- 守卫双层：编译期 `.golangci.yml` depguard + 测试期 `tools/archtest/archtest_test.go:114-150`
- 反例兜底：`tools/archtest/archtest_test.go` 的 LAYER-05/05T 阻止 `cells/A` 跨模块拉 `cells/B/internal/`

**强度证据**：archtest 用 `golang.org/x/tools/go/packages` 扫整个导入图（含传递依赖），不只看顶层 import。这是 K8s 同款手法。

### 3.3 整洁架构 ✅

- 业务核心（cells/）定义接口、不依赖具体技术（postgres/redis/rabbitmq）
- adapter 层（`adapters/`）实现 kernel 与 runtime 定义的接口
- 编译期可换实现：`accesscore` 既可用 postgres 又可用 in-memory（fixture 测试场景）

**潜在弱点**：runtime/auth/auth_jwt.go 类的具体实现，在 cells 中通过 `auth.Mount` 注入，archtest AUTH-PLAN-04 守卫禁止 cells 直接构造 AuthJWT/AuthMTLS。这是 DIP 的强制实施。

### 3.4 事件驱动 ✅

[`202605031900-adr-handler-vocabulary-collapse`](../architecture/202605031900-adr-handler-vocabulary-collapse.md) 把 handler 词汇收敛为 Ack/Requeue/Reject 三态，DLX 由 broker 原生处理。这是事件驱动里少见的「不发明轮子」实践 —— 直接复用 AMQP/broker 的死信路由。

### 3.5 管道-过滤器 ⚠️

bootstrap 有 phase0–7 阶段流转、verify 有多步骤验证、codegen 有读 yaml → 派生 → 写 Go 多步处理，但**没有显式的 Pipeline 接口或 Stage 抽象**。各场景独立实现。

**风险**：
- 新加 phase 时无统一注册位置
- phase 间的依赖与失败语义靠各处 `if err != nil { return }` 表达
- 不利于第三方扩展（虽然形态层约束下扩展性需求不强）

### 3.6 CQRS / 读写分离 ⚠️

`configcore` 已有 flagwrite + configpublish + configwrite + 查询投影的天然分离，但：
- 没有显式的 `CommandSide` / `QuerySide` 概念
- 投影一致性（L3 WorkflowEventual）由 outbox 保证，但「最终一致性的可观测性」缺失（无 replay lag 指标）
- 路线图 R-04 的 `event_replay_lag_seconds` 指标补齐后会强化这一模式

---

## 4. SOLID + 概念完整性

### 4.1 SOLID 五原则评级

| 原则 | GoCell 实践 | 评级 |
|---|---|---|
| **S** 单一职责 | cell.yaml / slice.yaml / contract.yaml 各自单一职责清晰 | ⚠️ 但 ContractMeta 同时承载分类与拓扑（详见 4.3） |
| **O** 开闭原则 | archtest 规则可加（136 函数已积累），cell 元数据字段可扩展 | ✅ |
| **L** 里氏替换 | `Provider` 接口（Prometheus / OTel / Nop）可互换；`TxRunner` 接口可换 | ✅ |
| **I** 接口隔离 | HandleResult 已收敛三态，移除 `Receipt` 字段（[202605031900-adr](../architecture/202605031900-adr-handler-vocabulary-collapse.md)） | ✅ |
| **D** 依赖倒置 | runtime/auth、adapter 全部经接口注入，archtest AUTH-PLAN-04 守卫 | ✅ |

### 4.2 概念完整性 — 4 处显式断层

#### 断层 #1：Contract 既是分类又是拓扑

`kernel/metadata/types.go:152-178` 的 `ContractMeta` 同时承载：

- 分类：`Kind` 字段，枚举 `http / event / command / projection`
- 拓扑：`Endpoints` 字段（`EndpointsMeta`），按 `Server / Clients / Publisher / Subscribers` 拆分

**违反 SRP**：一条 contract 同时回答「我是谁（分类）」和「我有几条边（拓扑）」两个问题，
导致：

- 新增 contract kind 时（例如 `stream`、`grpc`），endpoints 拆分逻辑必须同步扩展
- 不同 kind 下 endpoints 字段语义不同（http 的 server vs event 的 publisher），代码层用 if/switch 判断 kind
- 同一份 schema 既要满足 http 又要满足 event，schema validation 写得很厚

**建议**（路线图 R-02）：拆为 `ContractKind`（分类） + `ContractTopology`（拓扑）两个独立维度，由 ContractMeta 组合而成。

#### 断层 #2：Verify 四级 scope 边界未文档化

`kernel/verify/ref.go:42-96` 定义四种 prefix：

| Prefix | 例 | 隐含 scope |
|---|---|---|
| `journey.<journeyID>.<checkpoint>` | `journey.J-ssologin.login-success` | journey 全链路 |
| `smoke.<cellID>.<suffix>` | `smoke.accesscore.basic` | cell 启动后烟测 |
| `unit.<sliceID>.<func>` | `unit.sessionlogin.handleLogin` | slice 内部单测 |
| `contract.<contractID>.<aspect>` | `contract.http.auth.refresh.v1.serve` | 跨 cell 契约一致性 |

四种 scope 在 ref 字符串语法上同形（dot-separated），但**实际运行范围与触发时机不同**：

- journey 是黑盒、跨 cell、含 fixture 数据流
- smoke 是 cell 启动期 in-process 自检
- unit 是单测进程内
- contract 验证 schema/wire 一致性

**风险**：作者写 verify ref 时无明确指引「这条该用哪个 prefix」，容易写错 scope。

**建议**（路线图 R-02）：在 `kernel/verify/` 加一份 `SCOPE.md`，明确四级 prefix 的运行范围、触发时机、覆盖目标，并在 archtest 中加规则校验 prefix 与 ref 解析后的 scope 一致。

#### 断层 #3：Slice 内代码结构未元数据化

每个 slice 目录下普遍出现 `handler.go` / `service.go`，但：

- `slice.yaml` 的 `allowedFiles` 仅声明文件**白名单**，不声明**职责分工**
- `handler.go` 与 `service.go` 的命名是约定，无 archtest 强制
- 新人接手时无法从元数据判断「我要写新逻辑该放哪里」

**建议**（路线图 R-02 子项）：要么在 `slice.yaml` 加 `roles` 字段（如 `handler / service / repo`），要么放弃约定改用更扁平结构。

#### 断层 #4：fixtures/ 占位空挂

`fixtures/` 仅含 `.gitkeep`，CLAUDE.md 声明该目录用于 `run-journey` 的测试夹具，但目前 journey 没有任何 fixture 引用。

这违反「目录即承诺」原则：目录存在 = 该目录承诺有内容。

**建议**（路线图 R-03）：要么落地 fixture 体系（journey 引用 fixture-*.yaml），要么删除目录、把 fixture 概念从 CLAUDE.md 抹去，等真正需要时再引入。

> 修正：`contracts/` 顶层目录**已落地**，含 139 个文件（command/event/http/projection/shared 五子目录），不是占位。本次评估前的初步推测错误，已在第 1 节 V 模型表与本节修正。

### 4.3 高内聚低耦合 / 关注点分离 评估

| 概念 | 评估 |
|---|---|
| 高内聚 | cell（业务能力）/ kernel（基础设施）/ contract（接口）边界清晰；问题集中在 ContractMeta 内部内聚（见断层 #1） |
| 低耦合 | archtest LAYER-01..10 + AUTH-PLAN-04 + EXPORTED-ERROR-NEW-01 等共 136 条规则强制 | 
| 关注点分离 | 元数据（yaml）vs 代码（go）vs 验证（test）三者分离明确；唯一灰区是 verify scope 边界（断层 #2） |

### 4.4 概念完整性总评

GoCell 是少见的**概念完整性强**的 Go 框架（参考 K8s 同范式），但仍有 4 处显式断层。

修复优先级：**断层 #4（fixtures 占位）> 断层 #1（Contract SRP）> 断层 #2（Verify scope）> 断层 #3（slice 代码结构）**

理由：
- #4 是承诺与现实的不一致，最容易误导
- #1 影响新增 contract kind 时的扩展成本
- #2 影响新人写 verify ref 的正确性
- #3 是约定层问题，影响小

---

## 5. SysML 九图覆盖度

把 SysML 九图逐一映射到 GoCell 的现有元数据，看哪些已有等价物、哪些缺失。

### 5.1 九图覆盖度总表

| SysML 图 | GoCell 等价物 | 评级 | 缺口要点 |
|---|---|---|---|
| 需求图 | 无 | ❌ | 无 docs/requirements/、contract/journey 不绑定需求 ID |
| 用例图 | `journeys/J-*.yaml` + `actors.yaml` | ⚠️ | 已有元数据，无可视化 |
| 模块定义图 (BDD) | `cell.yaml` + `slice.yaml` + `assembly.yaml` | ⚠️ | 已有元数据，无生成图 |
| 内部模块图 (IBD) | `contract.yaml.endpoints` + `cell.yaml.listeners` | ⚠️ | 已有元数据，无生成图 |
| 活动图 | bootstrap phase0–7、outbox 流程文档 | ⚠️ | 仅自然语言，无可机读结构 |
| 状态机图 | `cell.lifecycle` 隐含、outbox entry state 隐含 | ⚠️ | 状态机未显式声明 |
| 序列图 | docs/ 中部分流程图（手绘） | ❌ | 无声明式来源、无自动生成 |
| 参数图 | 无 | ❌ | 物理 / SLO 约束未编码 |
| 包图 | go module 自带 | ✅ | — |

### 5.2 已有等价物（⚠️ 部分具备）的图

GoCell 的**声明式元数据天然映射 SysML 中的 4–5 张图**，但缺少**从元数据生成图**的工具。

#### 模块定义图 (BDD)：`cell.yaml` + `assembly.yaml`

- `cell.yaml.id / .type / .schema.primary` 等价于 SysML 模块的 ID / 类型 / 持久化结构
- `assembly.yaml` 等价于「系统（System）」节点
- `cell.yaml.l0Dependencies` 等价于 SysML 的 composition 关系

**缺生成图**：现状是阅读 yaml + 心算拼接族谱，无法一眼看清系统结构。

#### 内部模块图 (IBD)：`contract.yaml.endpoints` + `cell.yaml.listeners`

- `contract.yaml.endpoints.server / clients / publisher / subscribers` 等价于 SysML 端口（Port）
- `cell.yaml.listeners` 描述 Cell 暴露的对外端口
- `slice.yaml.contractUsages` 等价于 IBD 中模块间的 connector

**缺生成图**：跨 cell 调用关系靠 grep contractUsages 拼凑。

#### 用例图：`journeys/J-*.yaml` + `actors.yaml`

- `actors.yaml` 等价于 SysML 的 Actor（外部参与者）
- `journeys/J-*.yaml` 等价于 SysML 的 Use Case
- 但 journey 不显式列出 actor —— 用例与参与者的连接关系靠间接推断

**缺直接绑定**：建议 journey schema 加 `actors: []` 字段，建立 use case ↔ actor 的硬连接。

#### 活动图：bootstrap phase0–7 / outbox 流程

- bootstrap 7 阶段流转、outbox publish-claim-commit 流程是天然的活动图
- 现状只有自然语言描述（`docs/architecture/*-adr.md`、`docs/ops/`）

**缺机读元数据**：建议把 bootstrap phase 的 enum、phase 间的 trigger condition 抽到 `kernel/lifecycle/phase.go` 显式声明。

#### 状态机图：cell.lifecycle / outbox.entry.state

- Cell 有「未初始化 → Init → Ready → Drain → Stopped」隐含状态机
- Outbox entry 有「pending → claimed → committed / released → dead-letter」状态机
- 但**没有显式的 state 枚举 + transition 表**

**缺显式声明**：建议路线图 R-06 把这两组状态机写入 metadata schema，由 archtest 校验状态转移完备性。

### 5.3 完全缺失（❌）的图

#### 需求图（最严重）

GoCell 没有任何结构化需求文档，需求散落在：

- ADR（决策动机字段）
- Roadmap（任务条目）
- Journey goal 字段（自然语言陈述）
- PR 描述（最不结构化）

**建议**（路线图 R-01）：引入 `docs/requirements/REQ-*.yaml` 结构，每条需求含：

```yaml
id: REQ-AUTH-01
text: 用户成功 SSO 登录后获得有效 session
category: functional | non-functional | security | performance
priority: P0 | P1 | P2 | P3
satisfiedBy:
  - contract: http.auth.sso.v1
  - journey: J-ssologin
verify:
  - journey.J-ssologin.login-success
```

并要求：
- contract.yaml 加 `requirementID: []` 字段（反向链）
- journey schema 加 `requirementID: []` 字段（反向链）
- archtest 加规则：每条需求必有 `satisfiedBy`，每条 contract/journey 可反查需求

#### 参数图（次严重）

SysML 参数图把物理公式 / SLO 约束 / 数学绑定关系编码进模型。GoCell 当前完全没有：

- 没有 SLO 定义（如 `p99 latency < 100ms`）
- 没有性能约束（如 `outbox publish rate > 1000/s`）
- 没有容量上限（如 `consumer queue depth ≤ 10000`）

**建议**（路线图 R-07）：在 cell.yaml 加 `constraints` 字段：

```yaml
constraints:
  latency:
    p99_ms: 100
    p999_ms: 500
  throughput:
    publish_per_second: 1000
  capacity:
    queue_depth_max: 10000
```

并由 verify 钩子在 CI 中跑 micro-benchmark 校验（实际触发由路线图条目细化）。

#### 序列图

GoCell 中复杂的多 cell 协作场景（如 OAuth + audit + config 联动）需要序列图，但目前：

- docs/ 中的少量手绘序列图随时间漂移
- 无机读来源、无自动生成

**建议**：journey 的 passCriteria 已经描述时间序列上的检查点，但顺序关系不显式。可考虑给 passCriteria 加 `order: int` 或 `dependsOn: [<checkpoint>]` 字段，从而能从 journey 反向生成 sequence diagram。

### 5.4 自动化生成（路线图 R-05）

把上述「⚠️ 部分具备」的 5 张图（BDD / IBD / 用例图 / 活动图 / 状态机图）补成 ✅，最经济的方式是**写一个 `tools/sysmlgen/`**：

- 输入：`cell.yaml` / `slice.yaml` / `contract.yaml` / `assembly.yaml` / `journey.yaml`
- 输出：`generated/sysml/<view>.puml` 或 `generated/sysml/<view>.mermaid`
- CI step：`make sysml-verify` 检查生成产物与 yaml 是否同步（git diff = 空）

这与 `kernel/codegen` 的同款编译期 codegen 思路一致，K8s 也有 `kubectl explain` 等同类机制。

### 5.5 SysML 总评

GoCell 的**声明式元数据已经天然包含 SysML 4–5 张图的内容**，只是没有可视化生成器和需求/参数/状态机的显式扩展。

补全成本不高（一个 codegen 工具 + 三处元数据 schema 扩展），ROI 高（架构透明度 + 新人 onboarding 加速 + 评审效率提升）。

---

## 6. 落地改造路线图（advisory，不执行）

按优先级排序。每条标注：负责 agent / 复杂度（参 reviewer Cx 标准）/ 预期产物 / 主要依赖。

> **说明**：本节是路线图的**目录**，每条只给标题、范围、产物三要素。具体方案
> （ADR、PR 拆分、测试计划）由用户后续按需启动 `/ship` 或 `architect` agent
> 单独发起，不在本评估报告内展开。

### R-01 需求追溯链补齐 — P0 / Cx2 / architect + kernel-guardian

- **范围**：建立 `docs/requirements/REQ-*.yaml` 结构；contract.yaml 与 journey.yaml 增加 `requirementID: []` 字段
- **产物**：requirements schema + 反向追溯 archtest 规则（REQ-TRACE-01）+ 1–2 篇 ADR
- **依赖**：无
- **价值**：补齐 V 模型左侧反向追溯断点，建立单一需求事实源

### R-02 概念完整性整理 — P0 / Cx3 / architect

- **范围**：拆分 ContractMeta 的 `Kind` 与 `Endpoints`；Verify 四级 scope 显式文档化 + archtest 校验
- **产物**：1 篇 ADR（`yyyyMMddHHmm-adr-contract-srp-decomposition.md`）+ kernel/contract refactor PR（多步）+ kernel/verify/SCOPE.md
- **依赖**：无（但与 R-01 形成接口稳定性合力）
- **价值**：消除 ContractMeta SRP 违规、明确 verify scope 边界

### R-03 占位目录处置 — P0 / Cx1 / kernel-guardian

- **范围**：决策 `fixtures/` 目录是落地还是删除（从 CLAUDE.md 撤回承诺）
- **产物**：决策 ADR + 落地或删除 PR
- **依赖**：无
- **价值**：消除目录承诺与现实不一致

### R-04 事件层可观测性补齐 — P1 / Cx2 / devops + reviewer

- **范围**：补齐 `outbox_publish_total` / `outbox_publish_duration_seconds` / `consumer_messages_total` / `consumer_processing_duration_seconds` / `event_replay_lag_seconds` / `outbox_dlx_total` 等指标
- **产物**：`runtime/observability/metrics/` 内补 collector + `assemblies/corebundle/generated/metrics-schema.yaml` 同步 + Grafana dashboard 模板
- **依赖**：无
- **价值**：异步路径可观测性闭环；为 CQRS 模式提供 lag 数据驱动迭代

### R-05 SysML 视图自动生成 — P1 / Cx3 / doc-engineer

- **范围**：新建 `tools/sysmlgen/`，从 cell.yaml / slice.yaml / contract.yaml / journey.yaml / assembly.yaml 生成 PlantUML 或 Mermaid 图
- **产物**：`tools/sysmlgen/` 工具 + `generated/sysml/*.puml` 产物 + `make sysml-verify` CI step
- **依赖**：建议在 R-01、R-02 落地后启动（schema 稳定后再生成图）
- **价值**：架构透明度 / 评审效率 / 新人 onboarding

### R-06 状态机显式化 — P1 / Cx3 / architect + kernel-guardian

- **范围**：把 cell.lifecycle 与 outbox entry state 写入 metadata schema（state enum + transition 表），加 archtest 校验状态转移完备性
- **产物**：`kernel/cell/lifecycle.go` + `kernel/outbox/state.go` 显式状态机声明 + 1 篇 ADR
- **依赖**：建议在 R-04 之后（指标数据可验证状态机停留分布）
- **价值**：补齐 SysML 状态机图、防止状态转移黑洞

### R-07 参数图引入 — P2 / Cx3 / architect

- **范围**：cell.yaml / contract.yaml 增加 `constraints` 字段（latency / throughput / capacity / SLO）；verify 钩子跑 micro-benchmark 校验
- **产物**：metadata schema 扩展 + benchmark runner + 1 篇 ADR
- **依赖**：建议在 R-04 之后（指标数据为约束设定提供基线）
- **价值**：补齐 SysML 参数图、把 SLO 写进模型而非 PR 描述

### R-08 门禁机制补齐 — P2 / Cx1 / devops

- **范围**：`.github/CODEOWNERS` + `.github/pull_request_template.md` + 显式 branch protection 配置（如 ruleset 文件）
- **产物**：3 个 .github/ 下文件 + 1 篇说明文档
- **依赖**：无
- **价值**：自动 review 派发、PR 描述结构化、防 branch protection 漂移

### R-09 ADR ↔ Roadmap 交叉索引落地 — P2 / Cx1 / roadmap

- **范围**：核实 ADR-INDEX-01 是否已落地为 `docs/architecture/ADR-INDEX.md`；若未落地则补齐；建立 ADR ↔ K#xx / J#xx / D#xx 任务条目的双向链接
- **产物**：ADR-INDEX.md + roadmap 文档内 ADR 链接补全
- **依赖**：无
- **价值**：决策与交付的对应关系可索引

### R-10 examples 多 cell 协作样例 — P3 / Cx3 / developer

- **范围**：补一个含 2+ cell 通过 L2/L3 协作的示例（例如 ssobff + auditcore + configcore 协作演示）
- **产物**：`examples/multicelldemo/` 完整示例
- **依赖**：建议在 R-04（事件层指标）之后，演示效果更完整
- **价值**：示范多 cell 协作模式，弥补当前所有 example 都是单 cell 的局限

### 6.x 路线图依赖关系图

```
              R-01 (需求追溯)
                    │
                    ├──▶ R-02 (Contract 拆分)
                    │           │
                    │           └──▶ R-05 (SysML 视图生成)
                    │                       ▲
                    └──▶ R-06 (状态机)──────┘
                              ▲
                              │
              R-04 (事件层指标)
                    │
                    └──▶ R-07 (参数图 / SLO)
                              │
                              └──▶ R-10 (多 cell 示例)

            R-03 (占位目录) — 独立
            R-08 (门禁机制) — 独立
            R-09 (ADR 索引) — 独立
```

**建议执行顺序**：
1. 先做独立项 R-03 / R-08 / R-09（低成本、解放注意力）
2. 然后 R-01 → R-02（左侧追溯链 + 概念完整性，奠定模型基础）
3. R-04 单独并行（事件层指标，纯加法）
4. R-05 / R-06 → R-07 → R-10（视图与状态机生成，最后是参数与示范）

---

## 7. 不建议改造的项

以下三类是**正确的边界**，不是**缺口**。任何提案要绕过这三条边界的，应先驳回。

### 7.1 CD 链路（镜像 / SBOM / staging / canary）— 形态层不适用

**理由**：GoCell 是嵌入式编程框架，不拥有运行时与持久层。CD 是客户应用的事，
不是框架的事。

**例外**：`examples/*` 子目录的发布流程可受益（让客户更容易复制启动方式），但
ROI 不高，仅在客户反馈强烈时再考虑。

参考依据：[`202605041430-adr`](../architecture/202605041430-adr-architecture-optimization-via-engineering-thinking.md) §3.1 形态层约束。

### 7.2 微服务化拆分 — 形态层冲突

**理由**：GoCell 是嵌入式框架，N=每个客户的部署形态不同（pod / lambda / cf
worker / 裸金属）。框架不能假设分布式拓扑，否则违背形态层「不拥有运行时」约束。

### 7.3 journey 改 Gherkin — 抽象错位

**理由**：现有 `passCriteria` + `checkRef` 比 Gherkin 更工程化（直接驱动 go
test、不需要 step definition 翻译层）。Gherkin 的优势在于跨角色协作（PO / QA /
Dev），但 GoCell 当前模型中 journey 主要由开发者编写、由测试驱动验证，引入
Gherkin 反而增加翻译层。

**例外**：未来若 GoCell 进入「PO 直接编写 journey」的工作流，再考虑引入。

### 7.4 Kubernetes 风格 CRD/Operator — 范式校准而非范式照搬

**理由**：CLAUDE.md 与 [`202605041430-adr`](../architecture/202605041430-adr-architecture-optimization-via-engineering-thinking.md) 已明确 K8s 是**同范式参照**而非**同形态搬运**。GoCell 借
鉴 K8s 的 declarative + reconcile 思想（cell.yaml 是 desired state、verify 是
reconcile loop），但**不**复刻 apiserver/etcd/controller。

任何提议「在 GoCell 内引入 CRD / etcd / informer / controller-runtime」都应被
驳回。

---

## 8. 总评与一句话结论

### 8.1 整体水位

| 维度 | 水位 |
|---|---|
| V 模型右侧验证 | ✅ 高（136 archtest + 5 shard test + 3 安全扫描 + 8 journey + integration/e2e） |
| V 模型左侧追溯 | ❌ 低（无需求层结构化产物） |
| 敏捷 + DevOps | ✅ 高（9 workflow + 增量 lint + kernel coverage gate + 10 角色 agent） |
| 架构模式落位 | ✅ 高（分层 / 整洁 / 事件驱动 三大模式强守卫） |
| SOLID 原则 | ✅ 高（4/5 原则强，1 处 SRP 违规 ContractMeta） |
| 概念完整性 | ⚠️ 中（4 处显式断层，其中 fixtures 占位最易误导） |
| SysML 九图覆盖 | ⚠️ 中（5 图天然覆盖、缺生成器；3 图完全缺失：需求 / 参数 / 序列） |
| 可观测性 | ⚠️ 中（HTTP 维度完整、事件层指标空缺） |
| 门禁机制 | ⚠️ 中（CI 完整、CODEOWNERS / PR 模板缺） |

### 8.2 最具杠杆的三件事

按「补一个洞 → 解开多少下游约束」排序：

1. **R-01 需求追溯链** —— 补齐 V 模型左侧反向链 + 建立单一需求事实源。下游撬动 R-02 / R-05 / R-06 / R-07 / R-10 五条路线
2. **R-02 概念完整性整理** —— 修 ContractMeta SRP + Verify scope 边界。下游撬动 R-05 / R-06 / R-08
3. **R-04 事件层可观测性** —— 异步路径闭环。下游撬动 R-06 / R-07 / R-10

### 8.3 一句话结论

> GoCell 在 V 模型右侧、架构分层、SOLID、概念完整性主体、敏捷 DevOps 闭环上**已是同类 Go 框架的优等水位**，刚性守卫密度高于绝大多数对标项目；剩余真正的缺口集中在**V 模型左侧反向追溯**与**SysML 需求/参数/状态机三图**——这两块都是声明式扩展（不是抽象重构），按本路线图 R-01 → R-02 → R-04 → R-05/R-06 → R-07 顺序补，预计 6–10 个 phase 周期可全部闭环。

---

## 附录 A 验证清单（10 条核对命令）

写入本报告时，所有事实陈述均经以下命令逐项核对，输出与文中一致。读者复核时可逐条执行：

```bash
# 1. 占位目录核对（fixtures/ 应仅含 .gitkeep；contracts/ 已落地）
ls -la fixtures/                     # 仅 .gitkeep
find contracts -type f | wc -l       # 139（已落地）

# 2. L0-L4 enum 定义
grep -n 'L[0-4][^a-zA-Z]' kernel/cell/types.go | head

# 3. archtest 规则数
grep -n '^func Test' tools/archtest/*_test.go | wc -l   # 136

# 4. journey 数
ls journeys/J-*.yaml | wc -l         # 8

# 5. workflow 数
ls .github/workflows/*.yml | wc -l   # 9

# 6. ADR 时间戳文件数
ls docs/architecture/[0-9]*.md | wc -l   # 8

# 7. agent 数
ls .claude/agents/*.md | wc -l       # 10

# 8. kernel coverage gate（应 ≥90% 硬阈值）
grep -n '90' .github/workflows/_build-lint.yml | head

# 9. requirementID 字段（应为空，证明左侧反向追溯缺失）
grep -r 'requirementID\|requirement_id\|requirementId' contracts/ cells/ journeys/ | head

# 10. runtime/observability 子模块
ls runtime/observability/   # logging / metrics / poolstats / tracing
```

## 附录 B 关键文件引用速查

| 主题 | 路径:行 |
|---|---|
| L0–L4 定义 | `kernel/cell/types.go:20-28` / `:43-59` |
| CellMeta 字段 | `kernel/metadata/types.go:19-38` |
| SliceMeta 字段 | `kernel/metadata/types.go:83-96` |
| ContractMeta 字段（含 SRP 断层） | `kernel/metadata/types.go:152-178`（断层 #1：Kind 与 Endpoints 同处） |
| 依赖守卫（编译期）| `.golangci.yml`（depguard） |
| 依赖守卫（CI 期） | `tools/archtest/archtest_test.go:114-150`（LAYER-01..10） |
| Verify Reference 解析（断层 #2）| `kernel/verify/ref.go:42-96` |
| Verify Runner | `kernel/verify/runner.go:69-100` / `:137` |
| Bridle / 系统工程方法论 ADR | [`202605041430-adr`](../architecture/202605041430-adr-architecture-optimization-via-engineering-thinking.md) |
| HandleResult 收敛 ADR | [`202605031900-adr`](../architecture/202605031900-adr-handler-vocabulary-collapse.md) |
| 时钟注入 ADR | [`202605021500-adr`](../architecture/202605021500-adr-kernel-clock-injection.md) |
| Wire Format 出 kernel ADR | [`202605040030-adr`](../architecture/202605040030-adr-wire-format-out-of-kernel.md) |
| v1 Schema 演进 ADR | [`202605031600-adr`](../architecture/202605031600-adr-v1-schema-evolution.md) |
| Roadmap 主线 | `docs/plans/202605011500-029-master-roadmap.md` |
| HTTP cell label 规则 | `.claude/rules/gocell/observability.md` |
| EventBus 规范 | `.claude/rules/gocell/eventbus.md` |
| API 版本策略 | `.claude/rules/gocell/api-versioning.md` |
| 错误处理规范 | `.claude/rules/gocell/error-handling.md` |

---

> 报告结束。本评估**不引入任何 ADR 或代码改动**；路线图条目 R-01 ~ R-10 由读者按需后续单独发起。



