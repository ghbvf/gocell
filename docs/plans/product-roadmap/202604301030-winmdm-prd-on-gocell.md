# WinMDM 产品需求文档（基于 GoCell 重构版）

> 日期：2026-04-30
> 状态：实施 PRD（取代 `winmdm-mvp-v3/docs/products/prd-v6.md`）
> 关联：
> - `202604300900-gocell-as-platform-foundation.md`（GoCell → MDM → 零信任路线）
> - `202604300950-plan-d-go-workspace-multimodule-migration.md`（go workspace mdm module）
> - `202604300930-repository-structure-decision.md`（已选方案 D 全开源 MIT）
> - 旧版本：`/Users/shengming/Documents/winmdm/winmdm-mvp-v3/docs/products/prd-v6.md` v6.0（已废弃）

---

## §0 与 v6.0 的差异（核心说明）

旧版 v6.0 PRD 假设"5 个独立 Go module 微服务 + Redis Streams EventBus + 经典分层"。本 PRD 完全基于 GoCell cell-native 架构重写，**v6.0 的微服务划分、Redis Streams、独立 Go module、内部 API 契约表全部废弃**，改为：

| 维度 | 旧 v6.0 | 新（本文）|
|---|---|---|
| 架构模型 | 5 微服务 + 独立 Go module | GoCell cell-native（11 cell × 多 slice） |
| 部署形态 | Docker Compose 5 容器 / K8s 5 deployment | 6 种 assembly + 1 单 binary 兜底（`assembly.yaml` 切换） |
| EventBus | Redis Streams | RabbitMQ（GoCell 默认） |
| 服务间同步 | HTTP 内部 API | GoCell contract（`http` kind）|
| 数据库隔离 | 每服务 schema | 每 cell 独立 schema（cell.yaml schema.primary） |
| 仓库 | winmdm 独立仓 | `github.com/ghbvf/gocell/mdm` module（方案 D）|
| 授权 | 商业 | **MIT 全开源** |
| MVP 范围 | Sprint 0-3（8 周）只做注册+采集 | **Phase 1 一次性出齐：注册+采集+策略+分发+分组+RBAC+命令** |
| 启动时间 | 2026 Q1（已开始）| **2027 Q1**（GoCell v1.0 + P0 5 项就绪后）|

旧 v6.0 的 4 大 ADR 全部不再适用（ADR-05 设备分离 / ADR-06 DB per service / ADR-08 微服务从一开始 / ADR-10 移除 AD）。本 PRD 重新基于 GoCell 宪法约束和路线图给出 cell 设计。

---

## §1 产品概述

### 1.1 定义与愿景

**WinMDM**：基于 GoCell 的 Windows 终端管理平台，全开源 MIT。覆盖 Windows 10/11 全生命周期管理，支持从 Docker Compose 单 binary（< 5K 设备）到 K8s 多 assembly（20W+ 设备）连续部署形态。

**愿景**：让 Windows 终端管理像云资源一样灵活实时；通过 GoCell 多形态部署能力，**同一份代码**适配从 SMB 到大型企业的全规模。

### 1.2 核心架构原则

1. **MDM 优先 + Agent 增强**：MDM 协议是基础底座（Windows 原生 OMA-DM），Agent 是能力扩展（自定义 HTTP）
2. **两通道完整自治**：MDM cell 和 Agent cell 各自独立 enroll + device + sync 完整能力，互不依赖
3. **设备身份单点真相**：`deviceidentity` cell 通过 SMBIOS UUID + Serial Number 哈希派生 `unified_device_id`，跨通道识别同一物理设备（替代旧 v6.0 ADR-05 的 Phase 2 Reconciliation Worker）
4. **策略通道无关**：`policycell.router` 产派发事件，MDM 通道和 Agent 通道各自实现派发逻辑（`delivery_channel: mdm | agent | hybrid`）
5. **延迟优化项**：P2P / WebRTC / WNS / 时序遥测 全部延后到 Phase 2+，先做核心功能

### 1.3 目标用户

- **行业**：金融 / 高端制造 / 能源 / 政府央企
- **规模**：从 SMB（< 5K 设备）到大型企业（20W+ 设备）
- **核心痛点**：（1）灵活性不足；（2）反馈延迟；（3）注册困难；（4）合规压力
- **业务边界**：不做 SaaS、不做 EDR/杀毒，私有化部署聚焦

### 1.4 业务边界

**做**：Windows 10/11 全生命周期管理、Docker Compose 与 K8s 双形态部署、私有化交付
**不做**：SaaS（数据自主）、EDR / 杀毒、macOS（未来规划）、AD/LDAP 直连（用 SSO IdP 间接覆盖）

---

## §2 GoCell Cell 设计（11 cell）

### 2.1 Cell 全清单

| # | Cell | 类型 | 一致性 | 复用现有 | 内含 slices | 数据归属 |
|---|---|---|---|---|---|---|
| 1 | `accesscore` | 已有扩展 | L1 LocalTx | ✅ GoCell 内置 | + `jwtlifecycle` + `ssooidc` | `users`, `sessions`, `idp_config` |
| 2 | `auditcore` | 已有扩展 | L2 OutboxFact | ✅ GoCell 内置 | + `approvalflow` | `audit_logs`, `approval_chain` |
| 3 | `rbaccell` | 新建 | L1 LocalTx | — | `rolemgmt` / `permcheck` / `datascope` | `roles`, `permissions`, `user_roles`, `data_scopes` |
| 4 | `pkicell` | 新建 | L1 LocalTx | — | `wstep` / `scep` / `caworkflow` / `rotation` | `ca_certs`, `device_certs`, `cert_issued` |
| 5 | `deviceidentity` | 新建 | L1 LocalTx | — | `resolve` / `bind` / `lookup` | `unified_devices(id, smbios_uuid, serial_number, mdm_device_id, agent_device_id)` |
| 6 | `mdmcell` | 新建 | L1+L2+L4 | — | `enroll` / `device` / `command` | `mdm_devices`, `mdm_enrollments`, `syncml_sessions`, `mdm_commands` |
| 7 | `agentcell` | 新建 | L1+L2+L4 | — | `enroll` / `device` / `checkin` / `taskdispatch` | `agent_devices`, `agent_checkins`, `agent_tasks` |
| 8 | `groupengine` | 新建 | L3 WorkflowEventual | — | `dynrule` / `staticgroup` / `channelaware` | `groups`, `group_rules`, `group_members` |
| 9 | `policycell` | 新建 | L1+L2 | — | `engine` / `router` | `policies`, `policy_versions`, `policy_assignments`, `policy_executions` |
| 10 | `appcatalog` | 新建 | L1 LocalTx | — | `catalog` / `signing` / `scriptlib` / `s3dispatch` | `apps`, `app_versions`, `scripts`, `dispatch_tasks` |
| 11 | `devicelifecycle` | 新建 | L1 LocalTx | — | `tombstone` / `cronsweep` | `device_status_log`（unified_device_id 索引） |

> **id 命名**：全部 no-dash concat 格式（FMT-16 / FMT-C1 强制），由 `gocell validate --strict` 拦截。

### 2.2 关键 Cell 设计要点

#### `deviceidentity`（解决跨通道唯一设备）

```
unified_device_id = SHA-256(smbios_uuid + ":" + serial_number)
```

- `mdmcell.enroll` 注册时同步调用 `device.identity.resolve.v1` → 拿 `unified_device_id` → 写入 `mdm_devices.unified_device_id`
- `agentcell.enroll` 同上 → 写入 `agent_devices.unified_device_id`
- 两通道独立写入各自表，但共享 `unified_device_id` → 跨通道分组、墓碑判断、Phase 2 unified view 全部就绪

#### `policycell.router`（通道无关派发）

```
producer: policycell.router
  └─ event: policy.assigned.v1 {policy_id, target_devices[], delivery_channel}

consumers:
  - mdmcell.command (filter delivery_channel ∈ {mdm, hybrid}) → SyncML 命令
  - agentcell.taskdispatch (filter delivery_channel ∈ {agent, hybrid}) → Agent 任务
```

`delivery_channel = hybrid` 时双通道并行下发；`F-M01 执行反馈` 由 `auditcore` 聚合两通道结果。

#### `devicelifecycle`（基于 unified_device_id 的状态机）

```
状态机：online → offline → dormant(30d) → recycled(90d) → deleted(180d)

判定：
  - MDM OR Agent 任一通道在线 → online
  - 两通道都离线 X 天 → 进入下一状态
  - 通过 deviceidentity.lookup 反查 unified_device_id 关联的两表状态
```

Phase 2+ 加 `reconciler` slice：修复跨通道状态不一致（替代旧 v6.0 ADR-05 Phase 2 Reconciliation Worker）。

---

## §3 Assembly 拓扑（6 + 1 兜底）

### 3.1 7 种 assembly

| Assembly | 含 cell | 副本（20W） | 资源特征 |
|---|---|---|---|
| **wmcore** | accesscore + auditcore + rbaccell + pkicell + deviceidentity | 3（HA） | 被动服务、低 CPU、强一致 |
| **wmmdm** | mdmcell（enroll/device/command）+ adapters/mdmprotocol/windows | 8-15 | SyncML 长会话 + 注册风暴突发 |
| **wmagent** | agentcell（enroll/device/checkin/taskdispatch） | 10-15 | 高 QPS（200K/15min ≈ 222 QPS） |
| **wmgroup** | groupengine | 5 | CPU 密集（全量评估） |
| **wmpolicy** | policycell（engine + router） | 3 | 业务密集 + 状态机 |
| **wmasset** | appcatalog + devicelifecycle | 3 | I/O + 定时任务 |
| **winmdmall** | 全部 cell | 1 | 单 binary 兜底（开发 + SMB < 5K） |

### 3.2 部署形态切换

通过 `assembly.yaml` 切换部署形态（GoCell 多形态部署核心承诺）：

```yaml
# winmdmall/assembly.yaml （SMB / 开发）
cells: [accesscore, auditcore, rbaccell, pkicell, deviceidentity,
        mdmcell, agentcell, groupengine, policycell, appcatalog, devicelifecycle]

# wmcore/assembly.yaml （Enterprise 拆分）
cells: [accesscore, auditcore, rbaccell, pkicell, deviceidentity]
```

### 3.3 跨 Assembly 通信

- **同步调用**（contract `kind: http`）：所有业务 assembly → `wmcore`（auth.validate / audit.write / pki.issue）
- **异步事件**（contract `kind: event`，RabbitMQ）：
  - `device.attribute_changed.v1`：mdmcell/agentcell → groupengine
  - `group.membership_changed.v1`：groupengine → policycell
  - `policy.assigned.v1`：policycell → mdmcell.command + agentcell.taskdispatch

### 3.4 wmcore 高可用与降级

`wmcore` 是核心依赖单点，必须：

1. **至少 3 副本**（K8s anti-affinity 跨 zone）
2. **JWT 公钥客户端缓存**：业务 assembly 缓存 JWKS（24h TTL），wmcore 短暂故障不影响 token 验证
3. **审计写入异步化**：业务 assembly 通过 outbox L2 异步发往 auditcore，wmcore 故障不阻塞业务路径
4. **熔断器**（runtime/circuitbreaker P0）：三态模型 + 重试退避（1s → 2s → 4s + ±20% jitter）+ 幂等键

---

## §4 开发顺序（4 Stage 串行）

### Stage 1：基础设施（2027 Q1，~2 个月）

```
accesscore.{jwtlifecycle, ssooidc} + auditcore.approvalflow + rbaccell + pkicell + deviceidentity + adapters/mdmprotocol/windows（骨架）
```

DoD：
- 自建用户管理 + JWT 完整生命周期（access 1h + refresh 7d + 轮换 + 黑名单 + DPAPI 存储）
- SSO 对接 OIDC（Keycloak/Casdoor 互通测试）
- RBAC 5 角色（Super Admin / MDM Admin / 安全管理员 / Help Desk / Auditor）
- 审批流（24h 超时 + 紧急越权 + 审计告警）
- 内部 CA + WSTEP/SCEP 证书签发链路（Spike-03 出清）
- `wmcore` assembly 可独立启动 + 健康检查

### Stage 2：MDM 通道（2027 Q2-Q3，~3 个月）

```
mdmcell.{enroll, device, command} + adapters/mdmprotocol/windows（完整）+ wmmdm assembly
```

DoD：
- Discovery + XCEP + WSTEP 完整证书链注册
- DevDetail CSP 基础采集（DeviceID/Hostname/MAC/SMBIOS UUID/Serial/OS/Build）
- SyncML 命令下发（Wipe / Lock / Reboot 三件套）+ 状态机（pending → sent → acked → failed）
- E2E：Win10/11 设备 → 控制台手动注册 → 设备列表显示 → 远程 Wipe 触发
- 验收：E-1 ≤ 300s 注册时延 / E-2 100% 必填字段非空 / D-3 Wipe 设备下次联网执行

### Stage 3：Agent 通道（2027 Q3-Q4，~3 个月）

```
agentcell.{enroll, device, checkin, taskdispatch} + wmagent assembly
```

DoD：
- MSI 注册 + EV Code Signing 校验 + WinVerifyTrust + 防降级
- 基础采集（CPU 型号/核心/内存/磁盘/网卡/IP/OS/Build/Agent 版本）
- 15min 短轮询 checkin（不依赖 WebSocket，先做定时拉取）
- Agent 任务派发（脚本执行 / MSI 安装 / 资源采集）
- E2E：MSI 安装 → Agent 注册 → 心跳上报 → 任务拉取执行

### Stage 4：上层应用（2027 Q4-2028 Q1，~3 个月）

```
groupengine + policycell.{engine, router} + appcatalog.{catalog, signing, scriptlib, s3dispatch}
+ devicelifecycle.{tombstone, cronsweep} + wmgroup/wmpolicy/wmasset assembly
```

DoD：
- 静态/动态分组（通道感知 source_channel）+ 全量评估 5W 设备 < 15min
- 策略 CRUD + 版本 + delivery_channel 路由 + 灰度（10% → 50% → 100%）+ 撤回
- 软件库（MSI/MSIX/EXE/ZIP/PowerShell）+ S3 直连分发（断点续传）
- 设备墓碑（30d/90d/180d）+ CronJob 定时扫描

---

## §5 功能模块（与 v6.0 PRD 对应映射）

> 旧 PRD 用 F-{模块}{序号} 编号（F-E / F-D / F-G / F-P / F-S / F-R / F-M / F-A）。本 PRD 保留编号便于追溯，但每条映射到 cell.slice。

### 5.1 F-E 设备注册

| 旧 ID | 功能 | 新映射 |
|---|---|---|
| F-E01 | MDM 手动注册 | `mdmcell.enroll`（Discovery → XCEP → WSTEP → 入 mdm_devices）|
| F-E02 | Agent MSI 注册 | `agentcell.enroll`（MSI 签名校验 + 入 agent_devices）|
| — | 设备身份解析 | `deviceidentity.resolve`（同步调用，建 unified_device_id） |
| F-E（PPKG）| 离线 PPKG 预配 | Phase 2+（暂不列入 Phase 1） |

### 5.2 F-D 设备管理与采集

| 旧 ID | 功能 | 新映射 |
|---|---|---|
| F-D01-MDM | DevDetail CSP 采集 | `mdmcell.device` |
| F-D02-Agent | Agent 基础采集 | `agentcell.device` |
| F-D03 | 远程指令（Wipe/Lock/Reboot） | `mdmcell.command` + `auditcore.approvalflow`（高危操作）|
| F-D 深度采集 | S.M.A.R.T / 显卡 / Wi-Fi 信号 | Phase 2+ 优化项 |

### 5.3 F-G 分组管理

| 旧 ID | 功能 | 新映射 |
|---|---|---|
| F-G01 | 静态分组 | `groupengine.staticgroup` |
| F-G02 | 动态分组（通道感知） | `groupengine.dynrule` + `groupengine.channelaware`（source_channel）|
| F-G03 | 高级动态分组（嵌套+正则）| Phase 2 |
| 跨通道混合分组 | unified_device_id 关联 | **本 PRD 直接就绪**（替代 v6.0 Phase 2 unified view）|

### 5.4 F-P 策略管理

| 旧 ID | 功能 | 新映射 |
|---|---|---|
| F-P01 | 基础 CSP 策略（密码/WiFi/BitLocker/证书/Defender/USB） | `policycell.engine` + `mdmcell.command`（SyncML 下发）|
| F-P02 | 策略依赖编排（≤3 层）| `policycell.engine`（DAG 评估）|
| F-P03 | 灰度发布（10/50/100%） | `policycell.router` + `mdmcell.command`（按比例派发）|
| F-P04 | Windows 补丁管理 | Phase 2（基于 WUfB）|
| F-P05 | BitLocker 密钥托管 | Phase 2（Phase 1 仅开启策略，不回收密钥）|
| F-P06 | 策略撤回与中止 | `policycell.router`（abort event）+ 双通道消费 |

### 5.5 F-S 软件管理

| 旧 ID | 功能 | 新映射 |
|---|---|---|
| F-S01 | MSI/MSIX 静默安装（MDM 通道）| `appcatalog.catalog` + `mdmcell.command`（DownloadAndInstallApp CSP）|
| F-S02 | EXE/ZIP/脚本（Agent 通道）| `appcatalog.scriptlib` + `agentcell.taskdispatch` |
| F-S03 | P2P 分发 | **Phase 2 优化项**（本 PRD Phase 1 走 S3 直连 + 断点续传）|

### 5.6 F-R 远程控制

| 旧 ID | 功能 | 新映射 |
|---|---|---|
| F-R01 | WebSocket 实时推送 | **Phase 2 优化项**（Phase 1 用 15min 短轮询保底）|
| F-R02 | 远程桌面（WebRTC） | **Phase 2 优化项** |
| F-R03 | WebSocket 主动推送 | **Phase 2 优化项** |
| 远程 CLI / 文件传输 | — | Phase 2 |

### 5.7 F-M 监控与报表

| 旧 ID | 功能 | 新映射 |
|---|---|---|
| F-M01 | 执行反馈（双通道）| `auditcore`（聚合 mdmcell.command + agentcell.taskdispatch 反馈）|
| F-M02 | 自定义脚本采集 | `agentcell.taskdispatch` + `appcatalog.scriptlib` |
| F-M03 | 设备遥测（CPU/内存/磁盘/SMART）| **Phase 2 优化项**（依赖 TimescaleDB adapter，性能监测属优化项）|

### 5.8 F-A 权限管理

| 旧 ID | 功能 | 新映射 |
|---|---|---|
| F-A01 | RBAC 角色权限 | `rbaccell.{rolemgmt, permcheck, datascope}` |
| 用户认证 | 自建用户 + SSO | `accesscore.{jwtlifecycle, ssooidc}` |
| 审批流 | 高危操作 24h 审批 | `auditcore.approvalflow` |

---

## §6 验收标准（继承 v6.0 SMART）

### 6.1 注册（Stage 2-3）

| # | 标准 | 阈值 | 测试 |
|---|---|---|---|
| E-1 | MDM 注册到控制台可见 | ≤ 300s | Win10/11 手动注册流程 |
| E-2 | DevDetail CSP 必填字段非空 | 100% | 注册后查 mdmcell.device |
| E-3 | Agent MSI 安装到列表显示 | ≤ 180s | msiexec → checkin |
| E-4 | MDM 注册标识 | "Connected" | 系统设置查看 |
| E-5 | Agent 独立注册 | 100% | 未注册 MDM 设备装 Agent |
| E-N1 | 无效证书拒绝 | 100% | 自签证书发起 → 403 |
| E-N2 | 无效凭据拒绝 | 100% | 错误密码 → 401 |
| **E-6**（新）| unified_device_id 一致 | 100% | MDM + Agent 双注册 → 同 unified_id |

### 6.2 命令下发（Stage 2）

| # | 标准 | 阈值 |
|---|---|---|
| D-3 | Wipe 指令到设备执行 | 设备下次联网即执行 |
| D-5 | MDM/Agent 数据隔离 | 100%（独立表写入）|
| **D-6**（新）| 高危操作审批 | 100% 经过 approvalflow |

### 6.3 策略（Stage 4）

| # | 标准 | 阈值 |
|---|---|---|
| P-1 | BitLocker 策略生效 | ≤ 60min |
| P-2 | WiFi 策略生效 | ≤ 10min |
| P-3 | 策略依赖顺序 | A→B→C 按序 |
| P-4 | 在线下发延迟 | ≤ 10min（Phase 1 短轮询；Phase 2 WNS 唤醒）|
| P-5 | 策略路由正确 | 100%（delivery_channel 严格过滤）|
| P-N1 | 循环依赖检测 | 100% 校验错误 |

### 6.4 分组（Stage 4）

| # | 标准 | 阈值 |
|---|---|---|
| G-1 | 新设备自动分组 | ≤ 10s |
| G-2 | 全量评估性能 | 5W 设备 < 15min |
| G-3 | 增量更新 | ≤ 5s |
| G-4 | 通道感知隔离 | 100%（source_channel 严格过滤）|

### 6.5 软件分发（Stage 4）

| # | 标准 | 阈值 |
|---|---|---|
| S-1 | MSI 静默安装 | > 99% |
| S-2 | 断点续传 | 不重新开始 |
| **S-N1**（新）| MSI 签名校验失败拒绝 | 100% |

### 6.6 RBAC（Stage 1）

| # | 标准 | 阈值 |
|---|---|---|
| A-1 | 权限隔离 | 100% Help Desk 无法 Wipe |
| A-2 | 数据范围限制 | 100% 部门隔离 |
| A-N1 | 越权 API 403 | 100% |
| A-N2 | 过期 JWT 401 | 100% |

---

## §7 非功能需求

### 7.1 性能容量

| 指标 | Phase 1 目标 | 关联 |
|---|---|---|
| 在线设备规模 | 单 wmagentsync 1W → HPA 扩到 20W | 全局 |
| 注册风暴并发 | 1,000 台/分钟 | mdmcell.enroll / agentcell.enroll |
| Service Call P99 | < 50ms（业务 → wmcore）| 全局 |
| EventBus 传播延迟 | < 500ms（RabbitMQ）| policy/group 链路 |
| Agent 静默 CPU | < 0.1% / < 50MB | agentcell |
| Phase 1 不做 | WebSocket 20K 连接 / 实时指令 < 10s | 优化项 → Phase 2 |

### 7.2 可用性

| 组件 | 方案 |
|---|---|
| wmcore | 3 副本 HA + JWKS 客户端缓存 + 异步审计 |
| 数据库 | PostgreSQL 主从 + RPO < 1h |
| RabbitMQ | Cluster + 持久化队列 |
| 弱网 | 断点续传（HTTP Range）+ Agent 本地策略缓存 |

### 7.3 安全

| 维度 | 方案 |
|---|---|
| MDM 通信 | TLS 1.2+ + mTLS 双向（pkicell 签发） |
| Agent 通信 | TLS 1.2+ + Agent JWT |
| 字段加密 | BitLocker Recovery Key AES-256-GCM；用户 PII AES-256 字段级 |
| JWT | RS256 + access 1h + refresh 7d/30d + 轮换 + 黑名单 + DPAPI |
| Service Call 鉴权 | mTLS（Phase 1 可降级 Docker network 隔离）|
| MSI 供应链 | EV Code Signing + SHA-256 + WinVerifyTrust + 防降级 |
| 审计 | 完整审批链（actor / target / before / after / approval_chain）|

### 7.4 兼容性

- **OS**：Windows 10 Build 1809+ 全功能 / Windows 11 全系列
- **不支持**：macOS（未来）、AD/LDAP 直连（用 SSO IdP）、Azure AD（Phase 2 可选 IdP）

---

## §8 路线图与里程碑

### 8.1 时间线（路径 A：先 GoCell 后 winmdm）

```
2026 Q2-Q4               2027 Q1                    2027 Q2-Q3      2027 Q3-Q4    2027 Q4-2028 Q1
─────────────             ─────────────              ─────────────   ───────────   ───────────────
GoCell v1.0 + P0 5 项     Stage 1 基础设施           Stage 2 MDM    Stage 3 Agent  Stage 4 上层应用
                          (accesscore + auditcore +
                          rbaccell + pkicell +
                          deviceidentity)            mdmcell        agentcell      group + policy +
                                                                                   appcatalog +
                                                                                   devicelifecycle
                                                                                   ↓
                                                                                   2028 Q1 winmdm v1 GA
```

### 8.2 关键里程碑

| 里程碑 | 标志 | 时间 |
|---|---|---|
| **M0**：GoCell v1.0 + P0 就绪 | core/方案D + Windows MDM 协议 + WSTEP + JWT 完整 + 熔断 | 2026 Q4 |
| **M1**：winmdm Stage 1 完成 | wmcore 单 assembly 跑通 + RBAC + 审批流 | 2027 Q1 末 |
| **M2**：winmdm Stage 2 完成 | wmmdm assembly + 设备注册 + 远程命令 | 2027 Q3 中 |
| **M3**：winmdm Stage 3 完成 | wmagent assembly + 心跳 + 任务派发 | 2027 Q4 中 |
| **M4**：winmdm v1 GA | 全 6 assembly + 策略/分组/分发 + 5W 设备压测通过 | 2028 Q1 |
| **M5**：winmdm Phase 2 启动 | WebSocket / WebRTC / WNS / TimescaleDB / P2P | 2028 Q2+ |

---

## §9 Phase 2+ 优化项清单（明确延后）

| 优化项 | 价值 | 启动条件 |
|---|---|---|
| `adapters/wns` | MDM 策略推送 < 10min（替代 15min 短轮询） | M4 后 |
| `adapters/timeseries/timescaledb` + `agenttelemetry` cell | 性能监测 / Dashboard 实时聚合 | M4 后 |
| WebSocket 实时推送（agentcell 扩 slice） | 实时指令 < 10s | M4 后 |
| `adapters/webrtc/pion` + 远程桌面 cell | 远程协助 / Help Desk 提效 | Phase 2 |
| `adapters/p2p/bittorrent` + `p2pdistribution` cell | 大规模分发节省 80%+ 出口带宽 | 1W+ 设备规模后 |
| BitLocker 密钥托管 + KeyVault | 密钥回收 / 合规 | Phase 2 |
| Windows 补丁管理（WUfB） | 补丁策略自动化 | Phase 2 |
| `devicelifecycle.reconciler` slice | 跨通道状态修复 | Phase 2 |
| OOBE / Autopilot | 零接触部署 | 依赖 Azure AD（Phase 2）|
| Reconciliation Worker / unified_devices 物化 | **不需要**（已被 deviceidentity 替代）| ❌ 已删除 |

---

## §10 仓库与代码组织

### 10.1 Module 归属

按方案 D（go workspace 多 module），winmdm 全部代码落在 `mdm` module：

```
github.com/ghbvf/gocell/                     单仓库（MIT）
├── go.work
├── go.mod                                    core
├── kernel/ runtime/ adapters/ pkg/           framework
├── cells/{accesscore, auditcore, configcore} core 内置 cells
│
└── mdm/                                       module github.com/ghbvf/gocell/mdm
    ├── go.mod
    ├── kernel/                                MDM 业务抽象（protocol / pki / mdmtypes 接口）
    ├── adapters/
    │   └── mdmprotocol/windows/               OMA-DM/SyncML/XCEP/WSTEP/MS-MDE
    ├── cells/                                 11 cell（如 §2.1）
    │   ├── rbaccell/
    │   ├── pkicell/
    │   ├── deviceidentity/
    │   ├── mdmcell/
    │   ├── agentcell/
    │   ├── groupengine/
    │   ├── policycell/
    │   ├── appcatalog/
    │   └── devicelifecycle/
    ├── contracts/                             MDM 跨 cell 契约
    │   ├── http/
    │   └── event/
    ├── assemblies/                            7 种 assembly（含 winmdmall）
    │   ├── winmdmall/
    │   ├── wmcore/
    │   ├── wmmdm/
    │   ├── wmagent/
    │   ├── wmgroup/
    │   ├── wmpolicy/
    │   └── wmasset/
    └── cmd/winmdm/                            winmdm binary
```

复用 core 的 cells：accesscore + auditcore（在 `mdm/contracts/` 中通过跨 module contractUsages 引用）。

### 10.2 SemVer

- `github.com/ghbvf/gocell` v1.0.x（core）
- `github.com/ghbvf/gocell/mdm` v0.x.x → v1.0.0（M4 GA 时）

---

## §11 与 GoCell 路线图协同

### 11.1 P0 项（GoCell v1.0 前置 / 阻塞 winmdm 启动）

| # | 项 | GoCell 路线归属 |
|---|---|---|
| 1 | go workspace 多 module 切换（PR A1.1）| 方案 D 实施 |
| 2 | `adapters/mdmprotocol/windows`（替代原 Apple/Android 优先） | MDM 路线 §3.4 |
| 3 | WSTEP 协议支持（pkicell）| MDM 路线 §3.3 |
| 4 | JWT 完整生命周期（accesscore 扩 slice）| core 演进 |
| 5 | `runtime/circuitbreaker`（熔断三态 + 重试退避 + 幂等键） | core 演进 |
| 6 | RBAC 提前到 winmdm Stage 1（不再延后到 Phase 3）| MDM 路线调整 |

### 11.2 不再适用的旧路线项

| 旧项 | 状态 | 理由 |
|---|---|---|
| `adapters/eventbus/redisstream` | ❌ 取消 | EventBus 用 RabbitMQ |
| Reconciliation Worker | ❌ 取消 | 已被 deviceidentity 替代 |
| 商业化拆分 gocell-mdm 独立仓 | ❌ 取消 | 方案 D 全开源单仓 |
| `adapters/timeseries/timescaledb` 升 P0 | ⏬ 降 P2 | 性能监测属优化项 |
| WebSocket / WebRTC / WNS / P2P 升 P1 | ⏬ 降 P2 | 优化项延后 |

---

## §12 决策点（待确认）

- [x] 选路径 A（先 GoCell v1.0 后 winmdm）
- [x] 11 cell 设计（含 deviceidentity 解决唯一设备）
- [x] 6 + 1 assembly 拓扑（按职能 + 计算特征拆分）
- [x] Stage 1→2→3→4 串行开发顺序（先 MDM 后 Agent）
- [x] 全开源 MIT + 方案 D 单仓多 module
- [x] EventBus 用 RabbitMQ
- [x] P2 优化项明确延后清单
- [ ] **wmcore 高可用方案**（3 副本 + JWKS 缓存 + 异步审计 + 熔断）放进 P0 路线 — 待批
- [ ] **CI 测试矩阵**：多 assembly + winmdmall 双跑等价性 — 接受 30-50% CI 时间增加
- [ ] **winmdm-mvp-v3 旧仓库归档**：本 PRD 通过后，旧仓库标 `archived` 状态，README 指向本文件

---

## §13 与旧 v6.0 PRD 的字段对应索引

为方便迁移工作量估算，下表给出旧 v6.0 章节与本 PRD 的对应关系：

| 旧 v6.0 章节 | 状态 | 新位置 |
|---|---|---|
| §1.1 产品定义 | 保留 | §1.1 |
| §1.2 解耦双通道 | 保留 + 调整为 cell-native | §1.2 + §2 |
| §1.3 目标用户 | 保留 | §1.3 |
| §1.4 业务边界 | 保留 | §1.4 |
| §1.5 竞争定位 | 保留（移到附录） | 附录 A |
| §2.1-2.7 路线图 + Sprint + Spike | **重写** | §4 + §8 |
| §2.8 验收场景 | 保留 + 新增 unified_id 一致性 | §6 |
| §3 F-E/F-D/F-G/F-P/F-S/F-R/F-M/F-A | 保留 + 映射到 cell.slice | §5 |
| §4 NFR | 保留（去掉服务端微服务相关）| §7 |
| §5.1 前端 | 保留（独立项目，非 GoCell 范畴） | 附录 B |
| §5.2 后端微服务架构 | **删除**（被 §2 + §3 替代）| — |
| §5.3 网络域名 | 保留 | 附录 B |
| §5.4 部署模式 | 重写（assembly 切换） | §3.2 |
| §6.1 外部集成 | 保留 | §1.4 + §7.3 |
| §6.2 零信任安全 | 保留 | §7.3 |
| §6.3 合规审计 | 保留 + 落到 auditcore.approvalflow | §5.8 + §7.3 |
| 附录 A 用户故事 | 保留（迁到本 PRD 附录） | 附录 A |
| 附录 B-G | 保留 | 附录 B-G |
| 附录 E ADR-01 双模架构 | 保留 | §1.2 |
| 附录 E ADR-02 Polling First | 保留 | §1.2 + §5.6 |
| 附录 E ADR-03/07 TimescaleDB | 推迟 | Phase 2 |
| 附录 E ADR-04 PowerShell | 保留 | §5.5 / §5.7 |
| 附录 E ADR-05 设备分离 | **修订**（deviceidentity 替代 Phase 2 Reconciliation） | §2.2 |
| 附录 E ADR-06 DB per service | **删除**（cell schema 隔离替代）| — |
| 附录 E ADR-08 微服务从一开始 | **删除**（cell-native 替代）| — |
| 附录 E ADR-10 移除 AD | 保留 | §1.4 + §7.3 |
| 附录 E ADR-11 MVP 范围缩减 | **删除**（路径 A 一次性出 Phase 1 范围）| — |

---

## 附录 A：竞争定位与用户故事（继承 v6.0）

完整内容参考旧 v6.0 PRD §1.5 + §附录 A，本 PRD 不再赘述。关键改动：

- **竞争策略保留**：私有化 + Windows 深度 + 互联网架构降维（"Go/Microservices/P2P" 描述改为 "Go/cell-native/多形态部署"）
- **核心用户故事**保留 US-101/102/201/202/301/302/401/402 + 否定场景 US-N01/N02/N03

## 附录 B：前端与网络（继承 v6.0）

前端栈（Vue 3 + TypeScript + Ant Design Vue + ECharts + Vite）和网络规划（域名 / 端口 / 防火墙）继承旧 v6.0 §5.1 + §5.3，本 PRD 不重复。

## 附录 C：容量规划与 KPI（继承 v6.0）

容量估算（10W 设备流量 / 存储）+ 北极星指标继承旧 v6.0 附录 F + 附录 G。

## 附录 D：术语表（继承 v6.0）

完整术语继承旧 v6.0 附录 I（OMA-DM / SyncML / CSP / WSTEP / WNS / SCEP / mTLS / RBAC / SMBIOS / DPAPI 等）。

---

## §14 总结

本 PRD 以 GoCell cell-native 架构重写 winmdm，核心简化：
- **架构**：5 微服务 → 11 cell + 6 assembly 多形态部署（同代码 SMB → Enterprise）
- **唯一设备**：deviceidentity cell 替代 v6.0 Phase 2 Reconciliation Worker，从 day 1 就跨通道关联
- **范围聚焦**：注册 + 采集 + 策略 + 分组 + 分发 + RBAC + 命令下发 = Phase 1 一次性出齐
- **优化项延后**：P2P / WebRTC / WNS / TimescaleDB / WebSocket / 远程桌面 / BitLocker 密钥托管 / 补丁 → Phase 2+
- **时间线**：2027 Q1 启动 → 2028 Q1 v1 GA（路径 A，与 GoCell v1.0 协同）
- **仓库**：`github.com/ghbvf/gocell/mdm` module（方案 D 单仓多 module，全开源 MIT）
