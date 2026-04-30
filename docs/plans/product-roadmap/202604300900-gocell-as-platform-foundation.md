# GoCell 作为平台底座 — 库应用矩阵 + Tier C 解耦 + MDM / 零信任路线可行性

> 日期：2026-04-30
> 状态：产品视角探索文档（与 `../engineering-baseline/` 框架视角解耦）
> 受众：(a) 评估 GoCell 是否适合做 MDM / 零信任平台底座的产品决策者；(b) 想理解 GoCell 库形态能拼出什么应用的工程师；(c) 评估 Tier C 解耦边界的 framework 维护者
> 关联：
> - `../engineering-baseline/202604300600-radical-lightweight-revision.md`（核心 10 落点 E1-E10）
> - `../engineering-baseline/202604300700-extension-leverages.md`（Tier B 25 个独立包清单）
> - `../engineering-baseline/202604300800-final-form-capability-overview.md`（双形态能力讲解）

---

## §1 基于 25 包的库形态能拼出什么应用

E14 完成后 GoCell 暴露 25 个独立可用包，按典型场景拼装可覆盖 7 类应用。每个场景列所需包 + 业界类比 + 不能做的部分。

### 1.1 应用矩阵

| # | 场景 | 需要的包 | 业界等价组合 | GoCell 库形态相对优势 |
|---|---|---|---|---|
| **A1** | 通用 HTTP API service | `pkg/errcode + pkg/httputil + pkg/ctxkeys + runtime/observability/{logging,tracing,metrics} + runtime/shutdown + adapters/postgres/{pool,migrator}` (8 包) | chi + uber/zap + prometheus + goose + pgx | **单 import 路径**，错误体系 + HTTP + DB + 可观测一站式 |
| **A2** | 事件驱动 worker | `runtime/eventbus + runtime/outbox + kernel/{outbox,idempotency} + runtime/worker + runtime/distlock + adapters/postgres/pool` (7 包) | Watermill + 自实现 outbox + Redsync + pgx | **transactional outbox + 两阶段幂等开箱即用** |
| **A3** | 高幂等 webhook 服务 | `kernel/idempotency + runtime/eventbus + pkg/{errcode,httputil} + runtime/observability/logging` (5 包) | 自写幂等 + chi + zap | **Claim/Commit/Release 两阶段幂等是稀缺能力**，业界 OSS 罕见 |
| **A4** | IoT 设备 backend | `runtime/websocket + kernel/idempotency + runtime/distlock + adapters/postgres/migrator + runtime/observability/tracing + runtime/shutdown` (6 包) | nhooyr/websocket + 自写幂等 + redsync | **WebSocket + 命令幂等 + 设备会话 trace 三件套**，匹配 L4 DeviceLatent 一致性场景 |
| **A5** | 配置管理 service | `runtime/config + adapters/postgres/pool + runtime/eventbus + pkg/{errcode,httputil}` (5 包) | viper + pgx + watermill | **fsnotify + ConfigMap symlink pivot 检测**直接拿来用 |
| **A6** | CLI 工具 | `runtime/shutdown + pkg/errcode + runtime/observability/logging` (3 包) | cobra + multierr + zap | **退出码分级 + 错误码 Code 枚举**直接对齐 POSIX 规范 |
| **A7** | 数据 ETL pipeline | `runtime/worker + runtime/distlock + adapters/postgres/{pool,migrator} + kernel/idempotency + runtime/eventbus` (6 包) | conc + redsync + pgx + goose | **批处理幂等 + 分布式锁 + outbox 衔接下游**一体 |

### 1.2 库形态的核心价值

**单 go.mod 的 cohesion 优势**：
- 25 个包共享同一组依赖（slog / OTel / yaml.v3），不会出现 import 地狱
- archtest LAYER 守卫保证「轻库形态」不会意外引入 framework 类型
- v1.0 后单 SemVer 版本号，避免 25 个独立 module 协同 release 复杂度

**业界稀缺能力**（GoCell 独有，业界 OSS 替代少）：
1. **kernel/idempotency 两阶段 Claim/Commit/Release**：业界要么用 Redis SETNX（无生命周期管理），要么自写状态机；GoCell 提供完整两阶段语义
2. **runtime/observability/poolstats**：DB pool 统计自动注入 metrics，对接 Provider 抽象，业界要么写 Prometheus collector 要么不做
3. **adapters/postgres/migrator 的 invalid-index 前置检测**：goose 本身没有，避免迁移时打到坏索引
4. **runtime/config 的 K8s ConfigMap symlink pivot 检测**：viper 不处理这种边界情况，GoCell 显式覆盖

### 1.3 库形态不能做的（需要 framework 形态）

- **完整 cell-native 平台**：cell 治理 + 跨 cell contract 双向校验 + boundary.yaml fingerprint diff gate 必须用 framework
- **认证 service 完整功能**：auth 是 Tier C，强耦合 cell.RouteHandler / cell.Mount（详见 §2）
- **多形态部署**：assembly.yaml + boundary.yaml 是 framework 概念
- **archtest LAYER 静态守卫业务代码**：archtest 设计前提是 cells/ 目录组织

---

## §2 Tier C 进一步解耦分析

### 2.1 当前 Tier C 8 子包（grep 实证）

| 包 | framework 契约引用 | 性质 |
|---|---|---|
| `runtime/auth` | 2（cell + wrapper） | JWT 签发/验证 + cell.Mount 路由集成 |
| `runtime/bootstrap` | 3（assembly + cell + wrapper） | 10-phase 启动模型 |
| `runtime/command` | 2（assembly + cell） | gocell CLI 子命令编排 |
| `runtime/eventrouter` | 2（cell + wrapper） | EventRouter goroutine 编排 + EventRegistrar 自动发现 |
| `runtime/http/health` | 6（含 cell.HealthContributor） | /readyz + /livez + 健康检查聚合 |
| `runtime/http/middleware` | 2（cell + wrapper） | 中间件链 + cell context 注入 |
| `runtime/http/router` | 12（含 RouteGroupContributor） | 多 listener 分流 + chi 集成 + auth chain |
| `kernel/{cell,metadata,assembly}` | 自身定义契约 | framework 契约本身 |

### 2.2 可解耦的 5 个候选（拆出新 Tier B 包）

每个候选拆为「core 独立包（Tier B）」+「cell adapter（保留 Tier C）」两部分。

#### 2.2.1 runtime/auth → 拆出 `runtime/auth/jwt`（独立）

**当前耦合**：
- `runtime/auth/Mount(spec ContractSpec, h cell.RouteHandler)` 与 cell 强耦合
- 但 JWT 签发 / 验证 / key rotation 本身与 cell 无关

**拆分方案**：
- **新 Tier B**：`runtime/auth/jwt`（JWT 签发 + 验证 + key rotation + JWKS 服务）
  - API 例：`jwt.NewIssuer(opts...) Issuer` / `jwt.NewVerifier(opts...) Verifier` / `jwt.NewRotator(...)`
  - 零 framework 契约，与 stdlib `golang-jwt/jwt` 同档可独立用
- **保留 Tier C**：`runtime/auth/mount`（auth.Mount + cell.RouteHandler 集成）

**收益**：JWT 能力可独立给非 cell 项目用（如普通 HTTP service 的 JWT middleware）
**代价**：拆分后 mount 包要 import jwt 包，多一层间接

#### 2.2.2 runtime/eventrouter → 拆出 `runtime/eventrouter/core`（独立）

**当前耦合**：依赖 `cell.EventRegistrar` 自动发现。E2 完成后这个依赖消失（contributor 5→1 收编），eventrouter 主体已经是「subscription 编排 + middleware 链 + goroutine 管理」，与 cell 无关。

**拆分方案**：
- **新 Tier B**：`runtime/eventrouter/core`（subscription 编排 + middleware 链 + goroutine 管理）
  - API 例：`router.New(subscriber outbox.Subscriber) *Router` / `router.AddHandler(topic, handler)` / `router.Start(ctx)`
- **保留 Tier C**：`runtime/eventrouter/cell`（与 cell.Registry 集成，从 cell 注册的 Subscribe 调用收集 handler）

**收益**：通用事件路由器，与 watermill router 同档可独立用
**代价**：core 包不能享受 cell 治理（如 metrics-schema OBS-01 守护）

#### 2.2.3 runtime/http/router → 拆出 `runtime/http/multilistener`（独立）

**当前耦合**：12 处 framework 契约引用，主要是 RouteGroupContributor 自动发现 + 路由 mount。E2 完成后大部分依赖消失（cell 通过 Registry.Routes 显式注册）。

**拆分方案**：
- **新 Tier B**：`runtime/http/multilistener`（3 listener 分流：public / internal / metrics + chi 集成 + listener lifecycle）
  - API 例：`multilistener.NewServer(cfg Config) *Server` / `srv.Mount(listener, mux)` / `srv.Start(ctx)`
- **保留 Tier C**：`runtime/http/cellrouter`（从 cell.Registry 收集 Routes 并 mount 到对应 listener）

**收益**：3 listener 分流是常见模式（public API + internal RPC + metrics scrape），可独立给非 cell 项目用
**代价**：拆分后 cellrouter 包多一层调用

#### 2.2.4 runtime/http/middleware → 拆出 `runtime/http/middleware/generic`（独立）

**当前耦合**：2 处契约引用主要是 cell context 注入中间件。其余（logger / recovery / cors / rate-limit / request-id / timeout）与 cell 无关。

**拆分方案**：
- **新 Tier B**：`runtime/http/middleware/generic`（logger / recovery / cors / rate-limit / request-id / timeout / panic-recovery）
- **保留 Tier C**：`runtime/http/middleware/cell`（cell context 注入 + slice context 传递）

**收益**：通用中间件，与 chi/middleware 同档可独立用
**代价**：低（拆分干净）

#### 2.2.5 runtime/http/health → 拆出 `runtime/http/health/server`（独立）

**当前耦合**：6 处契约，主要是 HealthContributor 聚合。E2 完成后从 cell.Registry.Health 收集，主体逻辑与 cell 无关。

**拆分方案**：
- **新 Tier B**：`runtime/http/health/server`（/readyz + /livez 端点 + 检查聚合 + verbose token 保护 + JSON 响应）
  - API 例：`health.NewServer() *Server` / `srv.Register(name, fn HealthCheckFn)` / `srv.Mount(mux)`
- **保留 Tier C**：`runtime/http/health/celladapter`（从 cell.Registry 收集 health checker）

**收益**：健康检查 server 是 K8s 部署标配，与 prometheus client_golang 同档可独立用
**代价**：低（拆分干净）

### 2.3 不应拆的 3 个

| 包 | 不拆理由 |
|---|---|
| `runtime/bootstrap` | 整个就是 GoCell 启动模型；E8 显式化后 phase 函数已经"半库"，但仍依赖 assembly 概念，不是通用 server 启动器 |
| `runtime/command` | gocell CLI 子命令编排，强耦合 metadata 模型，不是通用 CLI 框架（cobra/urfave-cli 替代） |
| `kernel/{cell,metadata,assembly}` | framework 契约本身，拆了等于不要 GoCell |

### 2.4 解耦后的 Tier B 总数

```
当前 Tier B：25 个包
+ E2 完成后自然解耦的 5 个新 Tier B 包：
  - runtime/auth/jwt
  - runtime/eventrouter/core
  - runtime/http/multilistener
  - runtime/http/middleware/generic
  - runtime/http/health/server
─────────────────────────────────
总计 Tier B：30 个独立可用包（v1.0 + 1 后）
```

### 2.5 是否纳入路线图？

**建议**：作为 E14 完成后的延伸（**E15 候选**，进 backlog 评估）。前置条件：
- E2 Contributor 5→1 完成（消除大部分 type assertion 自动发现）
- E14 pkg/runtime/ 库化承诺已落地（25 包先稳定一年再拆 Tier C）
- 有真实外部用户拉群请求 Tier C 子包独立化

不要为了拆而拆——25 包已经覆盖 7 类应用（§1），再拆 5 个 Tier B 包的边际收益递减。

---

## §3 应用 1：MDM 系统（winmdm，第一客户）

> **2026-04-30 重大调整**：本节经 winmdm 第一客户对齐后大幅修订（详见 `202604301030-winmdm-prd-on-gocell.md`）。早期草稿假设的"10 cell + 4 adapter + Apple/Android 优先"已被否决，改为"11 cell + 1 adapter（windows 优先）+ Stage 1-4 串行"。本节是路线图视角的精简版；完整 PRD（验收标准 / NFR / 用户故事）见 winmdm PRD 文档。

### 3.1 MDM 核心需求拆解（聚焦 winmdm Phase 1）

| # | 能力 | Phase 1 范围 | Phase 2+ 优化项 |
|---|---|---|---|
| M1 | 设备注册（MDM + Agent 双通道）| 手动注册 + MSI 签名校验 + WSTEP 证书链 | OOBE / Autopilot / GPO（依赖 Azure AD） |
| M2 | 配置下发 / 策略推送 | 基础 CSP + 策略依赖编排 | 灰度发布 + 撤回 + WUfB 补丁 |
| M3 | 状态采集 | DevDetail CSP + Agent 基础硬件 | **遥测 / SMART / 时序聚合（延后）** |
| M4 | 远程控制 | Wipe / Lock / Reboot + 状态机 + 24h 审批 | **WebSocket 实时 / 远程桌面 / 远程 CLI（延后）** |
| M5 | 应用分发 | MSI/MSIX/EXE/ZIP/PowerShell + S3 直连 + 断点续传 + 包签名 | **P2P 分发（延后，> 1W 设备规模启动）** |
| M6 | 证书管理 | 内部 CA + WSTEP / SCEP + 证书轮换 + 4 类证书生命周期 | HSM / KeyVault 集成 |
| M7 | 合规审计 | 完整 actor/target/before/after + 高危操作审批链 | tamper-evident 存储 / SIEM 集成 |
| M8 | RBAC | 5 角色 + 数据范围 + 自建用户 + SSO（OIDC）| Azure AD（Phase 2 可选 IdP） |

### 3.2 GoCell 能力映射（winmdm 视角）

| MDM 需求 | GoCell 现有 | winmdm 新建 |
|---|---|---|
| M1 设备注册（双通道 + 唯一设备）| kernel/idempotency + outbox | mdmcell.enroll + agentcell.enroll + **deviceidentity**（SMBIOS 哈希 → unified_device_id） |
| M2 配置下发 / 策略路由 | runtime/config + outbox | policycell（engine + router）+ adapters/mdmprotocol/windows |
| M3 基础状态采集 | adapters/postgres | mdmcell.device + agentcell.device（基础硬件，**遥测时序延后**） |
| M4 远程控制 | kernel/idempotency + outbox | mdmcell.command（状态机）+ auditcore.approvalflow（24h 审批） |
| M5 应用分发 | adapters/s3（**已有，复用**） | appcatalog（catalog/signing/scriptlib/s3dispatch） |
| M6 证书管理 | runtime/crypto | pkicell（wstep/scep/caworkflow/rotation） |
| M7 合规审计 | auditcore | + approvalflow slice |
| M8 RBAC | accesscore | rbaccell（rolemgmt/permcheck/datascope）+ accesscore.{jwtlifecycle, ssooidc} |
| 设备生命周期 | — | devicelifecycle（基于 unified_device_id 的 5 状态机） |
| 分组（通道感知） | outbox | groupengine（dynrule/staticgroup/channelaware） |

### 3.3 11 cell 清单（取代旧 10 cell 列表）

> **id 全部 no-dash concat 风格**（FMT-16 / FMT-C1 强制）。复用 GoCell 内置 cell（accesscore / auditcore），新建 9 个 + 扩展 2 个。

| # | cell | 状态 | 一致性 | 内含 slices |
|---|---|---|---|---|
| 1 | `accesscore` | 已有扩展 | L1 | + jwtlifecycle / ssooidc |
| 2 | `auditcore` | 已有扩展 | L2 | + approvalflow |
| 3 | `rbaccell` | 新建 | L1 | rolemgmt / permcheck / datascope |
| 4 | `pkicell` | 新建 | L1 | wstep / scep / caworkflow / rotation |
| 5 | `deviceidentity` | 新建 | L1 | resolve / bind / lookup（**替代旧 Reconciliation Worker**）|
| 6 | `mdmcell` | 新建 | L1+L2+L4 | enroll / device / command |
| 7 | `agentcell` | 新建 | L1+L2+L4 | enroll / device / checkin / taskdispatch |
| 8 | `groupengine` | 新建 | L3 | dynrule / staticgroup / channelaware |
| 9 | `policycell` | 新建 | L1+L2 | engine / router（通道无关派发） |
| 10 | `appcatalog` | 新建 | L1 | catalog / signing / scriptlib / s3dispatch |
| 11 | `devicelifecycle` | 新建 | L1 | tombstone / cronsweep |

**已删除的旧 cell**（早期草稿）：
- ~~deviceregistry / deviceconfig / devicestate / devicecommand~~ → 合并为 `mdmcell` + `agentcell` 多 slice 模型
- ~~tenantisolation~~ → 推迟到零信任阶段（多租户在 winmdm Phase 1 不是 P0）
- ~~mdmgateway~~ → 协议适配下沉到 `adapters/mdmprotocol/windows`
- ~~appdistribution~~ → 改名 `appcatalog`，含脚本 + 资源 + 软件分发统一

### 3.4 Adapter 清单（精简到 1 + 复用）

| Adapter | 优先级 | 状态 |
|---|---|---|
| `adapters/mdmprotocol/windows` | **P0**（OMA-DM / SyncML / XCEP / WSTEP / MS-MDE Discovery） | 新建（替换原 Apple/Android 优先） |
| `adapters/s3` | P0 | **已有，复用**（验证断点续传 / Range 支持，必要时扩 slice） |
| `adapters/wns` | P2 | 延后（Phase 1 用 15min 短轮询保底） |
| `adapters/webrtc/pion` | P2 | 延后（远程桌面） |
| `adapters/p2p/bittorrent` | P2 | 延后（> 1W 设备规模启动） |
| `adapters/timeseries/timescaledb` | P2 | 延后（性能监测属优化项） |
| ~~adapters/mdmprotocol/{apple,android,intune}~~ | ❌ | winmdm 是 Windows-only，不做跨平台 |
| ~~adapters/pki/cmp~~ | ❌ | WSTEP + SCEP 已覆盖 winmdm 场景 |
| ~~adapters/cdn~~ | ❌ | S3 直连 + 断点续传已足够 |
| ~~adapters/policyengine/{opa,cedar}~~ | ❌ | policycell.engine 自实现 DAG 评估 |

### 3.5 Assembly 拓扑（6 + 1 兜底，按职能 + 计算特征）

| Assembly | 含 cell | 副本（20W） | 资源特征 |
|---|---|---|---|
| **wmcore** | accesscore + auditcore + rbaccell + pkicell + deviceidentity | 3（HA） | 被动服务、低 CPU、强一致 |
| **wmmdm** | mdmcell + adapters/mdmprotocol/windows | 8-15 | SyncML 长会话 + 注册风暴 |
| **wmagent** | agentcell | 10-15 | 高 QPS（200K/15min ≈ 222 QPS） |
| **wmgroup** | groupengine | 5 | CPU 密集（全量评估） |
| **wmpolicy** | policycell | 3 | 业务密集 + 状态机 |
| **wmasset** | appcatalog + devicelifecycle | 3 | I/O + 定时任务 |
| **winmdmall** | 全部 cell | 1 | 单 binary 兜底（开发 + SMB < 5K） |

20W 设备总规模 ≈ 30-40 个 pod。**6 + 1 拓扑 = GoCell 多形态部署核心承诺**：同一份代码，`assembly.yaml` 切换 SMB 单 binary ↔ Enterprise 多 binary。

### 3.6 GoCell 适配 MDM 的核心优势

| 优势 | 来源 |
|---|---|
| **L4 DeviceLatent 一致性**天然契合 | CLAUDE.md 明确"设备长延迟闭环、命令回执、证书续期"，mdmcell.command + agentcell.taskdispatch 直接落 L4 |
| **多形态部署**适配 winmdm SMB → Enterprise | winmdmall 单 binary（< 5K）+ 6 拆分 assembly（20W+），同代码切换 |
| **deviceidentity 解决跨通道唯一性** | 旧 v6.0 PRD 留到 Phase 2 的 Reconciliation Worker 在 GoCell 里 day 1 就绪（unified_device_id = SHA-256(smbios+serial)） |
| **强结构化合规** | auditcore.approvalflow + archtest LAYER 守卫，对等保三级 / GDPR 友好 |
| **wmcore 单点高可用** | 3 副本 HA + JWKS 客户端缓存 + 异步审计 outbox + 熔断器（runtime/circuitbreaker P0） |

### 3.7 时间估算（路径 A：先 GoCell v1.0 后 winmdm）

| 阶段 | 时长 | 内容 |
|---|---|---|
| **Phase 0**：GoCell v1.0 + P0 5 项 | 6 个月（2026 Q2-Q4） | E1-E10 + E14 + 方案 D 切换 + Windows MDM 协议 + WSTEP + JWT 完整 + 熔断 |
| **Stage 1**：基础设施（accesscore/auditcore/rbaccell/pkicell/deviceidentity） | 2 个月（2027 Q1） | wmcore 跑通 + RBAC 5 角色 + 审批流 + 内部 CA 链路 |
| **Stage 2**：MDM 通道（mdmcell + windows adapter） | 3 个月（2027 Q2-Q3） | WSTEP 注册 + DevDetail 采集 + Wipe/Lock/Reboot 命令下发 |
| **Stage 3**：Agent 通道（agentcell） | 3 个月（2027 Q3-Q4） | MSI 签名校验 + 基础采集 + 15min checkin + 任务派发 |
| **Stage 4**：上层应用（groupengine + policycell + appcatalog + devicelifecycle）| 3 个月（2027 Q4-2028 Q1） | 分组 + 策略路由 + 软件分发（S3）+ 设备墓碑 |
| **总计** | **17 个月**（含 Phase 0）/ **11 个月**（仅 winmdm Stage 1-4） | winmdm v1 GA 2028 Q1 |

> **P0 阻塞项**（GoCell v1.0 前置）：方案 D 多 module 切换 / `adapters/mdmprotocol/windows` / pkicell WSTEP 支持 / `accesscore.jwtlifecycle` / `runtime/circuitbreaker` / RBAC 提前到 Stage 1。
> **P2 优化项**（明确延后）：WNS / WebRTC / P2P / TimescaleDB / WebSocket / BitLocker 密钥托管 / 补丁管理 / OOBE。

---

## §4 应用 2：企业零信任开发平台可行性

### 4.1 零信任核心要素

零信任架构（NIST SP 800-207）核心 7 原则简化为 6 个能力：

1. **Identity-aware proxy（IAP）**：每请求验证身份 + 设备 + 上下文
2. **Continuous verification**：会话期间持续重新验证（不是登录时一次性）
3. **Least privilege + JIT**：短期凭证 + 即时授权
4. **Microsegmentation**：服务间 mTLS + L7 策略
5. **Device trust**：设备健康检查 + 合规评分
6. **Behavioral analytics**：异常识别 + UEBA

### 4.2 GoCell 能力映射

| 零信任能力 | GoCell 现有 + winmdm 后复用 | 需要新建 |
|---|---|---|
| Z1 IAP | accesscore（JWT/session）+ deviceidentity（设备身份） | iapgateway cell（请求拦截 + 三因素验证：身份/设备/上下文）|
| Z2 Continuous verify | accesscore + outbox | sessionpolicy cell（按策略触发重验证）+ trustscore cell（连续评估） |
| Z3 Least privilege + JIT | accesscore + L1 LocalTx + rbaccell（winmdm 阶段已建） | jitgrant cell（短期凭证签发 + workflow 审批）+ policycell（已在 winmdm 阶段建） |
| Z4 Microsegmentation | runtime/auth + pkicell（winmdm 阶段已建） | service-mesh adapter（Istio / Linkerd）+ mtlscert cell（与 pkicell 集成） |
| Z5 Device trust | mdmcell + agentcell + devicelifecycle（winmdm 阶段已建） | trustscore cell（聚合设备合规度 + 用户行为评分）|
| Z6 Behavioral analytics | adapters/postgres + metrics + outbox | eventcorrelation cell（流式聚合）+ ML adapter（外部推理服务） |

### 4.3 需要新建的 cell 清单（8 个，部分与 winmdm 复用）

1. **iapgateway**（L0 LocalOnly）—— Identity-aware proxy 入口
2. **sessionpolicy**（L1 LocalTx）—— 会话策略评估
3. **trustscore**（L3 WorkflowEventual）—— 设备 + 用户连续信任度评估
4. **jitgrant**（L1 LocalTx）—— JIT 短期凭证签发
5. **mtlscert**（L2 OutboxFact）—— 服务间 mTLS 证书
6. **eventcorrelation**（L3 WorkflowEventual）—— 行为事件流式聚合
7. **anomalydetection**（L3 WorkflowEventual）—— 异常识别（接入外部 ML）
8. **compliancereporting**（L1 LocalTx）—— 合规报告 + 仪表盘

复用 winmdm 阶段的 cell：
- accesscore + auditcore（GoCell 内置）
- rbaccell（角色权限）
- pkicell（证书 + WSTEP/SCEP）
- deviceidentity（unified_device_id 解析）
- mdmcell + agentcell（设备身份与状态）
- policycell（策略评估）
- devicelifecycle（设备状态机）

### 4.4 需要新建的 adapter 清单（4 个）

1. `adapters/servicemesh/{istio,linkerd,consul-connect}` —— 服务网格集成
2. `adapters/idp/{oidc,saml,okta,azuread,googleworkspace}` —— 第三方 IdP 集成
3. `adapters/ml/{tritoninference,sagemaker,vertex}` —— 外部 ML 推理服务
4. `adapters/siem/{splunk,elasticsearch,datadog}` —— 安全信息事件管理

### 4.5 不适合 GoCell 的部分（边界，必须明确）

| 能力 | 不适合理由 | 替代方案 |
|---|---|---|
| Packet-level IPS/IDS | 高频 packet 处理需要 eBPF / kernel module，不是 Go 应用层 | 集成 Cilium / Suricata，GoCell 仅做配置下发 + 事件接收 |
| L7 service mesh data plane | envoy filter 等高性能数据面不是 GoCell 设计领域 | 集成 Istio / Linkerd 数据面，GoCell 仅做控制面配置 |
| ML 推理 | Go 不擅长大规模 ML inference（GPU + 模型服务） | 外部 Triton / Sagemaker，GoCell 通过 ml adapter 调用 |
| 大规模 SIEM 存储 | 安全日志 PB 级存储 + 检索需要 ELK/Splunk 专用栈 | 集成 SIEM adapter，GoCell 仅做事件归集 + 触发 |
| 网络流量分析（NTA） | flow-level 数据分析需要专门工具 | 集成 Zeek / Suricata 输出，GoCell 仅做事件聚合 |

### 4.6 GoCell 适配零信任的核心优势

| 优势 | 来源 |
|---|---|
| **零信任本质 = 多 cell 协作**（IAM + Device + Policy + Audit + Behavioral）—— 与 GoCell 模型同构 | Cell 概念天然映射零信任组件 |
| **Cell 通过 contract 通信** | 零信任「每次请求都通过身份+设备+策略 cell 验证」自然落地 |
| **多形态部署支持 IAP 拓扑** | iapgateway 单独部署（边缘）+ 后端 cell 独立部署（集群）—— assembly 切换 |
| **强结构化合规** | 零信任合规要求高（NIST / FedRAMP）；archtest LAYER + 审计 cell 直接帮上 |
| **库形态作为 Phase 0** | 第三方系统可只用 errcode + idempotency + observability 单包，逐步整合到 zero-trust 平台 |

### 4.7 时间估算

| 阶段 | 时长 | 内容 |
|---|---|---|
| Phase 0-1：GoCell v1.0 + winmdm v1 GA（§3.7） | 17 个月（2026 Q2 ~ 2028 Q1） | 设备 + 身份基础 |
| Phase 5：iapgateway + sessionpolicy + trustscore | 4-5 个月 | IAP + 连续验证基础 |
| Phase 6：jitgrant + mtlscert + service-mesh adapter | 4-5 个月 | 微分段 + JIT 授权 |
| Phase 7：eventcorrelation + anomalydetection + ML adapter | 4-6 个月 | 行为分析 |
| Phase 8：compliancereporting + SIEM adapter + IdP 适配 | 3-4 个月 | 合规 + 集成 |
| **总计**（从零开始） | **32-37 个月**（~3 年） | 完整企业零信任平台 |
| 增量（基于已有 winmdm） | **15-20 个月** | 在 winmdm 基础上加零信任能力 |

---

## §5 GoCell → MDM → 零信任 总体路线

### 5.1 阶段化路线图

```
2026 Q2-Q4              2027 Q1-Q4                          2028 Q1-Q4              2029 Q1+
─────────────────       ─────────────────────────────       ───────────────────     ──────────────
Phase 0 GoCell v1.0     winmdm Stage 1-4                    winmdm v1 GA + Phase 2  零信任 Phase 5-8
+ P0 5 项                Stage 1: 基础设施 (wmcore)         + 优化项收编            (IAP + 微分段 +
                         Stage 2: MDM 通道 (wmmdm)          (WNS/P2P/WebRTC/        行为分析 + 合规)
                         Stage 3: Agent 通道 (wmagent)       TimescaleDB/补丁)
                         Stage 4: 上层应用 (wmgroup/         + 零信任启动
                                  wmpolicy/wmasset)
6 个月                   11 个月                            12 个月                15-20 个月
                                                                                    │
                                  ┌──────────────────────────┐                      │
                                  │ winmdm v1 GA（生产可用） │                      │
                                  │ 客户开始使用              │                      │
                                  │ M4 2028 Q1               │                      │
                                  └──────────────────────────┘                      │
                                                                                    ▼
                                                                          ┌──────────────────────────┐
                                                                          │ 零信任平台 v1 上线        │
                                                                          │ winmdm 客户升级零信任套件 │
                                                                          └──────────────────────────┘

并行：v1.0 后 GoCell 库形态对外开放（25 → 30 包），社区可独立使用单包，反哺 framework 演进
仓库结构：方案 D（go workspace 多 module，全开源 MIT，详见 202604300930）
```

### 5.2 关键里程碑

| 里程碑 | 标志 | 估时 |
|---|---|---|
| **M0 GoCell v1.0** | E1-E10 + E14 全部完成；25 包 SemVer 锁定；方案 D 多 module 切换 + Windows MDM 协议 + WSTEP + JWT 完整 + 熔断 5 项 P0 就绪 | 2026 Q4 |
| **M1 winmdm Stage 1** | wmcore assembly 跑通：accesscore + auditcore + rbaccell + pkicell + deviceidentity；RBAC 5 角色 + 24h 审批流；CA + WSTEP/SCEP 链路 | 2027 Q1 末 |
| **M2 winmdm Stage 2** | wmmdm assembly：mdmcell（enroll + device + command）；100 台 Win10/11 真机注册 + Wipe/Lock/Reboot 通过 | 2027 Q3 中 |
| **M3 winmdm Stage 3** | wmagent assembly：agentcell（enroll + device + checkin + taskdispatch）；MSI 签名校验 + 短轮询 + 任务派发 | 2027 Q4 中 |
| **M4 winmdm v1 GA** | 11 cell + 6 assembly + 1 winmdmall；策略 + 分组 + 软件分发（S3）+ 设备墓碑；5W 设备压测 | 2028 Q1 |
| **M5 winmdm Phase 2** | 优化项收编：WNS / WebRTC / P2P / TimescaleDB / WebSocket / 补丁 / BitLocker 密钥托管 | 2028 Q2-Q4 |
| **M6 零信任 IAP MVP** | iapgateway + sessionpolicy + trustscore；接入 1 个 IdP（OIDC） | 2029 Q1 |
| **M7 零信任 v1 GA** | 8 cell + 4 adapter；service mesh 集成；UEBA 基础 | 2029 Q4 |

### 5.3 风险评估

| 风险 | 严重度 | 缓解 |
|---|---|---|
| **GoCell v1.0 延期** | 高 | E1-E10 是 12-16 周固定承诺；P0 5 项（方案 D / Windows 协议 / WSTEP / JWT / 熔断）必须就绪 |
| **WSTEP 证书链复杂度** | 高 | 旧 winmdm Spike-03 单独立项；2026 Q4 v1.0 前置必跑通；先做 Windows 协议（Apple/Android 取消）|
| **wmcore 单点故障** | 中-高 | 3 副本 HA + JWKS 客户端缓存 + 异步审计 outbox + 熔断器（runtime/circuitbreaker P0 必备）|
| **零信任合规审计要求** | 中-高 | FedRAMP / SOC2 等审计要求会拖 6-12 个月；从 winmdm Stage 1 起就走 archtest + auditcore.approvalflow 完整路径，不要后期补 |
| **ML 推理性能** | 中 | UEBA 需要实时 ML 推理；GoCell 不做模型服务，依赖外部 Triton / Sagemaker，需要做好 fallback（推理超时降级到规则引擎） |
| **Service mesh 集成深度** | 中 | Istio 控制面 API 复杂，建议先做 Linkerd（API 简单）证明可行再扩 Istio |
| **团队规模** | 低-中 | 估时假设 4-6 人专职团队；2-3 人团队需要相应延长 60-100% |

### 5.4 市场定位（全开源 MIT，非商业化）

**winmdm 市场定位**（替代旧"商业化判断"）：
- 已有商业产品：Microsoft Intune / Jamf / Workspace ONE / MobileIron / Hexnode
- 差异化：(a) **全开源 MIT**，类比 Fleet MDM 但 Windows 深度领先；(b) Cell-native 多形态部署（同代码 SMB → Enterprise）；(c) 私有化 + 中文文档完整 + 信创替代场景
- 不竞争 SaaS 市场（Intune / Hexnode），专注 Windows 私有化大规模部署

**零信任定位**：
- 作为 winmdm 客户升级路径，不直接和 Zscaler / Cloudflare 竞争 SaaS
- 全开源 MIT，类比 Pomerium / OpenZiti 但与 winmdm 设备身份一体

### 5.5 推荐策略

**短期（2026 Q2-Q4）**：GoCell v1.0 + P0 5 项（方案 D / Windows 协议 / WSTEP / JWT / 熔断）；examples/ 加 winmdmsmoke 作为多 assembly 示例。

**中期（2027 Q1-2028 Q1）**：winmdm Stage 1-4 串行落地，11 cell + 6 assembly + 1 winmdmall；M4 GA 时 5W 设备压测通过。

**长期（2028 Q2+）**：winmdm Phase 2 收编优化项（WNS / WebRTC / P2P / TimescaleDB）；2029 Q1 启动零信任。

**不变**：全开源 MIT，单仓库 go workspace 多 module（方案 D）。framework / winmdm / 零信任 同 org 同仓库共同演进。

---

## §6 决策点（已确认）

- [x] **路径 A**：先 GoCell v1.0 后 winmdm（牺牲 9 个月时间换架构稳健，2027 Q1 winmdm 启动）
- [x] **协议优先级**：Windows-only（OMA-DM/SyncML/XCEP/WSTEP）；Apple/Android/Intune 不做（winmdm Phase 1 范围）
- [x] **EventBus**：RabbitMQ（GoCell 默认；不引入 Redis Streams adapter）
- [x] **零信任作为 winmdm 客户升级路径**：基于 winmdm 设备身份 + RBAC + PKI 复用
- [x] **全开源 MIT**：方案 D 单仓多 module（详见 `202604300930-...` §4.6）；不走商业化拆分
- [x] **deviceidentity 替代 Reconciliation Worker**：winmdm Phase 2 unified view 直接就绪
- [x] **优化项明确延后到 P2**：WNS / WebRTC / P2P / TimescaleDB / WebSocket / 远程桌面 / BitLocker 密钥托管 / 补丁
- [ ] **wmcore 高可用方案**（3 副本 HA + JWKS 缓存 + 异步审计 + 熔断）作为 P0 路线最后一项 — 待批
- [ ] **CI 多 assembly + winmdmall 双跑等价性测试**（30-50% CI 时间增加）— 接受？
- [ ] **winmdm-mvp-v3 旧仓库归档**：本路线确认后通知归档
