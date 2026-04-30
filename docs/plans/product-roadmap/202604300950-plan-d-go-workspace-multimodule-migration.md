# 方案 D 实施计划：从单仓单 module 迁移到 go workspace 多 module（全开源场景）

> 日期：2026-04-30
> 状态：实施计划（基于 `202604300930-repository-structure-decision.md` §4.6 全开源场景下的推荐）
> 适用前提：MDM / 零信任**完全开源**（Apache 2.0 / MIT）；如选择商业化，请改用同目录 `202604300930-...` §4.1-4.5 方案 C
> 关联：
> - `202604300930-repository-structure-decision.md`（仓库结构决策，含方案 D 推荐论据）
> - `202604300900-gocell-as-platform-foundation.md`（MDM / 零信任路线 + 时间估算）
> - `../engineering-baseline/202604300600-radical-lightweight-revision.md`（核心 10 落点 E1-E10）

---

## §1 决策回顾

**方案 D 核心**：单仓库 `github.com/ghbvf/gocell/` 内放置多个独立 Go module（go.work 编排），各 module 独立 SemVer，但跨 module refactor / 测试 / archtest 都在单仓库内完成。

**对比方案 C 商业化拆分**：
- ❌ 不需要 contract registry 工具（单仓库内 contract.yaml 路径直接解析）
- ❌ 不需要 federated archtest 工具（archtest 在 root 跑，跨 module 守卫）
- ❌ 不需要 SemVer 兼容性矩阵 CI（go.work 让本地 dev 永远用 sibling 最新版）
- ✅ 节省 framework 团队 2-3 个月工具投入

**业界对照**：Apache Camel / Linkerd2 / Kubernetes 主体 / Cilium 都走这条路（单仓库 multi-module 或单 module monorepo）。

---

## §2 当前状态 vs 目标状态

### 2.1 当前状态（v1.0 前）

```
github.com/ghbvf/gocell/                      单 go.mod
├── go.mod                                    module github.com/ghbvf/gocell
├── kernel/      runtime/    adapters/       framework
├── pkg/                                      共享工具库
├── cells/{accesscore,auditcore,configcore}   示例 cells
├── contracts/                                framework + 示例 contracts
├── assemblies/corebundle/                    示例 assembly
├── examples/{ssobff,todoorder,iotdevice}     示例项目
├── cmd/{gocell,corebundle}                   CLI + binary
├── tools/                                    archtest / metricschema 等
└── docs/                                     文档
```

import path 形态：`github.com/ghbvf/gocell/kernel/cell`、`github.com/ghbvf/gocell/pkg/errcode` 等。

### 2.2 目标状态（2028 Q1，零信任启动后）

```
github.com/ghbvf/gocell/                      单仓库（全开源 Apache 2.0）
├── go.work                                   workspace 定义（编排所有 module）
├── go.mod                                    module github.com/ghbvf/gocell（core，**顶层即 core**）
├── kernel/      runtime/    adapters/        core framework
├── pkg/                                      core 共享工具
├── cells/{accesscore,auditcore,configcore}   core 示例 cells
├── contracts/                                core 契约
├── assemblies/corebundle/                    core 示例 assembly
├── cmd/{gocell,corebundle}                   core CLI + binary
├── tools/                                    archtest / metricschema
├── docs/                                     全仓文档
│
├── examples/                                 ← **每个示例独立 module**（详见 §4.4）
│   ├── ssobff/                               module github.com/ghbvf/gocell/examples/ssobff
│   │   ├── go.mod
│   │   ├── main.go
│   │   └── ...
│   ├── todoorder/                            module github.com/ghbvf/gocell/examples/todoorder
│   │   ├── go.mod
│   │   └── ...
│   ├── iotdevice/                            module github.com/ghbvf/gocell/examples/iotdevice
│   │   └── go.mod
│   └── demo/                                 module github.com/ghbvf/gocell/examples/demo
│       └── go.mod
│
├── mdm/                                      ← MDM module（**复用 core 分层模式**，winmdm 第一客户）
│   ├── go.mod                                module github.com/ghbvf/gocell/mdm
│   ├── kernel/                               **mdm 业务领域抽象**（接口层，不是 framework kernel）
│   │   ├── protocol/                         MDMProtocolHandler 接口 + SyncML 数据类型
│   │   ├── pki/                              PKIIssuer 接口（WSTEP/SCEP）
│   │   └── deviceid/                         DeviceIdentityResolver 接口（unified_device_id）
│   ├── runtime/                              （可选）mdm 特有运行时编排
│   ├── adapters/                             **实现 mdm/kernel 接口**
│   │   └── mdmprotocol/windows/                实现 protocol.MDMProtocolHandler
│   │                                            （OMA-DM / SyncML / XCEP / WSTEP / MS-MDE Discovery）
│   │                                            P2 延后：wns / webrtc/pion / p2p/bittorrent /
│   │                                                     timeseries/timescaledb
│   ├── cells/                                  **依赖 mdm/kernel 接口，不直接 import adapters**
│   │   ├── rbaccell/                           （L1）角色权限管理
│   │   ├── pkicell/                            （L1）内部 CA + WSTEP/SCEP + 证书轮换
│   │   ├── deviceidentity/                     （L1）SMBIOS 哈希 → unified_device_id（替代旧 Reconciliation Worker）
│   │   ├── mdmcell/                            （L1+L2+L4）enroll / device / command 三 slice
│   │   ├── agentcell/                          （L1+L2+L4）enroll / device / checkin / taskdispatch 四 slice
│   │   ├── groupengine/                        （L3）dynrule / staticgroup / channelaware
│   │   ├── policycell/                         （L1+L2）engine / router 二 slice（通道无关派发）
│   │   ├── appcatalog/                         （L1）catalog / signing / scriptlib / s3dispatch
│   │   └── devicelifecycle/                    （L1）tombstone / cronsweep（基于 unified_device_id 状态机）
│   │   # 复用 core/cells/{accesscore,auditcore}，扩 slice 不在 mdm/cells 复制
│   ├── contracts/                            MDM 跨 cell 契约
│   ├── assemblies/                           winmdm 6 + 1 部署形态
│   │   ├── winmdmall/                        全部 cell（单 binary 兜底，开发 + SMB < 5K）
│   │   ├── wmcore/                           accesscore + auditcore + rbaccell + pkicell + deviceidentity
│   │   ├── wmmdm/                            mdmcell + adapters/mdmprotocol/windows
│   │   ├── wmagent/                          agentcell
│   │   ├── wmgroup/                          groupengine
│   │   ├── wmpolicy/                         policycell
│   │   └── wmasset/                          appcatalog + devicelifecycle
│   ├── examples/                             MDM 示例（每个独立 module）
│   │   └── winmdmsmoke/                      module github.com/ghbvf/gocell/mdm/examples/winmdmsmoke
│   │       ├── go.mod                        多 assembly 等价性烟测
│   │       └── ...
│   └── cmd/winmdm                            winmdm binary
│
└── zerotrust/                                ← 零信任 module（同 mdm 分层）
    ├── go.mod                                module github.com/ghbvf/gocell/zerotrust
    ├── kernel/                               零信任业务抽象
    │   ├── trust/                            TrustEvaluator 接口
    │   ├── policy/                           PolicyEngine 接口
    │   └── correlation/                      EventCorrelator 接口
    ├── runtime/                              （可选）零信任特有运行时
    ├── adapters/                             实现 zerotrust/kernel 接口
    │   ├── servicemesh/{istio,linkerd}         实现 mesh.Controller
    │   ├── idp/{oidc,saml,okta,azuread}        实现 idp.Provider
    │   ├── ml/{triton,sagemaker,vertex}        实现 ml.Inferencer
    │   └── siem/{splunk,elasticsearch,datadog} 实现 siem.Sink
    ├── cells/{iapgateway,sessionpolicy,trustscore,jitgrant,mtlscert,
    │         eventcorrelation,anomalydetection,compliancereporting}
    ├── contracts/                            零信任契约
    ├── assemblies/{zerotrustmonolith,zerotrustplatform}
    ├── examples/
    │   └── ztmvp/                            module github.com/ghbvf/gocell/zerotrust/examples/ztmvp
    │       └── go.mod
    └── cmd/zt                                零信任 binary
```

import path 形态：
- `github.com/ghbvf/gocell/kernel/cell` → **不变**（core 顶层 import path 保留）
- `github.com/ghbvf/gocell/pkg/errcode` → **不变**
- `github.com/ghbvf/gocell/mdm/cells/deviceregistry` → 新（MDM module 内部 cell）
- `github.com/ghbvf/gocell/zerotrust/cells/iapgateway` → 新（零信任 module 内部 cell）

### 2.3 关键设计决策：顶层 = core

**为什么 core 不放到 `core/` 子目录**：
- 现有所有 import 路径 `github.com/ghbvf/gocell/...` **零修改**
- 现有 cells/contracts/assemblies/examples/cmd 全部不动
- 类比 Kubernetes 顶层 = `k8s.io/kubernetes` 主 module
- mdm / zerotrust 是后加的「副 module」，不影响主 module

**代价**：顶层目录混杂（core 内容 + mdm/ + zerotrust/ 子目录），但比起改全仓 import 路径成本低得多。

---

## §3 目录结构设计

### 3.1 各 module 内部结构（统一规范）

每个 module（mdm / zerotrust）内部目录结构与 core 顶层保持一致（除 kernel/runtime 外）：

```
mdm/                                          MDM module
├── go.mod
├── go.sum
├── cells/                                    业务 cells
│   ├── deviceregistry/
│   │   ├── cell.go
│   │   ├── cell.yaml
│   │   ├── cell_gen.go              （E3 codegen 产出）
│   │   ├── slices/
│   │   │   ├── enroll/
│   │   │   │   ├── service.go
│   │   │   │   ├── service_test.go
│   │   │   │   ├── slice_gen.go
│   │   │   │   └── slice.yaml      （E4 codegen 后无需手写）
│   │   │   └── deregister/
│   │   └── internal/                 模块内私有
│   ├── deviceconfig/
│   └── ...
├── adapters/                                 MDM 专用 adapter
│   ├── mdmprotocol/
│   │   ├── omadm/
│   │   ├── apple/
│   │   └── android/
│   ├── timeseries/
│   ├── pki/
│   └── cdn/
├── contracts/                                MDM 契约
│   ├── http/...
│   └── event/...
├── assemblies/                               MDM 应用
│   ├── mdmmonolith/
│   ├── mdmmicroservice/
│   └── mdmedge/
├── examples/                                 （可选）MDM 示例项目
└── cmd/mdm/                                  MDM 服务 binary
    └── main.go
```

**关键规则（修订 2026-04-30）**：

mdm / zerotrust **复用 core 分层模式**（kernel + runtime + adapters + cells），原因是 CLAUDE.md 强制：「cells/ 依赖 kernel/ 和 runtime/，**不依赖 adapters/**（通过接口解耦）」。这条规则是 GoCell 架构的核心承诺，不能因为 module 边界放弃。

**framework kernel/runtime vs 业务 kernel/runtime 的区分**：

| 类型 | 位置 | 内容 | 是否复用 |
|---|---|---|---|
| **framework kernel**（不可复制）| 仅在 core | `cell.Cell` interface / metadata parser / assembly / outbox 接口 / idempotency 等 GoCell 框架契约 | mdm/zerotrust 直接 import core，**禁止复制** |
| **framework runtime**（不可复制）| 仅在 core | `bootstrap` 10-phase / `eventrouter` / `auth` / `http/router` 等 framework 运行时 | 同上 |
| **业务 kernel**（每个应用 module 自有）| `mdm/kernel/` / `zerotrust/kernel/` | mdm 业务领域抽象接口（如 `mdm/kernel/protocol.MDMProtocolHandler` / `mdm/kernel/pki.PKIIssuer`）| **必须有**，否则 cells 会直接 import adapters 违反 CLAUDE.md |
| **业务 runtime**（可选）| `mdm/runtime/` | mdm 特有运行时编排（如 mdm 协议 dispatcher）| 按需 |

**依赖方向**（每个 module 内部，与 core 一致）：
```
mdm/cells   →  依赖  →   mdm/kernel + core/kernel + core/runtime + core/pkg
mdm/runtime →  依赖  →   mdm/kernel + core/kernel + core/runtime
mdm/adapters → 实现  →   mdm/kernel 的接口（也可实现 core/kernel/outbox 等接口）
mdm/cells   ⊄   mdm/adapters    （CLAUDE.md 强制，archtest 守卫）
```

**其他规则**：
- mdm 可以有自己的 `pkg/`（mdm 专用工具）但应优先复用 core 的 pkg/
- mdm 的 contracts 引用 core contracts 时通过 contractUsages 跨 module（见 §8）

### 3.2 跨 module 文件不应出现的位置

| 文件类型 | 不应出现位置 | 理由 |
|---|---|---|
| **framework `kernel/cell`** / `kernel/metadata` / `kernel/assembly` 等 framework 契约定义 | mdm 或 zerotrust 内 | framework 真相源在 core，复制等于分叉 framework |
| **framework `runtime/bootstrap`** / `runtime/eventrouter` 等 framework 运行时 | mdm 或 zerotrust 内 | 同上 |
| 跨 module assembly | 不允许；跨 module 应用必须放在使用方 module 内（如 mdmmonolith assembly 包含 core 的 accesscore，则 assembly 放在 mdm/assemblies/mdmmonolith/） | assembly 是部署单元，归属使用方 |
| 跨 module contract 实现 | 不允许；contract.yaml 真相源属于实现 cell 所在的 module | 契约即代码所有权 |
| **业务领域 kernel**（如 `mdm/kernel/protocol`）| 不应在 core 出现 | core 是业务无关 framework，不应混入 MDM 业务抽象 |

### 3.3 docs/ 不分模块

`docs/` 仍在仓库根目录，不按 module 拆分。文档跨 module 描述自然（如 product-roadmap 同时谈 core / mdm / zerotrust）。

---

## §4 Module 划分

### 4.1 module 总览（v2.0 形态）

仓库内分 **4 类 module**：核心 / 应用 / 工具 / 示例。

#### 核心 + 应用 module（4 个）

| Module | go.mod path | 含 | 依赖 |
|---|---|---|---|
| **core** | `github.com/ghbvf/gocell` | kernel + runtime + adapters + pkg + 示例 cells（accesscore/auditcore/configcore）+ 示例 assemblies + cmd/{gocell,corebundle} + tools | 无 module 依赖（外部依赖如 OTel/yaml.v3） |
| **mdm** | `github.com/ghbvf/gocell/mdm` | MDM cells（9 新建：rbaccell + pkicell + deviceidentity + mdmcell + agentcell + groupengine + policycell + appcatalog + devicelifecycle）+ adapters/mdmprotocol/windows + MDM contracts + 6+1 assemblies（winmdmall + wmcore + wmmdm + wmagent + wmgroup + wmpolicy + wmasset）+ cmd/winmdm | core |
| **zerotrust** | `github.com/ghbvf/gocell/zerotrust` | 零信任 cells（8 个）+ 零信任 adapters（4 类）+ 零信任 contracts + 零信任 assemblies + cmd/zt | core + mdm（复用 deviceidentity / mdmcell / agentcell / pkicell / rbaccell / policycell / devicelifecycle） |
| **tools**（可选独立）| `github.com/ghbvf/gocell/tools` | gocell CLI plugin / archtest 扩展 / 第三方贡献者工具集 | core |

#### 示例 module（每个示例独立，详见 §4.4）

| Module | go.mod path | 含 | 依赖 |
|---|---|---|---|
| **examples/ssobff** | `github.com/ghbvf/gocell/examples/ssobff` | SSO BFF 完整示例（accesscore + auditcore + configcore 集成）| core |
| **examples/todoorder** | `github.com/ghbvf/gocell/examples/todoorder` | TodoOrder 业务示例 | core |
| **examples/iotdevice** | `github.com/ghbvf/gocell/examples/iotdevice` | IoT 设备 backend 示例（L4 DeviceLatent demo）| core |
| **examples/demo** | `github.com/ghbvf/gocell/examples/demo` | 最小 hello world 演示 | core |
| **mdm/examples/mdmmvp** | `github.com/ghbvf/gocell/mdm/examples/mdmmvp` | MDM MVP 示例（阶段 1 启动产出）| core + mdm |
| **zerotrust/examples/ztmvp** | `github.com/ghbvf/gocell/zerotrust/examples/ztmvp` | 零信任 MVP 示例（阶段 2 启动产出）| core + mdm + zerotrust |

> **tools 是否独立成 module 评估**：tools 当前在 core 顶层 `tools/` 目录。如果未来 archtest 工具被外部用户独立 import（不引整个 core），可以拆出。建议 v1.0 时仍在 core 内，按需后续拆分。

### 4.2 依赖图

```
core (foundation)
  ↑
  ├── mdm (depends on core)
  │     ↑
  │     └── zerotrust (depends on core + mdm)
  │
  └── zerotrust 也直接 depends on core
```

零信任 cell 复用：
- `core/cells/{accesscore,auditcore,configcore}` 的 contract（如 user-auth / audit-write）
- `mdm/cells/{deviceidentity,mdmcell,agentcell,pkicell,policycell,rbaccell,devicelifecycle}` 的 contract（设备身份 + 证书 + 策略 + 角色权限 + 设备状态）

### 4.3 module 不允许的反向依赖

| 不允许 | 理由 |
|---|---|
| core → mdm | core 是底座，不能依赖应用层 |
| core → zerotrust | 同上 |
| mdm → zerotrust | mdm 是零信任的前置，不能反向依赖 |
| 任何 module → tools（除 build 时） | tools 是开发期工具，不在运行时路径 |
| **任何 module → examples/* / mdm/examples/* / zerotrust/examples/*** | **examples 是消费方而非被依赖方**；core/mdm/zerotrust 不能反向 import examples |

archtest 加规则守卫这些反向依赖。

### 4.4 examples 多 module 设计

**为什么每个 example 独立 module（而非整个 examples/ 一个 module）**：

1. **依赖独立**：examples 引入的依赖（如 `stretchr/testify` / 特定 OIDC provider 库 / 业务 SDK）不应污染 core go.mod。如果整个 examples/ 一个 module，所有示例共享依赖，一个示例加新依赖牵连所有。
2. **每个示例 demo 不同形态**：ssobff demo monolith + OIDC；iotdevice demo L4 DeviceLatent + WebSocket；mdmmvp demo MDM 完整链路。各自依赖差异大。
3. **业界主流做法**：kubebuilder testdata（每个 project 独立 module）/ Watermill examples（每个独立 module）/ Temporal samples-go（每个 sample 独立 module）。
4. **不进入 SemVer 承诺**：examples 是教学材料，README 标注 `// reference only, no API stability guarantee`。各自独立 go.mod 让这点清晰。
5. **release 不发布**：examples 各 module 不需要 `go list -m` 公开发现，开发者用 `git clone` 看，不 `go get`。

**examples 的归属规则**：

| 示例类型 | 位置 | go.mod path |
|---|---|---|
| 仅用 core 的示例 | `examples/<name>/` | `github.com/ghbvf/gocell/examples/<name>` |
| 用 core + mdm 的示例 | `mdm/examples/<name>/` | `github.com/ghbvf/gocell/mdm/examples/<name>` |
| 用 core + mdm + zerotrust 的示例 | `zerotrust/examples/<name>/` | `github.com/ghbvf/gocell/zerotrust/examples/<name>` |

**归属原则**：示例依赖最远的 module 决定它的归属（`<deepest-dep-module>/examples/<name>/`）。这样 core 改动不会强制重测 mdm 示例，核心变化对应清晰。

**examples 内部不再有子模块**：每个 example 是一个 single-binary demo，只有 `main.go` + 一些 helper。不应在 example 内部再分 cells/contracts/assemblies（如果一个示例复杂到需要这些，说明它应该升级为 reference cell 进入 core/mdm/zerotrust 主体）。

**examples 的 examples_smoke 测试**：

- 每个 example 自带 `main_smoke_test.go`（build tag `//go:build examples_smoke`）
- CI workflow `examples-smoke` 跑所有 examples module 的 smoke test：
  ```bash
  for dir in examples/* mdm/examples/* zerotrust/examples/*; do
      if [ -f "$dir/go.mod" ]; then
          (cd "$dir" && go test -tags=examples_smoke ./...)
      fi
  done
  ```
- 触发条件：core / mdm / zerotrust 任一 module 改动 → 跑所有 dependent examples smoke

---

## §5 go.work + 各 go.mod 配置

### 5.1 仓库根 `go.work`

```
go 1.23.0

use (
    .                                       // core module（顶层 go.mod）
    ./mdm
    ./zerotrust
    ./tools

    // examples 各自独立 module
    ./examples/ssobff
    ./examples/todoorder
    ./examples/iotdevice
    ./examples/demo
    ./mdm/examples/mdmmvp
    ./zerotrust/examples/ztmvp
)
```

> `use .` 表示根目录的 go.mod 是 core module。所有 examples 各自一行 `use` 条目。新增示例时同步更新 go.work。

### 5.2 core `go.mod`（仓库根）

```go
module github.com/ghbvf/gocell

go 1.23.0

require (
    go.opentelemetry.io/otel v1.43.0
    go.uber.org/goleak v1.3.0
    github.com/jackc/pgx/v5 v5.x.x
    // ... 现有 framework 外部依赖
)
```

**保持不变**：当前所有 require 不动，外部依赖延续。

### 5.3 mdm `go.mod`

```go
module github.com/ghbvf/gocell/mdm

go 1.23.0

require (
    github.com/ghbvf/gocell v1.0.0    // core 依赖
    // ... MDM 专属外部依赖（如 mdmprotocol 协议库、PKI 库等）
)

// 本地 dev 时由 go.work 自动 redirect 到 sibling sibling
// release 时 go.work 不参与，require 的版本号生效
```

### 5.4 zerotrust `go.mod`

```go
module github.com/ghbvf/gocell/zerotrust

go 1.23.0

require (
    github.com/ghbvf/gocell v1.5.0          // core
    github.com/ghbvf/gocell/mdm v1.0.0      // 复用 device cells
    // ... 零信任专属外部依赖
)
```

### 5.5 各 module 独立 SemVer

| Module | 当前 | 目标（2028 Q3） |
|---|---|---|
| core | v0.x | v1.5.x（稳定） |
| mdm | （未存在） | v1.0.x（GA） |
| zerotrust | （未存在） | v0.5.x（早期） |

各自有独立 git tag：
- `v1.5.0`（core）
- `mdm/v1.0.0`（Go 多 module 子目录 tag 规范）
- `zerotrust/v0.5.0`

### 5.6 go.work 不进入 release 包

`go.work` + `go.work.sum` 仅本地 dev 用。CI 在跑 `go build ./...` 前应用以下策略之一：

- **方案 A**：CI 设置 `GOWORK=off`，强制各 module 用 go.mod 锁定版本（**推荐**，验证 release 一致性）
- **方案 B**：CI 用 `GOWORK=on`（与本地 dev 同），但要补 `make verify-release-tags` 跑一次 `GOWORK=off go build` 验证

---

## §6 import path 迁移策略

### 6.1 现有代码（core 内部）

**所有 `github.com/ghbvf/gocell/*` 路径不动**：
- `github.com/ghbvf/gocell/kernel/cell` ✅
- `github.com/ghbvf/gocell/runtime/bootstrap` ✅
- `github.com/ghbvf/gocell/pkg/errcode` ✅
- `github.com/ghbvf/gocell/cells/accesscore` ✅
- `github.com/ghbvf/gocell/contracts/...` ✅

**零修改**——这是顶层 = core 设计的最大收益。

### 6.2 新 mdm 代码

import path 自然落到 mdm module 路径：

```go
// mdm/cells/deviceregistry/cell.go
package deviceregistry

import (
    "context"

    // import core 的 framework + 工具
    "github.com/ghbvf/gocell/kernel/cell"
    "github.com/ghbvf/gocell/pkg/errcode"
    "github.com/ghbvf/gocell/runtime/observability/logging"

    // import mdm module 内部
    "github.com/ghbvf/gocell/mdm/contracts/event/deviceregistered/v1"
)
```

### 6.3 contract 引用跨 module

mdm 的 slice.yaml 引用 core 的 contract（通过 contractUsages）：

```yaml
# mdm/cells/deviceregistry/slices/enroll/slice.yaml （E4 codegen 后无需手写，由 marker 反推）
contractUsages:
  - contract: github.com/ghbvf/gocell/contracts/http/accesscore/userauth/v1
    role: depend       # 调用 core 的 user-auth contract 完成身份验证
  - contract: github.com/ghbvf/gocell/mdm/contracts/event/deviceregistered/v1
    role: produce      # 发布自己的事件
```

**实现要点**：`gocell validate` 解析 contractUsages 时，按 module path 前缀切分到对应 module 目录解析 contract.yaml。无需 contract registry 工具——单仓库内所有 contract.yaml 都在仓库 tree 内。

### 6.4 cmd/ 入口的 import

```go
// mdm/cmd/mdm/main.go
package main

import (
    "context"
    "os"

    "github.com/ghbvf/gocell/runtime/bootstrap"     // core 的 bootstrap
    "github.com/ghbvf/gocell/runtime/shutdown"      // core 的 shutdown

    // mdm assembly
    "github.com/ghbvf/gocell/mdm/assemblies/mdmmonolith"
)

func main() {
    ctx, cancel := shutdown.NotifyContext(context.Background())
    defer cancel()

    must(bootstrap.Run(ctx, mdmmonolith.Config(), mdmmonolith.Cells()...))
}
```

---

## §7 archtest 跨 module 配置

### 7.1 archtest 运行位置

archtest 工具（`tools/archtest/`）在 core module 内提供，但**在仓库根目录运行**，通过 go.work 看到所有 module 的 AST。

```bash
# 仓库根目录
$ go test ./tools/archtest/...
PASS  ./tools/archtest/layer_test.go
PASS  ./tools/archtest/cross_module_test.go    新增
```

### 7.2 跨 module LAYER 规则（新增）

`tools/archtest/cross_module_test.go` 守卫：

| 规则 | 检查 |
|---|---|
| **CM-LAYER-01** | core/* 不能 import `github.com/ghbvf/gocell/mdm/*` |
| **CM-LAYER-02** | core/* 不能 import `github.com/ghbvf/gocell/zerotrust/*` |
| **CM-LAYER-03** | mdm/* 不能 import `github.com/ghbvf/gocell/zerotrust/*` |
| **CM-LAYER-04** | mdm/cells/<X>/internal/ 只能被 mdm/cells/<X>/ 自身 import（cell internal 隔离，跨 cell 仍走 contract）|
| **CM-LAYER-05** | zerotrust/cells/<X>/internal/ 同上 |
| **CM-LAYER-06** | mdm/* / zerotrust/* 不能在 cell 之间 import 兄弟 cell 的 internal/（即使同 module）|
| **CM-LAYER-07** | 任何 module 都不能 import core/cells/{accesscore,...}/internal/（cell internal 跨 module 仍隔离）|
| **CM-LAYER-08** | **mdm/cells/* 不能 import mdm/adapters/***（CLAUDE.md "cells 不依赖 adapters" 跨 module 守卫）|
| **CM-LAYER-09** | **zerotrust/cells/* 不能 import zerotrust/adapters/***（同上） |
| **CM-LAYER-10** | **mdm/kernel/* 不能 import mdm/runtime/***、mdm/adapters/*、mdm/cells/*（kernel 是抽象层，不依赖运行时和实现） |
| **CM-LAYER-11** | **zerotrust/kernel/* 不能 import zerotrust/runtime/***、zerotrust/adapters/*、zerotrust/cells/*（同上） |
| **CM-LAYER-12** | mdm/runtime/* 不能 import mdm/adapters/*、mdm/cells/*（runtime 在 cell 之下，不能反向依赖） |
| **CM-LAYER-13** | zerotrust/runtime/* 不能 import zerotrust/adapters/*、zerotrust/cells/*（同上） |

### 7.3 现有 LAYER 规则（不变）

core 内部的 LAYER-01 到 LAYER-10 规则保持不变（kernel ⊄ runtime ⊄ cells 等）。

### 7.4 archtest 配置文件

`tools/archtest/config.yaml`（新增）：

```yaml
modules:
  - path: .
    name: core
    role: foundation
  - path: ./mdm
    name: mdm
    role: application
    dependsOn: [core]
  - path: ./zerotrust
    name: zerotrust
    role: application
    dependsOn: [core, mdm]
  - path: ./tools
    name: tools
    role: tooling
    dependsOn: [core]

layerRules:
  # core 内部规则不变（参考现有 LAYER-01~10）

  # 跨 module 规则
  - name: CM-LAYER-01
    forbid:
      - from: github.com/ghbvf/gocell
        to: github.com/ghbvf/gocell/mdm
        except: []
  # ... 其余 CM-LAYER 规则
```

---

## §8 contracts 跨 module 引用

### 8.1 contractUsages 路径表达

```yaml
contractUsages:
  - contract: github.com/ghbvf/gocell/contracts/http/accesscore/userauth/v1   # core 契约
    role: depend
  - contract: github.com/ghbvf/gocell/mdm/contracts/event/deviceregistered/v1  # mdm 自身契约
    role: produce
  - contract: github.com/ghbvf/gocell/mdm/contracts/event/devicedeleted/v1     # mdm 跨 cell 引用
    role: subscribe
```

### 8.2 boundary.yaml 跨 module 表达

每个 module 自己有 `boundary.yaml`：

```yaml
# mdm/assemblies/mdmmonolith/generated/boundary.yaml
exportedContracts:
  - github.com/ghbvf/gocell/mdm/contracts/event/deviceregistered/v1
  - github.com/ghbvf/gocell/mdm/contracts/http/devicemgmt/registerdevice/v1
importedContracts:
  - github.com/ghbvf/gocell/contracts/http/accesscore/userauth/v1   # 从 core 进口
sourceFingerprint: a3b5c8...   # 单 module 内 fingerprint
```

### 8.3 ADV-06 双向校验跨 module

`gocell validate --strict` 扩展为：
- 解析 contractUsages 中的 module path 前缀（`github.com/ghbvf/gocell/mdm/...`）
- 切分到对应 module 目录读取 contract.yaml
- 双向校验 endpoints.subscribers ↔ contractUsages[role=subscribe]
- 单仓库内 import 路径全部存在，无需远程查询

### 8.4 fingerprint 跨 module

每个 module 维护自己的 boundary.yaml fingerprint。跨 module contract 变化通过：
- 上游 module 的 contract.yaml 变化 → 上游 boundary.yaml fingerprint 变化
- 下游 module 的 importedContracts 引用上游 fingerprint，下游 boundary.yaml fingerprint 也变化
- 跨 module diff gate：CI 跑全仓 `make verify-codegen`，任何 module 的 boundary.yaml 漂移即 fail

---

## §9 CI/CD 调整

### 9.1 当前 CI（单 module）

```yaml
# .github/workflows/_build-lint.yml
jobs:
  build-lint:
    steps:
      - go build ./...
      - golangci-lint run ./...
      - go test ./...
      - go test -tags=integration ./...
```

### 9.2 多 module CI（path-based filter）

```yaml
# .github/workflows/_build-lint.yml
jobs:
  detect-changes:
    outputs:
      core: ${{ steps.filter.outputs.core }}
      mdm: ${{ steps.filter.outputs.mdm }}
      zerotrust: ${{ steps.filter.outputs.zerotrust }}
    steps:
      - uses: dorny/paths-filter@<sha>
        id: filter
        with:
          filters: |
            core:
              - '!(mdm|zerotrust|tools)/**'
            mdm:
              - 'mdm/**'
            zerotrust:
              - 'zerotrust/**'
            tools:
              - 'tools/**'

  build-core:
    needs: detect-changes
    if: needs.detect-changes.outputs.core == 'true'
    steps:
      - go build ./
      - golangci-lint run .
      - go test ./
      - go test -tags=integration ./

  build-mdm:
    needs: detect-changes
    if: needs.detect-changes.outputs.mdm == 'true' || needs.detect-changes.outputs.core == 'true'
    # core 改动 → mdm 也要重测，因为 mdm 依赖 core
    steps:
      - cd mdm && go build ./...
      - cd mdm && golangci-lint run ./...
      - cd mdm && go test ./...

  build-zerotrust:
    needs: detect-changes
    if: needs.detect-changes.outputs.zerotrust == 'true' || needs.detect-changes.outputs.mdm == 'true' || needs.detect-changes.outputs.core == 'true'
    # core 或 mdm 改动 → zerotrust 也要重测
    steps:
      - cd zerotrust && go build ./...
      - cd zerotrust && golangci-lint run ./...
      - cd zerotrust && go test ./...

  archtest:
    # 跨 module archtest 始终跑（任何 module 改动）
    needs: detect-changes
    if: needs.detect-changes.outputs.core == 'true' || needs.detect-changes.outputs.mdm == 'true' || needs.detect-changes.outputs.zerotrust == 'true'
    steps:
      - go test ./tools/archtest/...

  release-version-check:
    # GOWORK=off 验证 release-pin 一致性
    if: github.ref == 'refs/heads/main'
    steps:
      - GOWORK=off go build ./
      - cd mdm && GOWORK=off go build ./...
      - cd zerotrust && GOWORK=off go build ./...
```

### 9.3 节省的 CI 时间

只改 `mdm/cells/deviceregistry` 一个文件：
- 当前 CI：跑全仓 5 分钟
- 多 module CI：跑 mdm + archtest，约 1.5 分钟
- 节省：~70%

只改 `core/cells/accesscore`：
- 当前 CI：跑全仓 5 分钟
- 多 module CI：跑 core + mdm + zerotrust + archtest（因为 mdm/zt 依赖 core），约 6 分钟
- 略增：因为依赖传递性

---

## §10 转换路径（3 阶段）

### 阶段 0：v1.0 前（2026 Q2-Q4）

**保持当前形态不变**，单 go.mod 单仓库。重点是 framework 自身演进（E1-E10 + E14）。

**禁止动作**：不要预先创建 mdm/ 或 zerotrust/ 空目录；不要预先建 go.work；不要预先做 module 拆分。

### 阶段 1：MDM 启动（2027 Q1）

**触发条件**：core v1.0 GA + 决定启动 MDM Phase 1。

**转换 PR**（一次性切，不留过渡）：

#### PR A1.1：建立 go workspace + mdm module 骨架 + examples 多 module 化

```
新增文件：
  + go.work                              workspace 编排
  + mdm/go.mod                          module github.com/ghbvf/gocell/mdm
                                         require github.com/ghbvf/gocell v1.0.0
  + mdm/.gitignore
  + mdm/README.md

  # examples 各自转独立 module（同 PR 切，不留 mixed 状态）
  + examples/ssobff/go.mod              module github.com/ghbvf/gocell/examples/ssobff
                                         require github.com/ghbvf/gocell v1.0.0
  + examples/todoorder/go.mod           module github.com/ghbvf/gocell/examples/todoorder
                                         require github.com/ghbvf/gocell v1.0.0
  + examples/iotdevice/go.mod           module github.com/ghbvf/gocell/examples/iotdevice
                                         require github.com/ghbvf/gocell v1.0.0
  + examples/demo/go.mod                module github.com/ghbvf/gocell/examples/demo
                                         require github.com/ghbvf/gocell v1.0.0

修改文件：
  ~ go.mod（core 顶层）                   清理只 examples 用到的 require（如 stretchr/testify
                                         如果只在 examples 测试中用，迁到各 example go.mod）
  ~ Makefile                             加 mdm-build / mdm-test / mdm-lint
                                         加 examples-smoke 多 module 跑法
  ~ .github/workflows/_build-lint.yml    加 multi-module 路径过滤
  ~ .github/workflows/examples-smoke.yml 改为多 module 矩阵
  ~ tools/archtest/config.yaml           加 modules + cross-module rules
  ~ tools/archtest/cross_module_test.go  CM-LAYER-01~07（含 examples 反向依赖守卫）
  ~ docs/...                             路径调整（用 module path 引用）

零迁移代码：core 顶层（kernel/runtime/adapters/pkg/cells/contracts/assemblies/cmd/tools）全部不动。
零迁移代码：examples/ 内 *.go 文件全部不动（仅新增 go.mod）。
```

> **核心要点**：core 仍然在仓库顶层（**没有 core/ 子目录**，import path `github.com/ghbvf/gocell/...` 全部不变）。本 PR 只是把 examples + mdm 拉成独立 module，core 自身路径零修改。

#### PR A1.2-A1.N：winmdm cells 渐进开发（按 §3.7 Stage 1-4 串行）

> **开发顺序**：基础设施 → MDM 通道 → Agent 通道 → 上层应用（先 MDM 后 Agent，避免同时改基础设施引发一致性问题）。详见 `202604301030-winmdm-prd-on-gocell.md` §4。

**Stage 1 - 基础设施（2027 Q1）**：
- PR A1.2：rbaccell（rolemgmt / permcheck / datascope）+ wmcore assembly 骨架
- PR A1.3：accesscore.{jwtlifecycle, ssooidc} 扩 slice（在 core 内，不在 mdm）+ auditcore.approvalflow 扩 slice
- PR A1.4：pkicell（wstep / scep / caworkflow / rotation）+ adapters/mdmprotocol/windows 骨架
- PR A1.5：deviceidentity（resolve / bind / lookup）+ unified_devices 表

**Stage 2 - MDM 通道（2027 Q2-Q3）**：
- PR A1.6：mdmcell.enroll（Discovery + XCEP + WSTEP 完整链路）+ adapters/mdmprotocol/windows.enroll
- PR A1.7：mdmcell.device（DevDetail CSP 采集）+ adapters/mdmprotocol/windows.devdetail
- PR A1.8：mdmcell.command（Wipe/Lock/Reboot 状态机 + 24h 审批接入 auditcore.approvalflow）
- PR A1.9：wmmdm assembly + 100 台 Win10/11 真机 E2E

**Stage 3 - Agent 通道（2027 Q3-Q4）**：
- PR A1.10：agentcell.enroll（MSI 签名校验 + WinVerifyTrust + 防降级）
- PR A1.11：agentcell.device + agentcell.checkin（15min 短轮询）
- PR A1.12：agentcell.taskdispatch（订阅 policy.assigned.v1 中 channel ∈ {agent, hybrid}）
- PR A1.13：wmagent assembly + Agent MSI 注册 E2E

**Stage 4 - 上层应用（2027 Q4 - 2028 Q1）**：
- PR A1.14：groupengine（dynrule / staticgroup / channelaware）+ wmgroup assembly
- PR A1.15：policycell（engine + router，通道无关派发）+ wmpolicy assembly
- PR A1.16：appcatalog（catalog / signing / scriptlib / s3dispatch）+ wmasset assembly
- PR A1.17：devicelifecycle（tombstone / cronsweep）+ winmdmall 兜底单 binary

**P0 (GoCell v1.0 前置，非 mdm module 内)**：
- PR P0.1：runtime/circuitbreaker（熔断三态 + 重试退避 + 幂等键）
- PR P0.2：accesscore JWT 完整生命周期（access 1h + refresh 7d/30d + 轮换 + 黑名单 + DPAPI 接入点）

每个 PR 自带：cell + 单元测试 + 集成测试 + assembly 更新 + winmdmsmoke 等价性测试。

**winmdm v1 GA 产物**（M4 2028 Q1）：
- `github.com/ghbvf/gocell` v1.0.x（core 不变）
- `github.com/ghbvf/gocell/mdm` v0.x.x → v1.0.0（GA）
- 11 cell + 1 adapter（mdmprotocol/windows）+ 6+1 assembly

### 阶段 2：零信任启动（2028 Q1）

**触发条件**：MDM v1 GA + 决定启动零信任 Phase 5。

#### PR Z1.1：建立 zerotrust module 骨架

```
新增：
  + zerotrust/go.mod                    module github.com/ghbvf/gocell/zerotrust
                                         require github.com/ghbvf/gocell vX.Y.Z
                                         require github.com/ghbvf/gocell/mdm vA.B.C
  + zerotrust/.gitignore
  + zerotrust/README.md

修改：
  ~ go.work                              加 ./zerotrust
  ~ .github/workflows/...                加 build-zerotrust job
  ~ tools/archtest/config.yaml           加 zerotrust module
```

#### PR Z1.2-Z1.N：零信任 cells 渐进开发

类似 MDM 阶段，每个 PR 加 1-3 个零信任 cell。

**零信任 Phase 5 完成产物**：
- `github.com/ghbvf/gocell` v1.x.x
- `github.com/ghbvf/gocell/mdm` v1.0.x
- `github.com/ghbvf/gocell/zerotrust` v0.x.x → v0.5.x

### 阶段 3：tools module 拆分（2028 Q3+，按需）

**触发条件**：archtest / metricschema 工具被外部用户独立 import 的需求出现。

如果不出现，tools/ 保留在 core 内部不动。

---

## §11 与 E1-E10 路线图协同

方案 D 多 module 转换与 E1-E10 核心简化路线**完全独立**，可并行不冲突。

### 11.1 时间线整合

```
2026 Q2  ─ 2026 Q3  ─ 2026 Q4               2027 Q1            2027 Q2-Q3       2027 Q3-Q4      2027 Q4-2028 Q1     2029 Q1+
│         │         │                        │                  │                 │                │                   │
│ E1-E2   │ E3-E4   │ E5-E10 + P0 5 项       │ Stage 1          │ Stage 2 MDM    │ Stage 3 Agent  │ Stage 4 上层      │ 零信任启动
│ 核心    │ codegen │ Windows MDM 协议       │ 基础设施         │ 通道           │ 通道          │ 应用              │ 加 zerotrust
│ 简化    │         │ + WSTEP + JWT 完整    │ + 转 go.work    │ wmmdm          │ wmagent       │ wmgroup           │ module
│         │         │ + 熔断 + 方案 D 切换  │ + mdm module    │                │                │ wmpolicy          │
│         │         │                       │   骨架（A1.1）  │                │                │ wmasset           │
│         │         │                       │ + Stage 1 PR    │                │                │ winmdmall 兜底    │
│         │         │                       │   A1.2-A1.5     │                │                │ → winmdm v1 GA    │
│         │         │                       │                  │                │                │   (M4 2028 Q1)    │
│         │         │                       │                  │                │                │                   │
└─ E1-E10 + P0 P1 5 项（core module 内）─────└─ 多 module 切换 ─└─ MDM cells ──┴─ Agent cells ──┴─ 上层 cells ──────┴─ 零信任 module
                                              （PR A1.1）        Stage 2 PR     Stage 3 PR        Stage 4 PR
```

### 11.2 每个 E 落点 module 归属

| 落点 | 影响 module | 注 |
|---|---|---|
| E1-E10 | **仅 core**（顶层 module 内部）| 阶段 0 完成，不涉及 module 拆分 |
| E14 库化承诺 | **core 内 25 包**（pkg + runtime + kernel 子包）| 阶段 0 完成 |
| E11 errcode 收敛 | **仅 core/pkg/errcode** | 阶段 0 |
| E12 Actor 合并（不建议）| 仅 core | — |
| E13 assembly 极简 | core + mdm + zerotrust（每个 module 自己的 assembly.yaml）| 各阶段独立 |
| 方案 D PR A1.1 多 module 切换 | 仓库根 + tools/ + 新建 mdm/ | 阶段 1 |

### 11.3 E1-E10 不会因为 module 拆分而需要修改

E1-E10 的所有改动都在 core 内部（kernel/runtime/pkg），多 module 切换不涉及这些路径。

---

## §12 风险评估 + 缓解

| 风险 | 严重度 | 缓解 |
|---|---|---|
| **go.work 多 module IDE 支持瑕疵** | 中 | 2026 Q4 v1.0 时 go.work 已发布 5+ 年，gopls / GoLand / VSCode 应完全成熟；如有边角问题可贡献上游 |
| **跨 module test 跑不全** | 中 | 多 module CI 配置好 dependsOn 触发；archtest 始终全仓跑；本地 `go test ./...` 在 workspace 模式下覆盖全部 module |
| **import path 冲突** | 低 | 顶层 = core 设计避免了大部分冲突；mdm/zerotrust 是新路径不会冲突 |
| **跨 module refactor 难度** | 低 | 单仓库内 IDE rename 直接生效（只要 gopls 看到全 workspace） |
| **GOWORK=off 与 GOWORK=on 行为差异** | 中 | CI 加 release-version-check job 强制跑 GOWORK=off；本地 dev 用 GOWORK=on 更方便 |
| **cells/contracts 路径错位** | 中 | E4 marker codegen + verify-gate 守卫每个 module 独立 boundary.yaml |
| **MDM PR 量级失控** | 中 | 阶段 1 切片建议每周 1-2 PR；阶段 2 同样节奏；2027 全年 + 2028 上半年共 30-40 个 PR |
| **跨 module 单元测试 setup 复杂** | 低 | go.work 让测试时所有 module 直接可见，不需要 mock framework |

---

## §13 时间估算

### 13.1 多 module 切换工作量（阶段 1 启动 PR A1.1）

| 任务 | 工作量 |
|---|---|
| 创建 go.work + mdm/go.mod | 1 天 |
| Makefile 多 module 改造 | 1 天 |
| GitHub Actions 多 module 改造 + 验证 | 2-3 天 |
| archtest 跨 module 规则 + 测试 | 2-3 天 |
| 文档更新（README + CLAUDE.md + 各 module README） | 1-2 天 |
| 端到端验证（本地 + CI 全跑通）| 1 天 |
| **总计 PR A1.1** | **8-11 天**（约 2 个工程周） |

### 13.2 各阶段总时间（与 §11 路线图一致）

| 阶段 | 时长 | 主要工作 |
|---|---|---|
| 阶段 0：v1.0 完成 | 6 个月（2026 Q2-Q4）| E1-E10 + E14 |
| 阶段 1：MDM 启动 + 多 module 切换 | 2 周（PR A1.1）+ 12 个月（MDM 开发，PR A1.2-A1.N）| §13.1 + MDM cells |
| 阶段 2：零信任启动 + 加 module | 1 周（PR Z1.1）+ 18 个月（零信任开发）| zerotrust cells |
| 阶段 3：tools 拆分（可选）| 按需 | 仅当外部需求出现 |

**与 §11 整合**：
- 多 module 切换工作量极小（2-3 周一次性切），不影响整体路线 30-40 个月预算
- 主体时间花在 cells / adapters 实现，多 module 仅是组织结构调整

---

## §14 决策点（待确认）

- [ ] **是否同意方案 D 多 module 路径**？（前提：MDM/零信任完全开源 Apache 2.0 / MIT）
- [ ] **顶层 = core 设计是否同意**？（vs core/ 子目录，前者迁移成本低 0；后者目录更整齐但要改全仓 import path）
- [ ] **module 切分粒度**：core / mdm / zerotrust / tools 4 个是否合理？或只切 core / mdm / zerotrust 3 个（tools 留在 core 内）？
- [ ] **PR A1.1 多 module 切换 PR 是否同意作为 MDM Phase 1 的第一个 PR**？
- [ ] **archtest 跨 module 规则 CM-LAYER-01~07 是否同意**？补充别的规则？
- [ ] **CI 节省的 ~70% 时间**是否需要进一步加速（如 module-level 缓存）？建议作为后续优化项不在阶段 1 PR A1.1 范围

---

## §15 总结

**方案 D 实施核心**：

1. **v1.0 前不动**：保持单 go.mod 现状，重点 E1-E10 演进
2. **2027 Q1 一次性切换**：PR A1.1 建 go.work + mdm/ module 骨架，**8-11 天工作量**
3. **顶层 = core 设计**：现有 import path 零修改，迁移成本最低
4. **archtest 跨 module 规则 7 条**：守卫 module 间正确依赖方向
5. **CI 多 module 路径过滤**：节省 ~70% CI 时间（典型场景）
6. **业界对照**：Apache Camel / Linkerd2 / Kubernetes 主体模式

**节省的工程投入（vs 方案 C 商业化拆分）**：
- 不需要 contract registry 工具（节省 1-2 个月）
- 不需要 federated archtest 工具（节省 1-2 个月）
- 不需要 SemVer 兼容性矩阵 CI（节省 0.5 个月）
- 总节省 **2.5-4.5 个月 framework 团队投入**

这些节省的时间可投入到：
- 更早的 MDM Phase 1 启动（提前 2-3 个月）
- 或更深的 E11/E13/E14 落地（错误库 + assembly 极简 + 库化承诺质量）
- 或 examples/ 增加更多 reference 实现（如 mdmmvp 示例）

**关键约束**：必须保持全开源（Apache 2.0 / MIT）。任何商业化决策出现 → 立即转方案 C（参考 `202604300930-...` §4.1-4.5）。
