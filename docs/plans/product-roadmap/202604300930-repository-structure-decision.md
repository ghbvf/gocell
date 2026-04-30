# GoCell + winmdm + 零信任 仓库结构决策

> 日期：2026-04-30
> 状态：**已决策**（2026-04-30 确认全开源 MIT → 走方案 D 单仓库 go workspace 多 module）
> 关联：
> - `202604300900-gocell-as-platform-foundation.md`（GoCell → winmdm → 零信任路线 + 时间估算）
> - `202604300950-plan-d-go-workspace-multimodule-migration.md`（方案 D 实施计划）
> - `202604301030-winmdm-prd-on-gocell.md`（winmdm 第一客户 PRD）
> - `../engineering-baseline/202604300600-radical-lightweight-revision.md`（核心 10 落点）
> - `../engineering-baseline/202604300800-final-form-capability-overview.md`（25 包库化承诺）

---

## §0 决策结论（2026-04-30 锁定）

**选方案 D 单仓库 go workspace 多 module + 全开源 MIT**：

- 仓库：`github.com/ghbvf/gocell/`
- module：`core`（顶层）/ `mdm`（含 winmdm 全部代码）/ `zerotrust`（2029 Q1+）
- 授权：MIT（所有产品 / 项目统一）
- v1.0 前保持单 module monorepo（当前形态），2027 Q1 winmdm 启动时一次性切换到 go.work

**§4.1-4.5 商业化方案 C 路径已弃用**（仅作为历史参考，不再执行）。商业化场景假设（HashiCorp 模式 / BSL / 闭源 / contract registry / federated archtest）全部不适用。

**§4.6 全开源场景方案 D 已采纳**，详细实施见 `202604300950-plan-d-go-workspace-multimodule-migration.md`。

---

## §1 5 个候选方案

### 方案 A：单一 monorepo（当前形态延续）

```
github.com/ghbvf/gocell/                     单 go.mod
├── kernel/ runtime/ adapters/ pkg/         framework
├── cells/                                   示例 + MDM + 零信任所有 cell
│   ├── accesscore/                         示例
│   ├── deviceregistry/                    MDM
│   ├── pkicell/                           MDM/零信任共用
│   └── iapgateway/                        零信任
├── contracts/                               所有契约
└── assemblies/
    ├── corebundle/                         示例
    ├── mdmmonolith/                       MDM 应用
    └── zerotrustplatform/                 零信任应用
```

**优点**：
- archtest LAYER 跨所有 cell 直接守卫
- 单 go.mod 依赖管理简化
- 跨 cell contract fingerprint diff 单仓内闭环
- 重构 + 跨 cell refactor 方便（IDE rename 直接生效）

**缺点**：
- 仓库变成几十万行级巨型 monorepo
- 商业产品代码（MDM/零信任）与开源 framework 混在一起，**商业模式被锁死为开源**
- 团队权限难划分（framework 维护者 vs 产品开发者权限相同）
- CI 时间长：改 1 行 framework → 触发全仓 lint/test/archtest（半小时起跳）
- release 节奏锁定：framework 与 MDM 必须同 release

### 方案 B：完全独立仓库（3 个 repo）

```
github.com/ghbvf/gocell/                     framework
github.com/ghbvf/gocell-mdm/                 MDM 产品
github.com/ghbvf/gocell-zerotrust/           零信任产品
```

**优点**：
- 商业 vs 开源边界清晰（MDM/零信任可闭源 / 私有 repo / 商业授权）
- 每仓 release 独立
- 团队权限独立
- CI 范围清晰

**缺点**：
- archtest LAYER 失效（无法跨 repo 静态守卫）
- 跨 cell contract 双向校验需要 contract registry 远程同步工具
- framework 改动 → MDM 升级 → 零信任升级 三级协同 release 复杂
- 测试 + 集成测试跨 repo setup（需要先 publish framework 再消费）
- 跨 repo refactor 难（go.mod 锁版本，重命名要协同）

### 方案 C：核心开源 + 商业产品独立（HashiCorp 模式）

```
github.com/ghbvf/gocell/                     framework 开源（kernel + runtime + adapters + pkg + 示例 cells）
                                            含 reference assemblies/corebundle 作为最小可用示例
github.com/ghbvf/gocell-mdm/                 MDM 商业（可选闭源 / 私有 repo / Apache 双授权）
github.com/ghbvf/gocell-zt/                  零信任商业（同上）
github.com/ghbvf/gocell-extensions/          社区扩展集（第三方 adapter / 第三方 cells，开源）
```

**优点**：
- 商业 vs 开源边界清晰
- framework 开源稳定 + 商业产品独立演进
- 类比 HashiCorp（Terraform 开源 + Vault Enterprise / HCP 商业）模式经过验证
- archtest 跨 repo 治理可由 framework 提供 federated archtest 工具支持
- 各产品 release 节奏独立，但通过 SemVer 兼容性矩阵约束

**缺点**：
- 跨 repo refactor 需要多 PR 协同（framework PR → 等 framework release → 各产品仓库 bump version → 各自 PR）
- contract registry 需要建设（远程 contract.yaml 解析 + 缓存）
- federated archtest 需要工具支持

### 方案 D：单仓库 + go workspace 多 module

```
github.com/ghbvf/gocell/                     单仓库
├── go.work
├── core/                                    go.mod = github.com/ghbvf/gocell/core
├── mdm/                                     go.mod = github.com/ghbvf/gocell/mdm
├── zerotrust/                               go.mod = github.com/ghbvf/gocell/zerotrust
└── tools/                                   go.mod = github.com/ghbvf/gocell/tools
```

**优点**：
- 单仓库便于跨 module refactor
- 各 module 独立 SemVer（go.mod 各自版本号）
- archtest LAYER 跨 module 工作（同 workspace）
- go workspace 自 1.18 起官方支持

**缺点**：
- go workspace 多 module 在某些工具链（如部分 IDE / linter / coverage 工具）支持仍有边角问题
- 商业产品代码仍在同一仓库 → **商业模式仍受限**（除非用 git 子目录权限切割，但脆弱）
- 多 module 加重新人理解成本（需要懂 go.work + replace directive）
- 多 module + monorepo 在 Go 社区是少数派做法（CloudWeGo 是单仓多 module 但所有都开源）

### 方案 E：Plugin 模式（Terraform Provider 模式）

```
github.com/ghbvf/gocell/                     framework 核心 + 1-2 个 reference cells
github.com/ghbvf/gocell-cells-mdm/           独立 MDM cells 集合（多个 cell 一个 repo）
github.com/ghbvf/gocell-cells-zt/            独立 零信任 cells 集合
github.com/ghbvf/gocell-adapters-postgres/   各种 adapter 单独 repo
github.com/ghbvf/gocell-adapters-redis/
github.com/ghbvf/gocell-protocol-omadm/      MDM 协议 adapter
```

**优点**：
- 极致解耦，每个能力独立 release
- 类似 Terraform Provider 生态
- 第三方贡献者可独立维护某个 adapter 或 cells 集合

**缺点**：
- 仓库数量爆炸（v1.0 时可能 10+ repo，零信任阶段 30+ repo）
- 跨 repo 协同极复杂
- 对小团队（<10 人）维护成本过高
- 用户使用前要 import 多个 module（运维麻烦）

---

## §2 业界对照

| 项目 | 形态 | 仓库结构 | 何时拆分 |
|---|---|---|---|
| **Kubernetes** | 巨型 monorepo（kubernetes/kubernetes）+ 工具独立（kubebuilder / controller-tools / 等独立） | 方案 A 主体 + 方案 E 工具 | 工具因可独立使用而拆出 |
| **HashiCorp Terraform** | Terraform 核心（github.com/hashicorp/terraform）+ 每个 Provider 独立仓库（github.com/hashicorp/terraform-provider-aws 等数百个）| 方案 E 极致 | Provider 数量大 + 第三方贡献需要 |
| **HashiCorp Vault** | Vault 核心 + Vault plugins 独立仓库（vault-plugin-secrets-* / vault-plugin-auth-*） | 方案 C / E 混合 | Plugin 商业化 + 第三方扩展 |
| **CloudWeGo** | Kitex / Hertz / Netpoll 各独立仓库（同 org） | 方案 B 变体 | 各自定位独立（RPC vs HTTP vs 网络库） |
| **Temporal** | Temporal core（github.com/temporalio/temporal）+ Go SDK / Java SDK / Python SDK 各自独立 | 方案 B | 多语言 SDK 必须独立 |
| **uber-go** | fx / dig / multierr / atomic / zap 等数十个独立仓库 | 方案 E | 独立通用库 |
| **Spring Cloud** | spring-cloud-* 系列模块化仓库 | 方案 C / E | Java 多模块 + 商业演进 |

**关键观察**：
- 巨型 framework（K8s）走 monorepo + 工具拆分；其余产品（Vault / Terraform / Temporal）都拆分
- 商业化产品**全部**都做了仓库分离（核心 OSS + 商业产品独立）
- 没有任何业界对照支持「商业产品 + framework 同仓库」的模式

---

## §3 GoCell 关键决策因素

| 因素 | 当前状态 | 影响 |
|---|---|---|
| **商业模式** | 未确定（建议 HashiCorp 模式：framework 开源 + MDM/零信任商业）| 决定能否同仓库（商业不能与开源混） |
| **团队规模** | 未知（假设 4-10 人）| 小团队倾向少仓库；大团队倾向多仓库 |
| **archtest LAYER 跨界治理** | 当前单 repo 完整守卫；跨 repo 需要 federated archtest 工具 | 决定多 repo 方案的工具投入 |
| **Contract 跨界一致性** | 当前 boundary.yaml fingerprint 单仓内闭环；跨 repo 需要 contract registry | 决定多 repo 方案的工具投入 |
| **Release 节奏** | 当前单 SemVer；MDM/零信任商业产品 release 节奏可能不同 | 决定是否锁同步 release |
| **Refactor 频率** | framework v1.0 前频繁；v1.0 后稳定 | v1.0 前倾向单仓库；v1.0 后可分仓 |
| **第三方 adapter / cell 贡献** | 当前 0；未来可能有 | 多则倾向 plugin 模式（方案 E） |

---

## §4 决策方案分叉

> **当前状态**：§4.1-4.5（方案 C 商业化路径）**已弃用**，仅保留作历史参考。§4.6（方案 D 全开源路径）**已采纳**为最终方案。详见 §0 决策结论和 `202604300950-plan-d-go-workspace-multimodule-migration.md`。

### ~~4.1 方案 C 推荐理由~~（已弃用）

> 以下 §4.1-4.5 假设商业化场景，2026-04-30 决策全开源 MIT 后已不适用。保留供商业模式发生变更时回退参考。

综合「商业模式 + 团队规模 + archtest 治理 + release 节奏」四因素，~~**方案 C（核心开源 + 商业产品独立）渐进式拆分**最适合 GoCell~~（**已弃用**：商业前提不存在）：

- **不预先拆**：v1.0 前保持单 monorepo（方案 A），重点是 framework 自身演进（E1-E10 + E14）
- **MDM 启动时拆**（v1.0 后 / Phase 1 启动）：建 `gocell-mdm` 独立仓库，框架代码不动
- **零信任启动时再拆**（Phase 5 启动）：建 `gocell-zt` 独立仓库，复用 `gocell-mdm` 的 device 系列 cells（deviceregistry / deviceconfig / devicestate / devicecommand）

### 4.2 阶段化路线

```
阶段 0：当前 → v1.0 完成（2026 Q2-Q4）
─────────────────────────────────────
github.com/ghbvf/gocell/                     单 monorepo，framework + 示例 cells

阶段 1：MDM Phase 1 启动（2027 Q1）
─────────────────────────────────────
github.com/ghbvf/gocell/                     framework 稳定开源
github.com/ghbvf/gocell-mdm/                 新建 MDM 仓库（商业，可闭源 / 私有）
                                             require github.com/ghbvf/gocell v1.0+

阶段 2：MDM v1 GA + 零信任启动（2028 Q1）
─────────────────────────────────────
github.com/ghbvf/gocell/                     framework v1.x（按需 minor bump）
github.com/ghbvf/gocell-mdm/                 MDM v1.0 GA
github.com/ghbvf/gocell-zt/                  新建零信任仓库（商业）
                                             require github.com/ghbvf/gocell v1.x
                                             require github.com/ghbvf/gocell-mdm v1.0  （复用 device cells）

阶段 3：开源生态扩展（2028 Q3+，可选）
─────────────────────────────────────
github.com/ghbvf/gocell-extensions/          社区第三方 adapter / cells（开源）
                                             由社区维护，framework team 仅 review
```

### 4.3 各仓库职责划分

| 仓库 | 内容 | 授权 | 维护团队 |
|---|---|---|---|
| `gocell` | kernel + runtime + adapters/{postgres,redis,rabbitmq,vault,oidc,s3,websocket} + pkg + cmd/gocell + 示例 cells（accesscore/auditcore/configcore） + 示例 assemblies（corebundle） | Apache 2.0 / MIT 开源 | framework team |
| `gocell-mdm` | cells（deviceregistry/deviceconfig/devicestate/devicecommand/pkicell/policyengine/tenantisolation/rbaccell/mdmgateway） + adapters/{mdmprotocol,timeseries,pki,cdn} + assemblies（mdmmonolith/mdmmicroservice/mdmedge） + cmd/mdm | 商业授权 / 双授权（社区版 + 企业版）| MDM 产品 team |
| `gocell-zt` | cells（iapgateway/sessionpolicy/trustscore/jitgrant/mtlscert/eventcorrelation/anomalydetection/compliancereporting） + adapters/{servicemesh,idp,ml,siem} + assemblies（zerotrustmonolith/zerotrustplatform） + cmd/zt | 商业授权 | 零信任产品 team（部分复用 MDM team） |
| `gocell-extensions`（可选）| 第三方 adapter / cells（如 adapters/kafka / adapters/pulsar / 第三方业务 cell）| Apache 2.0 开源 | 社区贡献，framework team review |

### 4.4 Cell 复用关系

```
gocell-zt 依赖 gocell-mdm 的 cells：
- deviceregistry, devicestate（设备身份）
- pkicell（证书共用）
- policyengine（策略评估共用）
- tenantisolation, rbaccell（多租户共用）
- auditcell（合规审计，从 gocell.auditcore 扩展）

gocell-mdm 依赖 gocell：
- accesscore（用户身份基础）
- auditcore（基础审计）
- configcore（配置管理）
- 全部 framework 能力

gocell（无依赖）：
- 完全独立，可单独被任何项目引用
```

### 4.5 Module Path 设计

| 仓库 | Module path |
|---|---|
| `github.com/ghbvf/gocell` | `github.com/ghbvf/gocell` |
| `github.com/ghbvf/gocell-mdm` | `github.com/ghbvf/gocell-mdm` |
| `github.com/ghbvf/gocell-zt` | `github.com/ghbvf/gocell-zt` |

**注意**：用 `gocell-mdm` 而非 `gocell/mdm` 子路径，是因为：
- `gocell/mdm` 暗示是 gocell repo 的子目录（go module 路径与文件系统路径绑定）
- 独立 repo 用独立 module path 才不歧义
- 类似 `terraform-provider-aws` 而非 `terraform/provider-aws`

### 4.6 全开源场景下的修订推荐（重要补充）

§4.1-4.5 的方案 C 推荐基于「MDM/零信任商业化」前提。**如果 MDM/零信任完全开源**，原本的核心理由「商业产品代码不能放开源仓库」消失，决策矩阵需重算。

#### 4.6.1 全开源场景下的因素重评

| 因素 | 全开源场景下评分 | 倾向 |
|---|---|---|
| 商业 vs 开源边界 | **不存在**（都开源） | 中性 |
| archtest 跨界治理 | 单仓占优（不需要 federated 工具） | 单仓 |
| 仓库 manageability | 多仓占优（仓库不会巨型化） | 多仓 |
| Release 独立性 | 多仓 / 多 module 占优 | 多仓 / 多 module |
| Refactor 频率（v1.0 前/后） | v1.0 前单仓占优；v1.0 后中性 | 渐进 |
| 团队权限 | 同 org 都能管 | 中性 |
| CI 时间 | 多仓占优（改一处不全跑） | 多仓 |
| 新人理解 | 单仓占优（一个 git clone 看全） | 单仓 |
| 第三方贡献 | 中性 | 中性 |

**新结论**：「商业边界」这条最重的论据消失后，剩下的因素中「单仓便利性」和「多仓 manageability」势均力敌。**关键变量是代码量级**：
- 三者总代码量 < 30 万行 → 单仓库可承受
- 30-100 万行 → 多 module 单仓库 / 渐进拆分
- > 100 万行 → 必须拆分（K8s 是个例外，但 K8s 是个例外）

GoCell + MDM + 零信任完整形态预估：50-80 万行，落在「多 module 单仓库」最佳区间。

#### 4.6.2 全开源场景下的推荐：方案 D（go workspace 多 module）

```
github.com/ghbvf/gocell/                           单仓库（全开源）
├── go.work                                        workspace 定义
├── core/                                          go.mod = github.com/ghbvf/gocell/core
│   ├── kernel/ runtime/ adapters/ pkg/
│   ├── cells/{accesscore,auditcore,configcore}    示例 cells
│   ├── contracts/                                 framework 契约
│   └── cmd/{gocell,corebundle}
├── mdm/                                           go.mod = github.com/ghbvf/gocell/mdm
│   ├── cells/{deviceregistry,deviceconfig,...}
│   ├── adapters/{mdmprotocol,pki,timeseries,cdn}
│   ├── contracts/                                 MDM 契约
│   ├── assemblies/{mdmmonolith,mdmmicroservice}
│   └── cmd/mdm
├── zerotrust/                                     go.mod = github.com/ghbvf/gocell/zerotrust
│   ├── cells/{iapgateway,sessionpolicy,...}
│   ├── adapters/{servicemesh,idp,ml,siem}
│   ├── contracts/                                 零信任契约
│   ├── assemblies/{zerotrustmonolith}
│   └── cmd/zt
└── tools/                                         go.mod = github.com/ghbvf/gocell/tools
    └── 共享 CLI plugin / archtest 扩展 / contract registry
```

**优点（vs 方案 A 单 module monorepo）**：
- 各 module **独立 SemVer**：core 可以 v1.5.0，mdm v1.0.0，zerotrust v0.5.0 各自演进
- 改 mdm 不触发 core 全仓 CI（按 module 范围跑）
- 多个 cmd binary 可独立 build / release
- 用户可只 `import github.com/ghbvf/gocell/core/...` 不引入 mdm/zerotrust

**优点（vs 方案 B 完全独立 3 仓）**：
- 不需要 contract registry 工具（contract.yaml 跨 module 直接 import 解析，单仓库 fingerprint 全局）
- 不需要 federated archtest（archtest 在 root 跑，统一守卫所有 module 边界）
- 跨 module refactor 一个 PR 完成（go.work 让 IDE 实时看到所有 module 变更）
- 单 git clone 拿全代码，新人友好
- 所有 module 共享 CI / golangci.yml / archtest 配置

**缺点**：
- go.work 多 module 在某些 IDE / linter 边角支持仍有瑕疵（GoLand / VSCode + gopls 自 1.21 起较好；2026 Q4 v1.0 时 go.work 已发布 5+ 年应该完全成熟）
- 多 module 让新人需要懂 go.work + replace directive
- 跨 module dependency 显示更复杂（`go list -m all` 有更多输出）

#### 4.6.3 业界对照：全开源大型 framework 的真实选择

| 项目 | 仓库形态 | 关键决策 |
|---|---|---|
| **Kubernetes** | 巨型单 module monorepo（百万行）+ 工具拆分（kubebuilder/controller-tools 独立） | 核心保持单仓，工具因独立可用而拆 |
| **Apache Camel** | 单仓库多 module（Maven multi-module，几千组件） | 全在一个仓库便于 refactor |
| **Linkerd2** | 单仓库 monorepo（control plane + data plane + dashboard 多组件） | 全开源 + 同源演进 |
| **Cilium** | 单仓库（cilium/cilium）+ 部分子项目独立 | 核心单仓 |
| **Crossplane** | 核心单仓 + Provider 独立仓库（每个 provider 一个 repo） | Provider 是真正第三方扩展点 |
| **Spring Cloud** | spring-cloud-* 多个独立仓库（同 org） | Java 历史包袱 |
| **CloudWeGo** | Kitex / Hertz / Netpoll 多独立仓库（同 org，全开源） | 同等地位独立产品线 |

**关键观察**：
- **大型全开源 framework 偏向单仓库 + 多 module / 单 module monorepo**（K8s, Camel, Linkerd, Cilium）
- 多仓库一般出现在：
  - (a) Plugin/Provider 第三方扩展生态成熟（Crossplane, Terraform）
  - (b) 同等地位多产品线（CloudWeGo）
  - (c) 历史包袱（Spring Cloud）

GoCell + MDM + 零信任三者关系是 **framework + 应用 + 应用上的应用** 的层级关系，**不是同等地位多产品线**，更不是 Plugin 生态。这种层级关系最适合**单仓库多 module（方案 D）**，类比 Linkerd2 / Kubernetes 模式。

#### 4.6.4 全开源场景下的最终推荐路径

```
2026 Q2-Q4：v1.0 阶段                          单 module monorepo（保持当前）
                                               github.com/ghbvf/gocell/  go.mod
                                               原因：v1.0 前 framework 频繁变更，单 module 最简

2027 Q1：MDM 启动                              转为 go workspace 多 module
                                               github.com/ghbvf/gocell/
                                                 go.work
                                                 ├── core/       go.mod
                                                 └── mdm/        go.mod
                                               原因：MDM 启动 = 第二个 module 出现，go.work 价值显现

2028 Q1：零信任启动                             加第三个 module
                                                 ├── core/
                                                 ├── mdm/
                                                 └── zerotrust/  go.mod

2028 Q3+：社区扩展（按需）                      考虑独立 repo gocell-extensions
                                               原因：第三方贡献的 adapter/cells 不属于核心团队管辖
```

**关键转换点**：v1.0 → MDM 启动时**从单 module 转 go.work 多 module**。这次转换是**单仓库内部重构**，不需要拆分 git history，不需要建 contract registry / federated archtest 工具（仍然单仓库）。

#### 4.6.5 全开源场景 vs 商业场景的对比

| 维度 | 商业化（§4.1-4.5 推荐方案 C） | 全开源（§4.6 推荐方案 D） |
|---|---|---|
| 仓库数量 | 3-4 个独立 repo | 1 个 repo（多 module） |
| 跨 cell refactor | 跨 repo PR 协同 | 单仓库一个 PR |
| Contract 治理 | 需要 contract registry 工具 | 单仓库内 fingerprint 直接守卫 |
| Archtest 跨界 | 需要 federated archtest | 单 archtest 跨 module 守卫 |
| Release 节奏 | 各仓库各自 SemVer | 各 go.mod 各自 SemVer |
| 商业模式 | BSL / AGPL+商业 / 闭源可选 | 必须开源（Apache 2.0 / MIT） |
| 团队协作 | 各仓库独立 maintainer | 单仓库 CODEOWNERS 划分 |
| 工具投入 | 需要 contract registry + federated archtest（2-3 月）| 几乎无（go.work 是 stdlib 能力） |
| 新人上手 | 多 git clone | 单 git clone |
| 业界对照 | HashiCorp Terraform Provider 模式 | Apache Camel / Linkerd / K8s 单仓多 module |

**简化判断**：
- **商业化** → 方案 C（必须分仓）
- **全开源** → 方案 D（单仓库多 module，2027 Q1 MDM 启动时转换）

#### 4.6.6 不推荐方案 D 的边角情况

如果选择全开源 + 方案 D，但满足以下任一条件，仍然应考虑方案 B（完全独立多仓）：
- **MDM 团队和 framework 团队完全独立运作**（不同 oncall / 不同 release sprint / 不同 PR review 标准）
- **GoCell core 已经稳定为 LTS**（2-3 年不做 breaking change），MDM/零信任演进速度远超 core
- **第三方贡献占主导**（外部贡献者贡献 MDM cells 多于 framework team）
- **代码量超过 100 万行**（单仓库压力过大）

这些都是 **v1.0 之后才可能出现** 的情况，不影响阶段 0-1 决策。

---

## §5 多仓库的工具支持需求

> **适用前提**：本节工具仅在选择方案 B / 方案 C（多仓库）时需要。
> 全开源 + 方案 D（单仓库多 module）路径**不需要**这些工具——单仓库内 contract 引用和 archtest 都通过本地路径完成。

跨仓库治理需要 framework 提供 3 类工具，作为 v1.0+1 / v1.1 路线的 framework 演进项：

### 5.1 Contract Registry（远程 contract 同步）

**需求**：`gocell-mdm` 仓库内 contract.yaml 引用 `gocell` 仓库的 contract（如 mdm deviceregistry 引用 framework accesscore 的 user-auth contract）

**实现方案**：
- contract.yaml 加 `imports` 字段：
  ```yaml
  imports:
    - module: github.com/ghbvf/gocell@v1.0.0
      path: contracts/http/accesscore/user-auth/v1
  ```
- `gocell validate --strict` 通过 `go mod download` 解析远程 contract 文件并校验
- 缓存到 `~/.gocell/cache/<module>@<version>/contracts/...`
- 类比：buf workspace 的 buf module 引用机制

**实施时机**：阶段 1（MDM 启动）前必须就绪

### 5.2 Federated Archtest（跨仓库 LAYER 守卫）

**需求**：`gocell-mdm` 不能 import `gocell.kernel/cell.internal/...`（违反 framework internal 边界）；`gocell-zt` 不能 import `gocell-mdm.cells/deviceregistry.internal/...`（违反产品 internal 边界）

**实现方案**：
- framework 提供 `archtest.LayerRule` 库，每仓库 vendor 该库后写自己的 archtest_test.go
- LayerRule 定义：「本仓库 X 层不能 import 远程仓库 Y 的 internal/」
- archtest 通过 `go list -m all` + AST 解析远程 module 的 internal 路径
- CI 中各仓库独立跑 archtest，但规则统一从 framework 库取

**实施时机**：阶段 1 必须就绪

### 5.3 SemVer 兼容性矩阵

**需求**：`gocell v1.2.0` 与 `gocell-mdm v1.0.0 / v1.1.0 / v2.0.0` 哪些组合受支持

**实现方案**：
- `gocell-mdm` 在 README 维护兼容性表：
  ```
  | gocell-mdm 版本 | 兼容 gocell 版本 |
  | v1.0.x | v1.0+ |
  | v1.1.x | v1.0+ ~ v1.5 |
  | v2.0.x | v1.5+ |
  ```
- CI 跑兼容性矩阵测试（pinned framework version × 当前产品版本）
- 类比：Terraform Provider 与 Terraform 核心的兼容性声明

**实施时机**：阶段 1 之后逐步建设

---

## §6 风险评估

| 风险 | 严重度 | 缓解 |
|---|---|---|
| **跨 repo refactor 摩擦** | 中 | v1.0 前保持单仓库充分稳定 API；v1.0 后再拆 |
| **Contract registry 工具滞后** | 高 | 必须在阶段 1 启动前完成最小可用 contract registry，否则 MDM 仓库的 contract 无法校验 |
| **Federated archtest 工具滞后** | 中-高 | 同上，archtest 是 GoCell 强结构化承诺，跨仓库丢失意味着失去核心价值 |
| **商业版 vs 社区版边界争议** | 中 | 类比 HashiCorp / Elastic / MongoDB 都遇到过，建议早期定义清楚（参考 Apache 2.0 + 企业增强模块的双授权） |
| **第三方 adapter 生态早期空白** | 低 | 不急于建设；社区 adapter 等 v1.0+1 才考虑 |
| **多仓库 release 协同复杂度** | 中 | SemVer 严格遵守 + 兼容性矩阵 + framework 不轻易做 breaking change |

---

## §7 替代方案的明确不推荐理由

| 方案 | 不推荐理由 |
|---|---|
| 方案 A 单 monorepo | **商业产品代码不能放开源仓库**；release 节奏锁死；CI 时间长 |
| 方案 B 完全独立 | 太激进；framework 还没 v1.0 就拆会伤 archtest 跨界治理 |
| 方案 D go workspace 多 module | go.work 在多 module 下 IDE / linter 支持仍有边角问题；商业模式仍受限（同仓库下闭源代码难处理） |
| 方案 E plugin 模式 | 仓库数量爆炸；小团队（<10 人）维护成本过高；适合 Terraform Provider 这种社区生态成熟的项目，GoCell 早期不适合 |

**方案 C 渐进式** 是「保持当前单仓库充分稳定 + 商业启动时按需拆 + 工具配套支持」的折中，是这四个因素的最优解。

---

## §8 决策点（已确认）

- [x] **商业模式**：**全开源**（所有产品 / 项目 MIT 协议统一） → **方案 D**
- [x] **方案 D 路径**：v1.0 前单 module monorepo（当前形态保持）/ 2027 Q1 winmdm 启动时一次性切换 go.work / 2029 Q1 零信任启动时加 zerotrust module
- [x] **授权选择**：**MIT**（vs Apache 2.0）—— MIT 兼容性更高，对内部 fork 和企业集成最友好
- [x] **不建 contract registry / federated archtest 工具**：单仓库内闭环，省下 2-3 个月 framework 团队投入
- [ ] **是否预先建 `gocell-extensions` 仓库**作为社区扩展点？
  - 建议：v1.0 后再建（避免空仓库）；MIT 协议下社区直接 fork 也可作为替代

---

## §9 与现有目录结构的对接

阶段 0（当前）保持现状，本仓库 `github.com/ghbvf/gocell` 包含：
- `kernel/ runtime/ adapters/ pkg/`：framework
- `cells/{accesscore,auditcore,configcore}`：示例 + reference cells
- `examples/{ssobff,todoorder,iotdevice}`：示例项目
- `docs/plans/{ci-governance,engineering-baseline,product-roadmap}/`：本目录系列规划文档

阶段 1（MDM 启动）之后，**本仓库内 NOT 包含**：
- 不会出现 `cells/deviceregistry/` 或 `cells/iapgateway/`（去 `gocell-mdm` / `gocell-zt`）
- 不会出现 `adapters/mdmprotocol/` 或 `adapters/servicemesh/`（去对应商业仓库）
- 不会出现 `assemblies/mdmmonolith` 或 `assemblies/zerotrustplatform`（去对应商业仓库）

本仓库继续维护：
- framework + adapters（postgres/redis/rabbitmq/vault/oidc/s3/websocket 等通用）+ pkg
- 1-3 个示例 cells（accesscore + auditcore + configcore，作为 framework 自测和文档示例）
- 1 个示例 assembly（corebundle）

---

## §10 总结（2026-04-30 决策锁定）

### 最终方案：方案 D 单仓库 go workspace 多 module + MIT 全开源

- 仓库：`github.com/ghbvf/gocell/`（单仓库）
- module：`core`（顶层）/ `mdm`（含 winmdm）/ `zerotrust`（2029 Q1+）
- 授权：**MIT**（统一所有产品 / 项目）
- 切换时机：v1.0 前保持当前单 module monorepo / 2027 Q1 winmdm 启动时一次性切换 / 2029 Q1 加 zerotrust module
- 业界对照：Apache Camel / Linkerd2 / Kubernetes 单仓库多 module

### 核心约束

- **架构哲学**：「Cell-native + 多形态部署 + 强结构化治理」在每个 module 内部都成立
- **v1.0 前保持单仓单 module**：framework 自身演进期不变结构（详见 `202604300950-...` §10 阶段 0）
- **不建 contract registry / federated archtest**：单仓库内 archtest 跨 module 直接守卫（省 2-3 个月）
- **跨 module cell 复用**：
  - core 提供 accesscore + auditcore + configcore
  - mdm（winmdm）复用 core；新建 9 个 cell（rbaccell + pkicell + deviceidentity + mdmcell + agentcell + groupengine + policycell + appcatalog + devicelifecycle）
  - zerotrust 复用 core + mdm（deviceidentity + mdmcell + agentcell + pkicell + rbaccell + policycell + devicelifecycle）

### 已弃用方案

- ~~方案 A 单 monorepo 单 module~~：v1.0 后多 module 演进
- ~~方案 B 完全独立 3 仓~~：太激进，伤 archtest 跨界治理
- ~~方案 C 商业化拆分~~：**商业前提不成立（已确认全开源 MIT）**
- ~~方案 E plugin 生态~~：仓库爆炸，小团队不适合

### 决策驱动因素（事后总结）

1. **商业模式 = 全开源 MIT**（最重要，锁定方案 D）
2. **代码量级**：50-80 万行（GoCell + winmdm + 零信任完整形态），落在多 module 单仓最佳区间
3. **团队结构**：framework + winmdm + 零信任 同 org，CODEOWNERS 划分而非分仓
4. **第三方贡献**：MIT 协议下社区直接 fork 即可，不需要预先建 extensions

### 后续动作

参考 `202604300950-plan-d-go-workspace-multimodule-migration.md`：
- 阶段 0（2026 Q2-Q4）：当前形态不动，E1-E10 + P0 5 项落地
- 阶段 1（2027 Q1）：PR A1.1 一次性切换 go.work + mdm/ module 骨架（8-11 天）
- 阶段 1 后续（2027 Q1-2028 Q1）：PR A1.2-A1.17 winmdm 11 cell + 6+1 assembly 渐进开发
- 阶段 2（2029 Q1）：加 zerotrust module
