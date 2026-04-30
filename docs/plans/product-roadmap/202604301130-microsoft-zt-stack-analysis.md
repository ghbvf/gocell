# 微软零信任栈架构分析（Entra ID + Intune + Defender + Purview）

> 日期：2026-04-30
> 状态：参考材料（对标分析，作为 GoCell zt 设计输入）
> 数据源：Microsoft Learn 公开文档（2026-04 最新版）+ Conditional Access overview / MTD connector / 第三方 compliance partner 文档
> 关联：
> - `202604301100-zt-extensibility-and-mdm-decoupling.md`（GoCell zt 设计探讨，本文是其对标依据）
> - `202604300900-gocell-as-platform-foundation.md` §4 零信任章节

---

## §0 为什么对标微软栈

微软是事实上的零信任标杆，原因：

1. **完整产品线**：从身份（Entra ID / 前 AAD）→ 设备（Intune / Defender for Endpoint）→ 应用（Defender for Cloud Apps）→ 数据（Purview）→ 网络（Global Secure Access），全栈自有
2. **生态最大**：19+ 第三方 MDM 通过 compliance partner / MTD partner / SAML 三类 API 接入；任何企业身份系统都能通过 OIDC/SAML 接入
3. **Conditional Access 是事实标准**：业界谈零信任策略引擎，CA 是默认参考实现
4. **公开文档完整**：架构 / API / 数据流 / 集成方式都有官方说明

GoCell zt 不照抄微软（不是 SaaS、不绑定 Azure），但**架构原则可借鉴**：信号-决策-执行三层模型 + 中央策略引擎 + 第三方 partner API 解耦。

---

## §1 零信任三原则（微软定义）

| 原则 | 含义 |
|---|---|
| **Verify explicitly** | 基于所有可用数据点（身份/设备/位置/应用/风险）显式验证授权 |
| **Use least privilege access** | 最小权限 + JIT/JEA + 基于风险的自适应策略 + 数据保护 |
| **Assume breach** | 最小爆炸半径，分段访问，端到端加密，分析驱动威胁检测 |

→ 所有产品设计围绕这 3 条原则。GoCell 路线图 §4.1 的 6 个零信任能力（IAP / Continuous verify / Least privilege+JIT / Microsegmentation / Device trust / Behavioral analytics）是对这 3 条原则的展开。

---

## §2 微软零信任 6 支柱架构

```
                    ┌─────────────────────────────────────────────────┐
                    │     跨切关注点（Cross-cutting Layers）          │
                    │   Visibility & Analytics / Automation &         │
                    │   Orchestration / Governance & Compliance       │
                    └─────────────────────────────────────────────────┘
                               ↑       ↑       ↑       ↑
        ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐
        │Identity │  │Endpoint │  │  Apps   │  │  Data   │  │ Infra   │  │Network  │
        │         │  │(Device) │  │         │  │         │  │structure│  │         │
        │Entra ID │  │ Intune  │  │Defender │  │Purview  │  │Defender │  │Global   │
        │+ ID     │  │ MDE     │  │for Cloud│  │ MIP     │  │for Cloud│  │Secure   │
        │Protection│  │ MAM     │  │Apps     │  │ DLP     │  │         │  │Access   │
        └─────────┘  └─────────┘  └─────────┘  └─────────┘  └─────────┘  └─────────┘
```

**6 支柱产品对应**：

| 支柱 | 微软产品 | GoCell 对应（规划） |
|---|---|---|
| Identity | Microsoft Entra ID（前 Azure AD）+ Entra ID Protection | accesscore + rbaccell + deviceidentity |
| Endpoint | Microsoft Intune（MDM/MAM）+ Defender for Endpoint（EDR）| winmdm（mdmcell + agentcell + devicelifecycle）+ MDE-adapter（Phase 2+）|
| Apps | Defender for Cloud Apps（CASB）| externalsystem cell + app-gallery（zt 启动时设计）|
| Data | Microsoft Purview Information Protection（标签 + DLP）+ BitLocker | 本期不做（路线图 §4.5 标"不适合 GoCell"）|
| Infrastructure | Defender for Cloud（CSPM）| 不做（IaaS/CSPM 非 GoCell 场景） |
| Network | Microsoft Global Secure Access（SASE / SSE） | iapgateway + 不做 SASE 数据面 |

**跨切关注点**：

| 关注点 | 微软实现 | GoCell 对应 |
|---|---|---|
| Visibility & Analytics | Sentinel（SIEM）+ Defender XDR | adapters/siem/{splunk,...} + eventcorrelation cell |
| Automation & Orchestration | Logic Apps + Sentinel SOAR + CA Optimization Agent | policycell.router + workflow cell（未规划）|
| Governance & Compliance | Purview Compliance Manager + Entra ID Governance | auditcore.approvalflow + compliancereporting cell |

---

## §3 中央决策引擎：Conditional Access (CA)

### 3.1 CA 是策略引擎（"Zero Trust Policy Engine"）

微软官方定义：**Conditional Access is Microsoft's Zero Trust policy engine** taking signals from various sources into account when enforcing policy decisions.

策略形式：**if-then 语句**
- IF: 信号匹配（用户/设备/应用/风险/位置）
- THEN: 决策（block / grant + 附加要求）

### 3.2 CA 信号源（输入）

| 信号 | 来源 | GoCell 对应规划 |
|---|---|---|
| 用户 / 组 / Agent 身份 | Entra ID 用户库 | accesscore |
| IP 位置 | 自带 + 第三方威胁情报 | iapgateway 中间件 |
| 设备状态 | Intune 合规结果 + Entra ID 设备记录 | deviceidentity + devicelifecycle |
| 应用 | Entra ID app registry | externalsystem cell（zt 启动时新建）|
| 实时风险 | Entra ID Protection（用户/登录/Agent 行为风险） | trustscore cell + Entra ID Protection 类似的 cell |
| 云应用风险 | Defender for Cloud Apps（CASB） | Phase 2+ |
| **设备威胁等级** | **Defender for Endpoint（自家 EDR）+ MTD partner（19 家第三方）** | adapters/edr/{mde, lookout, zimperium, ...} + trustscore cell |
| **设备合规** | **Intune 自家 + 19 家第三方 MDM compliance partner** | core/contracts/devicecompliance/v1（中立契约）|

### 3.3 CA 决策（输出）

```
最严格 → 最宽松
─────────────────────────────────────────────────────
Block access （直接拒绝）
─────────────────────────────────────────────────────
Grant access + 一组要求：
  • Require Multi-Factor Authentication (MFA)
  • Require authentication strength（指定 MFA 方法强度）
  • Require device to be marked compliant（必须 Intune 合规）
  • Require Entra hybrid joined device（必须域加入）
  • Require approved client app（必须批准的 app）
  • Require app protection policy（MAM 保护）
  • Require password change
  • Require terms of use
─────────────────────────────────────────────────────
```

### 3.4 关键架构事实

- **CA 在第一因素认证完成后强制执行**（不是首层防御，不替代 DDoS 防护）
- **CA 通过 Microsoft Graph API 创建策略**（程序化管理）
- **License 要求**：Entra ID P1（基础 CA）+ Entra ID P2（风险类策略）+ Intune（设备合规类）+ Purview（数据类）—— **越多场景需要越多 license**
- **Conditional Access Optimization Agent**（2026 新功能）：Security Copilot 集成，AI 推荐策略

→ **GoCell 对应**：`policycell.engine` 是 CA 等价物。但 GoCell 不收 license 费，全开源 MIT。

---

## §4 数据流：信号 → 决策 → 执行

```
┌─────────────────────────────────────────────────────────────────────┐
│  信号源（Signal Sources）                                          │
│                                                                     │
│  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐  │
│  │Entra ID │  │ Intune  │  │   MDE   │  │ MTD     │  │3rd-party│  │
│  │+ ID     │  │compliant│  │ risk    │  │partner  │  │MDM      │  │
│  │Protect  │  │check    │  │ score   │  │ EDR     │  │compliant│  │
│  └────┬────┘  └────┬────┘  └────┬────┘  └────┬────┘  └────┬────┘  │
│       │            │             │             │             │       │
│       │            │             │             │             │       │
│       └────────────┴─────────────┴─────────────┴─────────────┘       │
│                              ↓                                       │
│                    Microsoft Graph API                               │
│              （统一数据/控制接入层，REST/JSON）                       │
│                              ↓                                       │
└─────────────────────────────────────────────────────────────────────┘
                               ↓
┌─────────────────────────────────────────────────────────────────────┐
│              Conditional Access 决策引擎                            │
│                                                                     │
│           Aggregates signals → if-then policies → decision          │
└─────────────────────────────────────────────────────────────────────┘
                               ↓
┌─────────────────────────────────────────────────────────────────────┐
│  执行点（Enforcement Points）                                       │
│  • Entra ID 登录流（aad.microsoft.com）                             │
│  • Apps 通过 OIDC / SAML 接入 Entra ID（被动消费决策）              │
│  • Intune 设备策略（MDM 协议下发）                                   │
│  • MDE 设备隔离 / Defender for Cloud Apps 会话控制                  │
└─────────────────────────────────────────────────────────────────────┘
```

**关键**：所有信号都通过 **Microsoft Graph API** 写到统一数据存储（Entra ID + Intune Service），CA 引擎从这里读取后做决策。

---

## §5 Entra ID 作为身份 + 设备身份的事实数据库

**核心观察**：Entra ID 不只是身份目录，**也是设备目录**：
- 每个被 Intune（或第三方 MDM）管理的设备 → 在 Entra ID 创建一条 device record
- 设备的 compliance state（compliant / non-compliant / not evaluated）写入 Entra ID device record
- CA 引擎读取 Entra ID device record 做策略评估，**不直接读 Intune**

→ 这意味着 **Intune 是 device state writer，Entra ID 是 device state store，CA 是 reader**。Intune 故障不影响 CA 读已有合规状态（fault tolerance）。

**对 GoCell 启示**：
- `deviceidentity` cell 应该是**所有 device state 的真相源**（类似 Entra ID 设备目录）
- `devicelifecycle` cell 写状态到 deviceidentity
- `policycell.engine` 从 deviceidentity 读取，**不直接调** mdmcell / agentcell
- 这正好对应 zt-extensibility 探讨中"中立契约"的设计

---

## §6 Intune 作为设备合规 hub

**Intune 双重角色**：
1. **MDM provider**（自家 MDM）：注册 / 配置 / 命令 / 应用分发，对应 winmdm
2. **Compliance hub**：聚合**自家 + 第三方 MDM** 的合规结果，写入 Entra ID

```
              ┌──────────────┐
Intune 自家 ─►│              │
              │   Intune     │
Jamf       ──►│  Compliance  │──► 写入 Entra ID device record
Workspace1 ──►│     Hub      │
SOTI       ──►│              │
... (19+)  ──►│              │
              └──────────────┘
                     ↓
              Conditional Access reads
```

→ **Intune 是 hub 而非 single MDM**。第三方 MDM 接入后，**该用户组设备的 MDM 权威切换为第三方 MDM**（Intune 不再下发策略），但合规状态依然写入 Entra ID 供 CA 消费。

**对 GoCell 启示（验证 zt-extensibility 探讨）**：
- winmdm 既是 device provider 又是合规 hub？还是分开？
- 微软答案：**Intune 同时承担 hub 角色**。但 GoCell 路线图把 hub 分到 zerotrust module（externalmdm adapter 在 zt 而非 mdm）— 这避免 winmdm 反向代理其他 MDM
- 微软的"用户组 → MDM 权威"机制可参考：管理员配置每个组用哪个 MDM，winmdm vs Intune-adapter vs Jamf-adapter

---

## §7 第三方接入：3 类 Partner API

### 7.1 Device Compliance Partner（19 家第三方 MDM）

**支持列表**：42Gears SureMDM / 7P / Addigy / BlackBerry UEM / Citrix Workspace / CLOMO MDM / Fleet / IBM MaaS360 / **Jamf Pro** / Kandji / Ivanti Neurons / Ivanti EPMM / mobiconnect / Mosyle Fuse / Mosyle Onek12 / **Omnissa Workspace ONE UEM** / Scalefusion / SOTI MobiControl

**接入方式**：
1. 在 Intune admin center 添加 compliance partner（按平台 + 用户组分配）
2. 第三方 MDM admin console 配置 Intune connector
3. 设备从第三方 MDM 注册
4. 第三方 MDM 推送合规状态到 Intune → Entra ID

**关键约束**：每个平台（iOS / Android / macOS）只能选 1 个 partner（不允许同一平台多 MDM 重叠）。

→ **对 GoCell**：`zerotrust/adapters/externalmdm/{intune,jamf,workspaceone,...}` 实现 `core/contracts/devicecompliance/v1`，写入 deviceidentity cell。

### 7.2 Mobile Threat Defense Partner（EDR/MTD 集成）

**支持列表**：Microsoft Defender for Endpoint（自家）+ Lookout / Zimperium / Wandera / Symantec / Check Point / Sophos / 等

**接入方式**：
1. Intune admin center 启用 MTD connector
2. 第三方 MTD 在自己 console 配置 Intune connection
3. 配置三类 toggle：
   - **Compliance policy evaluation**：威胁等级触发合规规则（"Device Threat Level"）
   - **App protection policy evaluation**：MAM 场景触发"Max allowed threat level"
   - **Shared settings**：app sync / certificate sync / partner timeout

**信号流**：
```
设备 → MTD agent 检测威胁 → MTD service 计算 risk level
                                          ↓
                          MTD partner connector → Intune
                                          ↓
                            Intune compliance evaluation
                                          ↓
                          Entra ID device record updated
                                          ↓
                          CA reads → block / require MFA
```

**重要变化**（2024-04 起）：第三方 MTD 不再需要 classic CA policy，自动集成。

→ **对 GoCell**：`adapters/edr/{mde,lookout,...}` 实现 `core/contracts/devicestate/v1` 中的 risk score 字段，trustscore cell 消费。

### 7.3 SAML / OIDC 应用接入（Application Gallery）

**接入方式**：任何应用通过 SAML 2.0 / OIDC 接入 Entra ID，自动获得 CA 策略保护：
- 应用要求登录 → 重定向到 Entra ID
- Entra ID 验证身份 → 触发 CA 评估 → 通过 SAML response / OIDC token 返回应用

**Application Gallery**：4000+ 预配置应用模板（Salesforce / Slack / Workday / 等）。

→ **对 GoCell**：模式 A（IAP 反代）+ B（externalsystem cell 注册）的组合。差别是 GoCell 不做 SaaS app gallery，而是企业自配置。

### 7.4 Microsoft Graph API：统一接入层

所有数据 / 操作都经过 `graph.microsoft.com`：
- 创建/查询 CA 策略
- 查询设备 / 用户 / 应用
- 触发 Intune 命令（Wipe / Lock / Sync）
- 配置 partner connector

**协议**：REST + JSON + OAuth 2.0 token

→ **对 GoCell 启示**：GoCell contract（http kind）是等价物，但每个 cell 自己暴露 contract，没有统一 graph 网关。**是否需要 GoCell Graph API**（聚合所有 cell 的查询/操作 API）？这是 zt 启动时可考虑的工具层项目。

---

## §8 数据保护层（Purview + BitLocker）

**Purview Information Protection (MIP)**：
- 文件 / 邮件级别**敏感性标签**（Confidential / Highly Confidential / Public）
- 标签触发**加密 + 访问控制 + 水印**
- DLP（数据丢失预防）：传输时检测敏感数据
- 与 CA 联动：CA 可基于"用户访问 Confidential 文件"决策强制 MFA

**BitLocker**：
- 磁盘级加密
- 密钥托管在 Entra ID（device record 关联恢复密钥）
- Intune 下发 BitLocker 策略，密钥自动 escrow 到 Entra ID

→ **对 GoCell**：本期不做（路线图 §4.5 已标"不适合"）。BitLocker 密钥托管延后到 winmdm Phase 2。Purview 类的数据分类标签平台不在 GoCell 范畴。

---

## §9 微软栈的关键架构特征（GoCell 应学习）

### ✅ 学习点

| 特征 | 微软实现 | GoCell 对应 |
|---|---|---|
| **中央策略引擎** | Conditional Access | policycell.engine |
| **统一身份+设备数据库** | Entra ID 同时存身份和设备 | deviceidentity + accesscore |
| **partner API 解耦** | Compliance Partner / MTD Partner / SAML | core/contracts/devicestate/v1 等 4 个中立契约 |
| **设备状态由 hub 聚合** | Intune 聚合 19+ MDM 合规 | deviceidentity 聚合多 provider |
| **Graph API 作为统一入口** | graph.microsoft.com REST | （GoCell 未规划，可考虑作为长期工具） |
| **3 原则一致性** | Verify / Least Priv / Assume Breach | 同步采纳作为设计准则 |
| **policy = if-then DSL** | CA 策略 = signals + decision + controls | policycell.engine 应实现类似 DSL |

### ❌ 不照搬部分

| 特征 | 不学习理由 |
|---|---|
| SaaS 部署 | GoCell 私有化，不做 SaaS |
| License 分级（P1/P2 + 多产品组合）| 全开源 MIT，单一形态 |
| Application Gallery（4000+ app）| 不做应用市场，企业自配置 |
| Purview / Compliance Manager | 数据合规非 GoCell 场景 |
| Defender for Cloud（CSPM）| IaaS 安全非 GoCell 场景 |
| Microsoft Graph API（统一聚合层）| 可选，初期不做（增加 framework 复杂度）|
| Security Copilot AI 推荐 | 长期可选，初期不做 |

---

## §10 对 GoCell zt 设计的具体启示

### 10.1 验证 zt-extensibility 探讨文档的设计

| zt-extensibility 探讨原则 | 微软对标验证 |
|---|---|
| 原则 1：企业现有系统零代码改造接入 | ✅ Application Gallery + SAML/OIDC 印证 |
| 原则 2：zt 不依赖 mdm | ✅ Intune Compliance Partner（19 家第三方 MDM）+ MTD Partner（多家 EDR）印证 |
| 4 个中立契约（devicestate / deviceidentity / devicecompliance / remotecommand）| ✅ 对应微软 Compliance Partner API + MTD Partner API + Entra ID device record + Intune remote actions |
| 三种接入模式（A IAP / B 虚拟 cell / C ext_authz）| ✅ A 对应 Entra Application Proxy / B 对应 SAML+SCIM / C 对应 Microsoft Tunnel |

→ 探讨文档的设计**与微软栈高度一致**，可以保持原方向，2029 Q1 zt 启动时落实。

### 10.2 微软栈给的额外灵感（可加入探讨文档）

| 灵感 | 加入 zt-extensibility 探讨文档 §X |
|---|---|
| **CA 策略 DSL（if-then）**：策略不是代码而是配置 | §3 加 policycell.engine 的策略 DSL 设计要求 |
| **每平台只能选 1 MDM**：避免重叠管理 | §3 部署矩阵加约束："同一设备只能由一个 MDM provider 管理" |
| **partner timeout / 失联**：partner 不响应时的降级行为 | §3 加 contract 的 SLA / fallback 设计 |
| **Graph API 作为统一聚合层**：可能未来需要 | §6 范围之外加注："长期可考虑 GoCell Graph 聚合层" |
| **设备 record 在 Entra ID 而非 Intune**：状态读写分离 | §3 加 deviceidentity 是真相源（不是 winmdm）|
| **AI 优化（CA Optimization Agent）**：Security Copilot 集成 | §6 范围之外加注："Phase 3+ 可考虑 AI 策略推荐" |

### 10.3 winmdm Phase 1 现在该做的小幅调整

虽然 zt 是 2029 Q1，但**有 1 项可以从 winmdm Stage 2 开始做**（已在 zt-extensibility 探讨文档 §4 提过）：

→ **4 个中立契约定义提前到 winmdm Stage 2**（+1-2 周工作量）：
- `core/contracts/devicestate/v1`（设备健康/在线/合规）
- `core/contracts/deviceidentity/v1`（unified_device_id 解析）
- `core/contracts/devicecompliance/v1`（合规检查规则）
- `core/contracts/remotecommand/v1`（远程命令触发）

→ winmdm 11 cell 实现这 4 个契约，让 winmdm 从 day 1 就是合格的"device provider"，2029 Q1 zt 启动时无需大改。

→ 微软用了 10+ 年才把 Intune 改造成"hub + provider 双角色"。GoCell 一开始就按"中立契约 + 多 provider"设计，避开历史包袱。

---

## §11 关键引用源

| 文档 | URL | 用途 |
|---|---|---|
| Zero Trust Overview | learn.microsoft.com/security/zero-trust/zero-trust-overview | 三原则 + 6 支柱总览 |
| Conditional Access Overview | learn.microsoft.com/entra/identity/conditional-access/overview | CA 策略引擎机制 |
| Intune MTD Connector | learn.microsoft.com/intune/device-security/mobile-threat-defense/enable-connector | EDR/MTD partner 集成 |
| Intune 3rd-party Compliance Partners | learn.microsoft.com/intune/device-security/compliance/third-party-partners | 19 家 MDM compliance partner |
| Microsoft Graph API | learn.microsoft.com/graph | 统一接入层（本文未深入）|

---

## §12 总结

微软零信任栈的核心架构是 **"信号源 + 中央 CA 引擎 + 统一身份/设备数据库 + 3 类 partner API"**：

1. **信号源**：Entra ID 身份 / Intune 合规 / MDE 风险 / 19 家第三方 MDM / 多家 EDR / 位置 / 应用
2. **CA 引擎**：if-then 策略，aggregates 所有信号
3. **Entra ID = 统一数据库**：身份 + 设备 record + 合规状态都在这里（不在 Intune）
4. **Partner API**：Compliance Partner / MTD Partner / SAML/OIDC 三套，让生态接入

**对 GoCell 启示**：
- zt-extensibility 探讨文档的设计（中立契约 + 多 provider + 三种接入模式）**与微软高度一致**，方向正确
- winmdm Stage 2 提前定义 4 个中立契约（+1-2 周），让 winmdm 从 day 1 就是合格 device provider
- 长期可考虑 "GoCell Graph" 统一聚合层（不是 P0）
- 不照搬 SaaS / License / Purview / CSPM / Application Gallery 等微软特有形态

GoCell zt 不需要"复制微软栈"，而是**学习其架构模式 + 适配私有化全开源场景**。
