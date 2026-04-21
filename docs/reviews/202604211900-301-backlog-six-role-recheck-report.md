# Backlog 六角色代码复查报告（2026-04-21）

## 1. 审查范围与总体风险

- 审查输入：`docs/backlog.md`、`docs/backlog_later_detail.md`
- 审查方式：六席位并行（架构/安全/测试/运维/可维护性/产品），基于真实代码证据复查
- 总体结论：
  - 阻塞项仍存在（P0/P1）
  - 多项“条件延后”在生产前必须闭环
  - 文档与代码存在漂移，需同步清账避免误导后续阶段

总体风险评级：高（在进入下一阶段前至少需先清理阻塞项）

---

## 2. 合并后问题表（按严重级别）

| 严重级别 | 问题ID | 发现席位 | 证据 | 问题 | 根因 | 影响 | 处理建议 | 阻塞性 |
|---|---|---|---|---|---|---|---|---|
| P0 | L1 / AUDIT-ROUTE-POLICY-01 | 安全/架构/测试/产品 | `cells/audit-core/cell.go:241`, `cells/audit-core/slices/auditquery/handler.go:56` | 审计查询路由仍裸挂，策略未在真实注册链路强制绑定 | 路由注册与策略声明未形成单一门禁 | 非 admin 跨用户读取审计数据风险 | 将策略收口到 RegisterRoutes + 启动期强校验；补真实路由链路 401/403/200 测试 | 阻塞 |
| P1 | S-nonce / SERVICE-TOKEN-NONCE-STORE-01 | 安全/架构 | `runtime/auth/authenticator.go:130`, `cmd/core-bundle/main.go:347` | NonceStore 默认可空，生产 wiring 未强制注入 | 控制面安全能力是可选项而非 real 模式硬约束 | internal API 5 分钟窗口可重放，尤其多 pod 放大 | real 模式强制 NonceStore（Redis）+ 启动失败 | 生产前阻塞 |
| P1 | S4b / VAULT-TOKEN-STATIC-REAL-GUARD-01 | 安全/架构 | `adapters/vault/transit_provider.go:470` | real 路径仍允许静态 VAULT_TOKEN | Vault auth mode 未显式建模 | token 泄露后长期滥用，续期/最小权限不可控 | A14 同批落地 authMode，real 禁止 static token | 生产前阻塞 |
| P1 | L2 / ROUTE-POLICY-REGISTRY-01 | 架构/安全 | `runtime/bootstrap/bootstrap_phases.go:489` | 启动期无法验证“路由已注册但无策略声明” | 缺全局策略注册表和对账机制 | 同类安全回归可反复出现 | 新增 PolicyRegistry，启动期 Verify fail-fast | 阻塞 |
| P1 | L7 / EXAMPLES-STARTUP-SMOKE-01 | 测试/产品/架构 | `examples/sso-bff/main.go:100`, `examples/todo-order/main.go:37`, `.github/workflows/ci.yml:21` | examples 启动路径无 smoke 门禁，且 cursor key 字面量高风险 | CI 偏 build/test，缺运行态可用性检查 | 新用户首跑失败，DX 明显受损 | 引入 examples-smoke job + key 程序化构造 | 非阻塞（高优先） |
| P1 | L6 / CONTRACTTEST-MODEL-ALIGN-01 | 架构/可维护性/测试 | `pkg/contracttest/contracttest.go:91`, `kernel/metadata/types.go:158` | contracttest 与 metadata 模型双轨，扩展键可能丢失 | 共享 schema 类型未下沉到 pkg | 契约治理与测试结果不一致 | 抽 pkg/contracts 共享模型并补一致性测试 | 非阻塞（高优先） |
| P2 | A21 / HEALTH-CHECKER-CTX-BUDGET-01 | 运维/架构 | `runtime/http/health/health.go:21`, `runtime/http/health/health.go:146`, `kernel/lifecycle/managed_resource.go:23` | checker 串行无统一 budget，预算各自为政 | 健康检查接口过早固化为 `func() error` | /readyz 尾延迟叠加、误判就绪 | 升级为 `func(ctx) error`，聚合层统一 deadline + 并发 | 非阻塞 |
| P2 | L11 / GOVERNANCE-CI-MAINBRANCH-01 | 运维 | `.github/workflows/governance.yml:5`, `.github/workflows/ci.yml:5` | governance/CI 仅覆盖 develop，main/release 可绕过 | 门禁分支策略不一致 | 发布链路可携带治理缺陷 | workflow 扩展到 main/release/** + required checks | 非阻塞（发布前需做） |
| P2 | L8 / PAGINATION-HELPER-EXTRACT-01 | 可维护性 | `pkg/httputil/request.go:23`, `cells/audit-core/slices/auditquery/handler.go:122` | 分页错误处理逻辑多处重复且有重复日志 | 横切逻辑无组合入口 | 维护成本高、观测语义不一致 | 抽 `pkg/httputil` helper 统一处理 | 非阻塞 |
| P2 | F10 / TEST-JOURNEY-ASSEMBLY-HARNESS-01 | 测试/产品 | `tests/integration/journey_test.go:14` | 关键 journey 集成测试大量 `t.Skip` | full assembly harness 缺失 | 跨 cell 回归难以被 CI 提前发现 | 先恢复 2 条高价值旅程后逐步扩展 | 非阻塞 |

---

## 3. 根因问题簇（含数据流/调用链）

### 簇A：路由策略声明与执行断裂（P0/P1）

- 症状：策略函数存在但真实路由未强制绑定；启动期无全局校验
- 数据流：HTTP 请求 -> Router 注册路由 -> 进入 handler；若声明缺失则策略链条不生效
- 调用链：`Bootstrap.Run -> RegisterRoutes -> FinalizeAuth`，缺少“注册路由 vs 声明策略”对账环节
- 架构原因：路由声明是可选约束，不是启动不变量
- 影响范围：所有 HTTP 路由，尤其审计与 internal 控制面

### 簇B：控制面安全能力未 fail-closed（P1）

- 症状：ServiceToken 防重放可关闭；Vault real 仍可 static token
- 数据流：internal token 验签通过 -> 无 nonce 去重 -> 重放请求再次通过；Vault provider 直接读取 static token 运行
- 调用链：`InternalGuard -> ServiceTokenAuthenticator` 与 `main -> NewTransitKeyProviderFromEnv`
- 架构原因：生产安全能力被设计为 optional
- 影响范围：internal API、密钥管理链路、生产合规

### 簇C：治理模型与验证模型双轨（P1/P2）

- 症状：contracttest 与 metadata 对同一 schema 语义解析不一致
- 数据流：contract.yaml 分别进入 metadata 与 contracttest 两条链路 -> 扩展键处理不同
- 调用链：`kernel/metadata` 与 `pkg/contracttest` 独立结构定义
- 架构原因：缺共享契约类型的单一事实源
- 影响范围：契约验证可信度与演进安全

### 簇D：运行态门禁弱于编译门禁（P1/P2）

- 症状：examples 无启动 smoke；journey 大量 skip；readyz 无统一 budget
- 数据流：代码可编译并通过单测 -> 运行态初始化/探针阶段失败
- 调用链：`CI build/test` 未覆盖 `go run examples` 与全链路 journey
- 架构原因：质量门禁偏静态，动态可运行性缺位
- 影响范围：开发者体验、运维稳定性、回归防线

---

## 4. 席位分歧与处理

- 分歧点：internal 路由 policy 当前是否为真实安全漏洞
  - 观点A：当前 delegated guard 已接管，短期不构成直接绕过
  - 观点B：policy 常量与 `RoleInternalAdmin` 不一致，未来重构时易触发 403 或绕过
- 处理结论：按“防御性一致性”处理，列为 P2 改进项（L10），不作为当前阻塞项

---

## 5. 已解决但文档未同步项

| 项目 | 代码现状 | 文档漂移 |
|---|---|---|
| L4 / ID-VALIDATION-SINGLE-SOURCE-01 | `pkg/idutil/id.go` 已存在并被 runtime/kernel 复用 | backlog 仍按未完成表述 |
| AL-01 / Outbox Relay 拆分 | relay 已在 `runtime/outbox/relay.go` | later_detail 仍按待拆分描述 |
| adapter t.Skip 统计 | 当前适配器侧 skip 数明显低于历史描述 | later_detail 历史数字未回填 |

---

## 6. 下一阶段准入条件（建议）

进入下一阶段前建议满足：

1. 关闭 P0：L1（审计路由策略）
2. 关闭生产前阻塞 P1：S-nonce、S4b
3. 建立防复发门禁：L2（PolicyRegistry）
4. 建立最小运行态门禁：examples-smoke + 2 条 journey 恢复
5. 回填文档漂移：backlog 与 later_detail 状态同步

---

## 7. 输出说明

- 本报告为六席位并行复查后的合并版，不等同于单席位观点
- 涉及“最佳实践”相关建议已在独立对标报告中补充 3+ 项目证据
