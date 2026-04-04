---
title: 通用 Go Slice-Cell 底座 — 能力版图与演进方向
status: draft
owner: platform
audience: engineering
created: 2026-04-04
source_of_truth: false
related_specs:
  - 202604041700-521-go-foundation-final-plan
external_refs:
  - winmdm-mvp/docs/reviews/reports/202604041138-go-foundation-discussion/202604041200-531-generic-go-foundation-core.md
  - winmdm-mvp/docs/reviews/reports/202604041138-go-foundation-discussion/202604041148-530-go-foundation-capability-tiering-analysis.md
  - winmdm-mvp/docs/reviews/reports/202604041138-go-foundation-discussion/202604041144-529-round5-realtime-and-peripheral-capability-workshop.md
---

# 通用 Go Slice-Cell 底座 — 能力版图与演进方向

本文是底座持续完善的方向地图。所有能力按 4 层 + 时间线排列。

---

## 1. 能力全景

### v1.0 已确认（首批实现）

#### Kernel — 内核

| 编号 | 能力 | 说明 | 来源 |
|------|------|------|------|
| K01 | Cell/Slice/Assembly 运行时原语 | Cell/Slice Go 接口 + BaseCell/BaseSlice | 讨论确认 |
| K02 | cell.yaml / slice.yaml / contract.yaml / assembly.yaml | 元数据 schema + parser | 512-519 |
| K03 | Journey Catalog + journeys/*.yaml + Status Board | 五层信息模型 | 527 修订版 |
| K04 | L0-L4 一致性等级 | 定义 + 校验 | 516 |
| K05 | validate-meta | 元数据校验器 | 515 |
| K06 | generate-assembly | Go 代码生成 | 515 + 讨论 |
| K07 | select-targets | 改动 → 受影响 slice → 最小测试集 | 515 |
| K08 | verify-slice / verify-cell | go test 智能包装 | 515 + 讨论 |
| K09 | run-journey | 跨 cell journey 编排 + fixture | 515 |
| K10 | scaffolder | new-cell / new-slice / new-contract | 515 |
| K11 | contract registry | YAML 索引 + 查询 + 兼容性检查 | 515 |
| K12 | dependency checker | forbidden import / one-slice-one-cell / unregistered contract | 515 |
| K13 | caller trace | 静态 + 部署 + 运行时三层 | 515 |
| K14 | traced wrapper | sync call / event publish / command dispatch | 515 |
| K15 | config / logger / error model | 通用配置加载 + slog + errcode | Phase 1 |
| K16 | request/context ids | correlation_id / journey_id / cell_id | Phase 1 |
| K17 | lifecycle / healthz / readyz | 服务生命周期管理 | Phase 1 |
| K18 | graceful shutdown | 优雅关闭管理器 | Phase 1 |
| K19 | worker runtime | 后台任务生命周期管理 | 531 |
| K20 | job runtime | 异步任务执行框架 | 531 |
| K21 | scheduler / cron | 定时任务调度 | 531（提前到 v1.0） |
| K22 | retry / timeout / backoff | 独立重试退避运行时 | 531 |
| K23 | transactional outbox | 事务内写入 outbox | Phase 1 |
| K24 | consumed marker | 消费标记（显式） | 531 |
| K25 | replay checkpoint | projection rebuild 检查点 | 516 |
| K26 | idempotency | 消费者幂等（接口 + Redis 实现） | Phase 1 |
| K27 | reconcile runtime | 最终状态收敛运行时 | 531（新增） |
| K28 | JWT / RS256 钉扎 | 算法固定，防 alg confusion | Phase 1 |
| K29 | OIDC / SSO | OpenID Connect 认证流程 | Phase 1 |
| K30 | service auth | 服务间认证（service token） | Phase 1 |
| K31 | RBAC hook | 角色权限钩子 | 531 |
| K32 | secret / key abstraction | KeyManager 接口 | Phase 1 |
| K33 | TLS / mTLS hook | TLS 配置构建器 + mTLS 钩子 | 531 |
| K34 | audit | 审计写入器 | Phase 1 |
| K35 | hash chain | HMAC-SHA256 审计链 | Phase 1 |
| K36 | Prometheus metrics | 指标注册 + HTTP 中间件 | Phase 1 |
| K37 | OpenTelemetry tracing | 分布式追踪 | 530 |
| K38 | log correlation | 日志关联（trace_id/span_id） | 531 |
| K39 | webhook receiver | 接收外部回调（幂等 + 签名验证） | 531 + 用户要求 |
| K40 | webhook dispatcher | 向外部推送（outbox + 重试 + 签名） | 531 + 用户要求 |
| K41 | feature flags | 功能开关 / 灰度 / rollout config | 531（提前到 v1.0） |
| K42 | rollback metadata | 回滚元数据记录 | 531 |
| K43 | kill switch | 紧急关闭开关 | 531 |
| K44 | HTTP middleware 栈 | request_id / real_ip / recovery / access_log / security_headers / body_limit / rate_limit | Phase 1 |
| K45 | bootstrap | 统一启动器 | 讨论确认 |

#### Built-in Cells — 内置 Cell

| 编号 | Cell | Slices | 核心 Journeys |
|------|------|--------|--------------|
| C01 | access-core | identity-manage / session-login / session-refresh / session-logout / authorization-decide | J-sso-login / J-session-refresh / J-session-logout / J-user-onboarding / J-account-lockout |
| C02 | audit-core | audit-write / audit-verify / audit-archive | J-audit-login-trail |
| C03 | config-core | config-manage / config-publish / config-subscribe / feature-flag | J-config-hot-reload / J-config-rollback |

#### First-class Adapters — 一等适配器

| 编号 | Adapter | 说明 |
|------|---------|------|
| A01 | PostgreSQL | 连接 + TxManager + Migrator + 健康检查 |
| A02 | Redis | 连接 + TLS + 健康检查 + 分布式锁 |
| A03 | OIDC provider | SSO/OIDC 认证适配（access-core 使用） |
| A04 | S3 / MinIO | 对象存储 |
| A05 | VictoriaMetrics | 指标推送 |

#### 正式 Adapter Family（v1.0 做基础实现）

| 编号 | Adapter | 热度 | 说明 |
|------|---------|------|------|
| F01 | RabbitMQ | 高 | 命令、回执、DLQ、重试 |
| F02 | WebSocket | 高 | 实时推送、订阅流、signal-first 为默认 |

---

### v1.1 待补（v1.0 后优先迭代）

#### Kernel 补充

| 编号 | 能力 | 说明 |
|------|------|------|
| K50 | support bundle / diagnostics | 生产诊断打包 |
| K51 | contract test framework | 独立于 verify 的契约测试 |
| K52 | replay drill | 回放演练自动化 |
| K53 | testcontainers integration | 集成测试基础设施 |

#### 正式 Adapter Family 补充

| 编号 | Adapter | 热度 | 说明 |
|------|---------|------|------|
| F03 | SSE | 中 | Server-Sent Events |
| F04 | gRPC hook | 中 | gRPC 服务接入钩子 |
| F05 | MySQL / MariaDB | 中 | 连接 + TxManager + Migrator |
| F06 | search abstraction | 中高 | OpenSearch / Elasticsearch 接口 |
| F07 | telemetry adapter | 中高 | VictoriaMetrics 之外的遥测后端 |
| F08 | notification adapters | 中 | email / SMS / Slack / Teams |
| F09 | tenant / workspace | 中高 | 多租户抽象（默认单租户可运行） |
| F10 | policy / rule engine hook | 中 | 策略引擎钩子（不内置引擎） |
| F11 | admin CLI / operator CLI | 中 | 运维命令行工具 |
| F12 | callback dispatcher | 高 | 异步回调分发（补充 webhook） |

#### Examples 补充

| 编号 | 示例 | 说明 |
|------|------|------|
| E01 | examples/sso-bff | SSO + BFF 登录完整旅程 |
| E02 | examples/todo-order | 经典 CRUD + 事件驱动 |
| E03 | examples/iot-device | IoT 设备管理（验证 L4 DeviceLatent） |

#### Templates 补充

| 编号 | 模板 | 说明 |
|------|------|------|
| T01 | ADR 模板 | 架构决策记录 |
| T02 | Cell 设计卡 | 新 Cell 设计模板 |
| T03 | Contract 评审模板 | Contract 变更评审 |
| T04 | Runbook 模板 | 运维操作手册 |
| T05 | Postmortem 模板 | 事后复盘 |
| T06 | Grafana dashboard 模板 | cell health / outbox lag / journey progress |

---

### v2.0 预留边界（暂缓强实现）

| 编号 | 能力 | 热度 | 说明 |
|------|------|------|------|
| R01 | Kafka adapter | 中 | 事件平台化后再做强 |
| R02 | Debezium CDC | 中 | 和 Kafka 配套 |
| R03 | ClickHouse adapter | 中低 | 长期分析上限高 |
| R04 | config center（外部） | 中低 | Consul / etcd / Nacos 集成 |
| R05 | service discovery | 中低 | 多数场景不需要 |
| R06 | full workflow engine / saga | 中低 | 先有 reconciler，再考虑工作流平台 |
| R07 | plugin registry | 低 | 过早平台化 |
| R08 | data lineage | 低 | 有价值但不是底座核心 |
| R09 | batch / ETL framework | 中低 | 可先做薄层 |
| R10 | blue-green / canary hooks | 中低 | 先留部署钩子 |
| R11 | SQLite edge mode | 中低 | Agent 离线缓存 |
| R12 | edge / offline sync | 中低 | 进 adapter family，需 kernel hook |

---

### 不进底座（产品族扩展）

| 能力 | 原因 |
|------|------|
| remote execution / shell | 设备控制面，非通用 |
| session recording | 产品级功能 |
| file transfer channel | 设备通道专用 |
| device / agent command plane | MDM/RMM 专用 |
| built-in visual console | 平台产品 |
| built-in BI / reporting | 平台产品 |
| full API gateway | 多数项目不需要 |
| cost attribution | 商业化能力 |
| notification inbox | 产品级功能 |
| multi-region active-active | 过重过早 |

---

## 2. 内置 Cell 完整清单

### C01: access-core

```
能力：SSO/OIDC 登录 + 密码登录 + JWT 签发/验证 + Session 管理 + 登录锁定 + 角色权限
一致性：L1/L2
Slices: identity-manage / session-login / session-refresh / session-logout / authorization-decide
Journeys: J-sso-login / J-session-refresh / J-session-logout / J-user-onboarding / J-account-lockout
Produces: event.session.created.v1 / event.session.revoked.v1 / event.user.created.v1 / event.user.locked.v1
```

### C02: audit-core

```
能力：事件消费 → 审计写入 + HMAC-SHA256 hash chain + 验证 + 归档
一致性：L2
Slices: audit-write / audit-verify / audit-archive
Journeys: J-audit-login-trail
Consumes: event.session.* / event.user.* / event.config.*
Produces: event.audit.integrity-verified.v1
```

### C03: config-core

```
能力：配置 CRUD + 版本管理 + 热更新推送 + Feature flags + 灰度 + 回滚
一致性：L2
Slices: config-manage / config-publish / config-subscribe / feature-flag
Journeys: J-config-hot-reload / J-config-rollback
Produces: event.config.changed.v1 / event.config.rollback.v1 / http.config.get.v1
```

**三个 Cell 之间的交互：**

```
access-core ──event.session.*──→ audit-core
access-core ──event.user.*────→ audit-core
config-core ──event.config.*──→ audit-core
config-core ──event.config.*──→ access-core（配置热更新）
config-core ──event.config.*──→ 任何订阅 cell
```

**内置 Journey 完整清单（8 条）：**

| Journey | 跨 Cell | 验证要点 |
|---------|---------|---------|
| J-sso-login | access-core | OIDC 完整流程 |
| J-session-refresh | access-core | token 刷新 |
| J-session-logout | access-core | session 吊销 + 事件 |
| J-user-onboarding | access-core | 用户创建到可登录 |
| J-account-lockout | access-core | 锁定 + 解锁 |
| J-audit-login-trail | access + audit | 跨 cell L2 事件消费 |
| J-config-hot-reload | config + all | 配置传播 + 健康验证 |
| J-config-rollback | config + all | 版本回滚 + 审计 |

---

## 3. 演进方向总结

```
v1.0 ──→ Kernel(45项) + 3 内置 Cell + 5 一等 adapter + 2 正式 adapter
  │
  ├── 核心方程：Cell 运行时 + 治理工具 + access/audit/config + PG/Redis/OIDC/S3/VM
  │
v1.1 ──→ 补 support bundle + 6 正式 adapter + 3 examples + templates
  │
  ├── 扩展方程：SSE/gRPC/MySQL/search/notification/tenant + 示例验证
  │
v2.0 ──→ Kafka/Debezium/ClickHouse/workflow/edge 按需
  │
  ├── 平台方程：按实际项目需求选择性做强
```

**持续完善的原则（来自 531）：**

1. **只把跨项目高复用、后补代价高的能力放进 kernel**
2. **把高频但形态多样的能力做成正式 adapter family**
3. **把高上限但重平台化的能力先留边界，不做强实现**
4. **把产品族能力从第一天就隔离到扩展层**
