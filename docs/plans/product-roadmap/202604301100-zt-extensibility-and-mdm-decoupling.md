# 零信任可扩展性 + MDM 解耦探讨

> 日期：2026-04-30
> 状态：**探讨期**（不影响 winmdm Phase 1，作为 zt 启动时（2029 Q1）的设计输入）
> 受众：长期架构规划者 / zt 启动时的设计参与者
> 关联：
> - `202604300900-gocell-as-platform-foundation.md` §4 零信任章节（当前依赖关系将被本文修订）
> - `202604300950-plan-d-go-workspace-multimodule-migration.md` §4.2 module 依赖图（修订点）
> - `202604301030-winmdm-prd-on-gocell.md`（winmdm 第一客户，将作为 device provider 之一）

---

## §0 探讨触发与结论锁定时机

**触发**：2026-04-30 winmdm PRD 对齐过程中识别出两个长期架构问题：

1. 企业现存系统（非 GoCell 实现）如何加入 zt？
2. zt 不应该硬依赖 winmdm，应类比 Intune 的"设备信号 = 中立契约 + 多 provider"模型。

**当前阶段（2026 Q2 - 2028 Q1）不动作**：
- winmdm Phase 1（Stage 1-4，11 cell + 6+1 assembly）按 PRD 实施
- GoCell v1.0 + 方案 D 多 module 切换照常推进
- 本文档不影响任何 P0/P1 排期

**zt 启动前回顾（约 2028 Q4）**：
- 在零信任 Phase 5 设计阶段重新审议本文档
- 确认两条原则后再决定是否落到 plan-d 依赖图 + 路线图 §4 修订

---

## §1 两条核心原则（待 zt 启动时确认）

### 原则 1：企业现有系统接入 zt 必须零代码改造（默认路径）

**论据**：企业不愿为 zt 改造存量系统（OA / ERP / 内部 wiki / 老 Java 单体）。任何要求"加 SDK / 改 middleware"的方案在 enterprise 落地中会失败。Intune / Cloudflare Access / Pomerium / BeyondCorp 的成功核心都是"零接入"。

**推论**：iapgateway cell 必须能处理 100% 流量（HTTP/HTTPS），外部系统不感知 zt 的存在。

### 原则 2：zt module 不依赖 mdm module

**论据**：Intune / Jamf / Workspace ONE / SCCM 已是企业 MDM 既成事实。如果 zt 硬依赖 winmdm，等于强制企业放弃既有 MDM，进入门槛极高。正确模型是"zt 消费设备信号契约，winmdm 是 *一个* provider"。

**推论**：
- `zerotrust/go.mod` 不再 require `mdm`，只 require `core`
- 设备状态契约上移到 `core/contracts/`（中立契约层）
- winmdm 是 device provider 之一，外部 MDM 通过 adapter 也是 provider

---

## §2 问题 1：企业现有系统接入零信任的三种模式

### 模式 A：零接入 — IAP 反代（默认路径）

```
                   ┌─────────────────┐
   client ──────►  │  iapgateway cell│ ──► 现有系统（不改造）
                   │  (反向代理)      │
                   └─────────────────┘
                          │
                          ▼
                   accesscore（身份验证）
                   deviceidentity（设备验证）
                   policycell（策略评估）
                   trustscore（信任评分）
```

**特征**：
- 外部系统零代码改造
- iapgateway 作为 K8s Ingress / 反向代理
- 通过 `externalsystem` cell（**新建**，归属 zerotrust module）让管理员注册系统元数据：
  ```yaml
  - name: "internal-wiki"
    url: "https://wiki.internal"
    routes: ["/wiki/*"]
    trust_level: medium
    require_device_compliance: true
    require_mfa: false
    headers_to_inject: ["X-Forwarded-User", "X-Forwarded-DeviceTrust"]
  ```
- 类比：Pomerium / Cloudflare Access / Google BeyondCorp Enterprise / HashiCorp Boundary

**适用**：90% 企业接入场景。Web 应用 / API / 内部工具。

**限制**：仅 HTTP/HTTPS 协议；非 HTTP 协议（数据库直连 / SSH / RDP）需模式 C。

### 模式 B：半接入 — 虚拟 Cell + Actor 注册

```
zerotrust/contracts/external/v1
       │
       ▼
externalsystem cell ──► 转发 contract 调用
       │
       ▼
adapters/externalapi（HTTP/gRPC/Webhook）
       │
       ▼
现有系统的 API（webhook 接收 / API 调用 / 状态查询）
```

**特征**：
- 利用 GoCell 现有 `actors.yaml`（CLAUDE.md "参与 contract 但不属于 Cell 模型的系统"）
- 每个外部系统注册为 actor，`externalsystem` cell 通过 contract usage 描述其能力
- zt 把 actor 当**虚拟 cell** 看：发布同样的 contract，但实际调用通过 HTTP/gRPC adapter 转出去
- 外部系统提供 webhook 或简单 API 接收 zt 决策（如：用户 X 在设备 Y 上失去信任 → webhook 通知 OA 系统强制登出）

**适用**：外部系统有 API 但不愿改造为 zt 客户端的中等改造场景。

**限制**：需要管理员手动配置 contract → actor 映射。

### 模式 C：深度接入 — SDK / ext_authz Sidecar

```
zerotrust/cmd/zt-sidecar              企业现有应用 pod
   │                                       │
   └─ 部署为 K8s sidecar                   │
       │                                   │
       ▼ Envoy ext_authz / Nginx           ▼
       auth_request 协议                  Application
       │                                   │
       └──────── unix socket / TCP ────────┘
```

**特征**：
- 提供 `zerotrust/cmd/zt-sidecar` binary
- 企业现有应用 pod 加边车，通过 Envoy ext_authz / Nginx auth_request 协议鉴权
- 性能最高（每请求 < 5ms 鉴权延迟）
- 接入成本最高（需改 K8s manifest，加 sidecar 容器）
- 类比：OAuth2-Proxy / Envoy ext_authz / Linkerd policy proxy

**适用**：高性能微服务场景；非 HTTP 协议（gRPC / TCP）；K8s 原生应用。

**限制**：客户必须在 K8s；非 K8s 部署退化到模式 A。

### 三种模式的部署矩阵

| 客户类型 | 推荐模式 |
|---|---|
| 中小企业，少量 web 应用 | **模式 A**（一键部署 IAP 网关）|
| 中型企业，混合应用（web + 内部 API + 老系统）| 模式 A + 模式 B（IAP + 部分系统配置 webhook）|
| 大型企业，K8s 微服务 + 高性能要求 | 模式 A（边缘）+ 模式 C（内部）|
| 国际企业，多 MDM 既存 | 模式 A + 模式 C + 第三方 MDM adapter（见 §3）|

---

## §3 问题 2：zt 解耦 mdm — 中立设备信号契约层

### 当前路线图依赖（待修订）

```
core (foundation)
  ↑
  ├── mdm (winmdm)
  │     ↑
  │     └── zerotrust (depends on core + mdm)   ← 错：硬依赖
```

### 修订后依赖

```
core (foundation, 含 contracts/devicestate/v1 等中立契约)
  ↑                                            ↑
  ├── mdm (winmdm)                             │
  │     └─ implements                          │
  │        contracts/devicestate/v1            │
  │        contracts/deviceidentity/v1         │
  │        contracts/devicecompliance/v1       │
  │        contracts/remotecommand/v1          │
  │                                            │
  └── zerotrust                                │
        └─ consumes (任意 provider) ───────────┘

外部 MDM（Intune / Jamf / SCCM / 客户自建）
  └─ adapter 实现 DeviceStateProvider → 流入 zt 决策
     位置：zerotrust/adapters/externalmdm/{intune,jamf,sccm,workspaceone}
     注：不在 mdm module（winmdm 不应反向代理其他 MDM）
```

### 4 个中立契约（提到 core/contracts/）

| Contract | 路径 | 用途 | winmdm 实现者 | 外部 MDM 实现者 |
|---|---|---|---|---|
| `devicestate/v1` | `core/contracts/devicestate/v1` | 设备健康/在线/合规状态查询 | mdm/cells/devicelifecycle | intune-adapter / jamf-adapter |
| `deviceidentity/v1` | `core/contracts/deviceidentity/v1` | unified_device_id 解析（SMBIOS+Serial → ID）| mdm/cells/deviceidentity | 外部 MDM 的设备 ID 映射器 |
| `devicecompliance/v1` | `core/contracts/devicecompliance/v1` | 合规检查（BitLocker / AV / 补丁版本 / 防火墙）| mdm/cells/policycell.engine | 外部合规扫描器（Intune Compliance / Jamf Smart Group）|
| `remotecommand/v1` | `core/contracts/remotecommand/v1` | 远程命令触发（隔离/通知重认证/Wipe）| mdm/cells/mdmcell.command | 外部 MDM 的远程指令 API |

> **为什么契约放 core 而非新建 devicebase module**：
> - core 是所有 module 的基础，放 core 不引入新依赖
> - 这些契约是设备信号的**通用抽象**，不是 framework 内部实现，符合 core/contracts/ 现有定位
> - 替代方案"新建 devicebase module"会引入额外 module 切换，对增量收益不显著

### 对 11 cell 设计的影响

| 决策 | 结论 |
|---|---|
| `deviceidentity` cell 留 mdm 还是上移到 core？| **留 mdm**。winmdm 是 *一个* device identity 实现（基于 SMBIOS+Serial 哈希），不是 framework 概念。其他 MDM 用不同 ID 算法 |
| `devicelifecycle` cell 同上 | **留 mdm**。winmdm 的 5 状态机是其特定实现 |
| `mdm/adapters/external/{intune,jamf,...}` 反向代理？| **不建在 mdm module**。建在 `zerotrust/adapters/externalmdm/`。winmdm 是 device provider，不应反向"代理"其他 MDM |
| `externalsystem` cell 归属？| 归 zerotrust module（属于 zt 接入面，处理外部业务系统注册而非 MDM 注册）|
| `zt-sidecar` cmd 归属？| 归 `zerotrust/cmd/zt-sidecar` |

### 部署形态矩阵（zt + 多 MDM 组合）

| 场景 | module 部署 | adapter 部署 |
|---|---|---|
| 客户既要 winmdm 也要 zt（统一栈） | core + mdm + zerotrust | 无 |
| 客户只要 zt + 既有 Intune | core + zerotrust | zerotrust/adapters/externalmdm/intune |
| 客户只要 zt + 既有 Jamf（macOS 主导） | core + zerotrust | zerotrust/adapters/externalmdm/jamf |
| 客户只要 zt + 多 MDM 混合 | core + zerotrust | intune + jamf + workspaceone（多 adapter 同时） |
| 客户只要 winmdm（无 zt） | core + mdm | 无 |
| 客户既要 winmdm 也要 Intune（双 MDM） | core + mdm + zerotrust | zerotrust/adapters/externalmdm/intune（zt 同时消费两边）|

→ **这是 GoCell 多形态部署 + 全开源模块化的真正价值**：客户按需组合 module + adapter，没有强制依赖。

---

## §4 与现有路线图的冲突点（zt 启动时决策清单）

下表列出本文若被采纳，对现有 4 份文档的修改点。**zt 启动前不动作**。

| 文档 | 章节 | 修改 |
|---|---|---|
| `202604300950-plan-d-...` | §4.2 module 依赖图 | zerotrust 依赖从 `core + mdm` 改为 `core` |
| 同上 | §3.1 mdm/zerotrust 目录树 | 加 `zerotrust/adapters/externalmdm/{intune,jamf,sccm,workspaceone}` / `zerotrust/cells/externalsystem` / `zerotrust/cmd/zt-sidecar` |
| 同上 | §7.2 archtest CM-LAYER | 加规则禁止 zerotrust import mdm |
| 同上 | §10 阶段 2 PR Z1.1 | go.mod 不再 require mdm |
| `202604300900-...` | §4.2 GoCell 能力映射 | "复用 winmdm cell" 改为 "通过 core/contracts/devicestate/v1 接任意 MDM provider" |
| 同上 | §4.3 cell 复用清单 | 删除 "复用 winmdm 阶段的 cell" 列表，改为 "通过中立契约消费" |
| 同上 | §5.1 阶段化路线图 | 标注 zt 可独立部署，不强依赖 winmdm |
| `202604301030-winmdm-prd-...` | §10 仓库归属 | 加注 winmdm 实现 core/contracts/devicestate/v1 等 4 个中立契约 |
| 同上 | §11.1 P0 项 | core/contracts/devicestate/v1 等 4 个契约定义提到 P0（GoCell v1.0 前置） |
| `core/contracts/` | 新增 | devicestate / deviceidentity / devicecompliance / remotecommand 4 个 contract 目录 |

### 哪些工作可以提前做（不用等 zt 启动）

虽然 zt 启动是 2029 Q1，但有 1 项可以提前到 winmdm Phase 1 做：

**core/contracts/devicestate/v1 等 4 个中立契约定义**：
- 在 winmdm Stage 2-3 实施 mdmcell / agentcell 时，让 winmdm **已经实现这 4 个契约**
- 不需要在 winmdm 内消费它们（winmdm 内部直接调用 cell 即可）
- 但契约提前定义好，让 winmdm 的 cell 接口形状对齐中立契约
- 收益：2029 Q1 启动 zt 时，winmdm 已是合格 device provider，不需要重写 cell 接口

→ 这条建议作为 **winmdm Stage 2 设计审查时的小幅扩展**，加 1-2 周工作量但避免 2029 Q1 大改。

---

## §5 启动条件 / 决策时间点

### 启动 zt Phase 5 前必须确认的 5 个问题

1. 4 个中立契约的字段定义是否冻结？
2. winmdm 是否已实现 4 个契约（提前到 Stage 2 还是 Phase 5 时再补）？
3. 三种接入模式优先实现顺序：A → B → C，还是 A → C 跳过 B？
4. 第一批外部 MDM adapter 优先级（推 Intune 还是 Jamf）？
5. zt-sidecar 的 ext_authz 协议选择 Envoy 还是 Nginx auth_request 优先？

### 决策时间点

- **2028 Q4 zt 启动设计阶段**：重读本文 + winmdm v1 GA 经验 → 决定是否采纳
- **2029 Q1 zt 启动 PR Z1.1**：按本文修订 module 依赖图，go.mod 不 require mdm
- **2029 Q2 zt 第一个 contract 设计**：4 个中立契约首版（如 winmdm Stage 2 已提前定义则复用）

---

## §6 不在本文范围

- **mTLS / service mesh**：仍按现有路线图 §4.4（istio/linkerd adapter）
- **行为分析 / UEBA**：仍按现有路线图 §4.6（eventcorrelation + ML adapter）
- **零信任合规上 SOC2 / FedRAMP**：仍按现有路线图 §5.3 风险评估
- **winmdm 内部架构**：本文不影响 winmdm 11 cell 设计；winmdm 仍按 PRD 实施
- **现存路线图的 P0/P1 项**：本文不影响 GoCell v1.0 前置或 winmdm Stage 1-4 排期

---

## §7 总结（zt 启动时再回顾）

两条原则若被采纳：

1. **企业现有系统接入零信任 = 模式 A 默认 + B/C 按需** —— 不要求外部系统改造
2. **zt module 不依赖 mdm** —— 通过 core 中立契约接入 winmdm / Intune / Jamf / 等任意 MDM provider

这两条原则是 zt 商业化生命力的关键（vs 锁死单 MDM）。但**实施在 2029 Q1**，本文目的是固化讨论上下文，避免 2 年半后丢失。

**当前不动作**：路线图 4 份文档保持现状，winmdm Phase 1 按 PRD 推进。

---

## §8 决策点（zt 启动时填）

- [ ] 接受原则 1（企业接入零代码改造）？
- [ ] 接受原则 2（zt 不依赖 mdm）？
- [ ] 4 个中立契约提前到 winmdm Stage 2 定义（+1-2 周），还是 zt Phase 5 启动时再做？
- [ ] 第一批外部 MDM adapter 优先级（Intune / Jamf / SCCM / Workspace ONE 选 1-2 个）？
- [ ] `zerotrust/adapters/externalmdm/` 路径是否合适，还是建独立 module `externalmdm`？
- [ ] zt-sidecar 是否纳入 zt v1 GA 范围（M7）？
