# PR #392 Setup Admin 设计 Postmortem — bootstrap 模式应删

- **Date**: 2026-05-06
- **Branch**: `refactor/527-sec-setup-closure`
- **Triggered by**: 第 4 轮 review 在 4 个不同表面同时显形 P1 阻塞 (operator/user plane 混用 / contract 不同步 / FMT-28 contains / multi-pod fail-open)
- **Range**: `develop@6100e85` → `refactor/527-sec-setup-closure@1a7241b6` (~101 files, post 3 轮 fix)
- **Author intent (per user)**: 「设计一个 setup 引导式的 admin 账号配置方案」

本文不是又一份 finding 列表，而是回头审视**设计本身**：原始意图是什么，PR 为何越改越乱，根因在哪一层，正确的回归点是什么。

---

## 1. 原始意图（用户原话）

> 「本意是想设计一个 setup 引导式的 admin 账号配置方案，结果乱改越改越乱了」

「引导式」 = 人在场，运维主动调用 endpoint，显式提供 admin 身份信息。这正是 **interactive 模式**：

```
operator (持有 Basic Auth env) → POST /api/v1/access/setup/admin
    Authorization: Basic base64(env_creds)
    body: {username, email, password}    ← 这是 admin 的业务身份
→ 201 Created (admin 入库)
→ 后续 POST → 410 Gone
```

interactive 模式的概念**完全闭合**：
- env creds = operator 是谁（authenticator）
- body = admin 是谁（subject）
- 两个 plane 由 ADR §D5 显式分离
- 没有上面 4 个 P1 的任何一个

**用户的本意从一开始就是对的，且现在的代码里 interactive 模式是干净的。**

---

## 2. 那为什么会乱？bootstrap 模式怎么混进来

PR #392 在 ADR `202605061600-adr-bootstrap-admin-boundary.md` 同时做了三件事：

| 决策 | 内容 | 是否必要 |
|------|------|---------|
| §D1 | setup/admin 从 anonymous public 变成 auth.bootstrap 闭合契约 | ✅ 安全升级，必要 |
| §D3 | 删除 39 个 credfile / sweep / cleaner / generator 文件（~1500 LOC） | 顺手清理，独立合理 |
| §D4 + §D9 | 保留 bootstrap 模式（原 credfile 时代「无人值守自动创建 admin」），改用 env-driven，并写 §D9「持久 startup credential 模型」给它续命 | ❌ **错** |

### 2.1 关键拐点

PR #392 之前 bootstrap 模式的实现机制：
```
启动 → 检测 admin 不存在 → 随机生成密码 → 写 <stateDir>/initial_admin_password 文件 → 运维读文件登录首管 → 后台 worker 定期清理
```

bootstrap 模式存在的**全部理由**是上面这个「随机密码 + 安全分发 + 自动清理」便利性。

**§D3 删了这个机制**之后，bootstrap 模式失去了存在理由。这是一个清晰的拐点，应当问一句：

> "随机密码 + credfile + cleanup worker" 已删，bootstrap 模式还应该存在吗？

**没人问出这句话**。我反而：

1. **强行给 bootstrap 模式找新的存在理由**：让 lifecycle 直接用 env operator secret 当 admin 密码。这是把「operator plane」和「user plane」物理混合 —— 直接踩穿 §D5。
2. **写 §D9「持久 startup credential 模型」做事后正名**：声称这是和 MinIO root creds / Vault unseal keys 同类的成熟模式。
3. **错位类比**：
   - MinIO root creds 是 **root principal**，不会出现在 user table；GoCell 把 env 密码哈希存进 `users.password_hash` —— 性质不同
   - Vault unseal keys 是机器层 unseal，**不创建用户** —— 性质不同
   - 真正的上游对照是 **Keycloak `KC_BOOTSTRAP_ADMIN_USERNAME` 的 temporary admin** + **kubeadm bootstrap-token 的 TTL** —— 都是 ephemeral / temporary，不是 permanent
   - **错位类比比没有类比更危险**：它给一个本无上游对应物的设计涂上了一层虚假的合理性

### 2.2 §D3 + §D5 + §D9 的内部矛盾

把这三条 ADR 决策放在 bootstrap 模式下逐项展开：

- §D5：env = operator identity；body = admin identity（**两个 plane 分离**）
- §D3：no credfile，no random password generator（**没有第三方密码源**）
- §D9：env is persistent startup credential（**env 是稳态部署 secret，不可清除**）

bootstrap 模式：lifecycle 自动创建 admin，**没有 body 输入**。
→ admin password 只能来自 env（§D3 排除了别的源）
→ 但 env 是 §D5 中的 operator identity 且 §D9 中的稳态 secret
→ **持久部署 secret 直接物化为业务 admin 的真实密码**
→ §D5 的两 plane 分离原则，被 §D3+§D9 的组合给踩穿了

写 ADR 时三条决策**逐条单独**审视都通过；**组合**起来在 bootstrap 模式下产生悖论。我没识别。

---

## 3. 4 个 P1 是同根

第 4 轮 review 给出 4 个 P1 阻塞，看起来 4 件不同的事：

| # | 表述 | 实际本质（同一根因的不同投影） |
|---|------|--------------------------------|
| P1#1 | operator secret 与 seeded admin 凭据混用 | **plane 没分离**：operator plane 直接物化成 user plane（§D5/§D3/§D9 矛盾的直接后果） |
| P1#2 | contract 没声明 401/429，治理只看 handler AST | **SoT 没分离**：保护推到 middleware 时没让 contract 升级表达完整 HTTP 交互 |
| P1#3 | FMT-28 用 `strings.Contains("setup/admin")`，runtime/schema/archtest 4 处近似规则 | **谓词没收敛**：bootstrap 边界没有单一精确函数 |
| P1#4 | multi-pod 检查 fail-open（缺 `GOCELL_REPLICA_COUNT` env 直接 false） | **拓扑是 hint 不是约束**：把安全决策建立在「环境变量可能存在」的可选信号上 |

P1#1 + P1#4 直接绑定 bootstrap 模式：
- P1#1：bootstrap 模式才把 env 当 admin 密码（interactive 不会）
- P1#4：bootstrap 模式 lifecycle 幂等所以多 pod 安全；**只有 interactive 模式需要拓扑约束**，但 interactive 模式当前依赖一个 fail-open 的 hint

P1#2 + P1#3 是 PR §D1「保护推到 middleware」的副作用，与 bootstrap/interactive 选择无关，是治理层独立修复。

**结论**：删除 bootstrap 模式 → P1#1 直接消失（plane 不再混用，因为 bootstrap 分支不存在）+ P1#4 简化（interactive 改 fail-closed 即可，因为 interactive 是唯一模式）。剩下 P1#2/P1#3 是纯治理修复，工作量小。

---

## 4. 为什么前 3 轮 fix 解决不了（4 层原因）

### 4.1 修复以 review 单子驱动，不以「在设计什么」驱动

时间线：

```
PR 主体             —— 引入 auth.bootstrap 闭合契约 + §D9 「持久 startup credential」
第 1 轮 review fix  —— 删 mux wrapper / 删 opt-in middleware / 删 type alias    （wiring 层）
第 2 轮 review fix  —— 补示例 wiring / 补测试 / 修 lint                          （消费方层）
第 3 轮 review fix  —— 修 e2e 脚本 Basic Auth / setup_integration_test 缺 wire  （CI 层）
第 4 轮 review      —— P1#1 凭据混用 / P1#2 contract / P1#3 谓词 / P1#4 拓扑     （设计层终于显形）
```

每轮 fix 在错误的地基上加补丁。地基的概念矛盾从来没被审视。**没有人**——包括前几轮 review 角色，包括我自己——回头问：「bootstrap 模式还应该存在吗？」大家默认它是对等的 first-class 模式，只在它的实现细节里挑毛病。

### 4.2 三原则自审用错了对象

memory 里有 feedback「计划先做激进三原则自审」（feedback_radical_self_audit.md），我每轮都做了「彻底/不向后兼容/优雅简洁」自审，但**审的是当前 PR 代码层的补丁**：

- 删 type alias 彻底吗？✅
- 删 BootstrapAllowAllLimiter 不向后兼容吗？✅
- mux wrapper 优雅吗？❌ → 删

但**没人审视过「§D3+§D5+§D9 三条 ADR 决策放在一起对吗」**。

> 优雅简洁的代码可以承载错误的概念模型。激进自审必须包含「我自己写的 ADR 内部一致吗」这一层，不能只审视当前 PR 的代码补丁。

### 4.3 单一 PR 内做了三件事，被打包后矛盾不可见

PR #392 同时做了：

1. anonymous public → Basic Auth bootstrap（必要，必须做）
2. 删 39 个 credfile 文件（顺手清理）
3. 引入「持久 startup credential 模型」给 bootstrap 模式续命（错的）

如果是三个独立 PR，第 2 个 PR review 会被问「删了 credfile 后 bootstrap 模式还有存在意义吗？」—— 但打包成一个 PR 后，三件事互相遮蔽。

### 4.4 错位类比给错误设计涂上虚假合理性

我在 ADR §D9 写「对照 MinIO root creds + Vault unseal keys」。这两个对照在表面相似：
- 都是「每次启动都要用」
- 都是「持久部署 secret」

但语义完全不同：
- MinIO root creds → MinIO 内部 root principal（不是 user table 行）
- Vault unseal keys → 机器层 unseal（不创建任何用户）
- GoCell bootstrap env → user table 里 admin 行的 password_hash 来源 ❌

正确的对照应该是 Keycloak `KC_BOOTSTRAP_ADMIN_USERNAME` 的 temporary admin 模式（master realm 创建后失效）和 kubeadm bootstrap-token 的 TTL 模式（入群后短期失效）—— 都是 ephemeral，不是 permanent。

错位类比让我自己 + ADR review 角色 + 后续 fix 的所有人都觉得这个设计「有上游撑腰」，再没人质疑它是否合理。

---

## 5. 正确的回归点：删除 bootstrap 模式

回到用户的本意：「setup 引导式的 admin 账号配置方案」 = interactive 模式。

### 5.1 一次性收益

| 项 | 现状 | 删 bootstrap 模式后 |
|----|------|---------------------|
| P1#1 plane 混用 | 凭据双重职责 | **直接消失**（bootstrap 分支不存在） |
| P1#4 multi-pod fail-open | env 缺失放行 | **简化**：interactive 唯一模式，缺拓扑显式 fail-fast |
| ADR 决策数 | D1–D10 + 内部矛盾 | D1–D5 概念干净 |
| 测试矩阵 | interactive 完整 + bootstrap 残缺 | interactive 唯一路径，矩阵完整 |
| 文档/示例 | 两套并行表述 | 单一表述 |
| 概念模型 | 两个模式 + plane 边界在 bootstrap 模式下踩穿 | 单一模式 + plane 边界天然成立 |

### 5.2 唯一损失

`go run ./cmd/corebundle` 启动后**不会自动创建 admin**，运维需多发一次：

```bash
curl -u "${BOOTSTRAP_USER}:${BOOTSTRAP_PASS}" -X POST http://localhost:8080/api/v1/access/setup/admin \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","email":"admin@local","password":"InitialPass1!"}'
```

这正是用户「**引导式**」一词的本意。失去的便利原本依赖 §D3 删掉的 credfile 路径，§D3 之后这个便利已经只剩名字。

### 5.3 删除清单（精确）

代码：
- `cmd/corebundle/access_module.go`：
  - 删 `adminProvisionMode` 类型 + `adminProvisionModeBootstrap`/`adminProvisionModeInteractive` 常量
  - 删 `resolveAdminProvisionMode` 函数；`SetupModeEnv` 完全移除（运维不再需要选模式）
  - 删 `isMultiPod` + `GOCELL_REPLICA_COUNT`（interactive 唯一模式时改成「显式声明 single-pod 才允许启动」更彻底）
  - `Provide()` 简化：只构造 BootstrapMiddleware + WithBootstrapAuth，不再有 mode 分支
- `cells/accesscore/initialadmin/`：**整个包删除**（lifecycle hook、envdriven_bootstrap、所有相关测试 + unsupported stub）。约 30 个 .go 文件。
- `cells/accesscore/cell.go`：删 `WithInitialAdminBootstrap` option + `initialAdmin` 字段
- `cells/accesscore/cell_init.go`：删 `bindInitialAdmin` + `c.initialAdmin != nil` lifecycle 注册
- `cells/accesscore/cell_initialadmin_test.go`：整个文件删除
- `cmd/corebundle/outbox_e2e_integration_test.go`：删 `WithInitialAdminBootstrap` 调用，改用 setup/admin POST + Basic Auth 创建 admin（与 ssobff walkthrough 同模式）
- `cmd/corebundle/main_test.go`：删 `SetupModeEnv` 相关 setenv

文档：
- ADR `202605061600-adr-bootstrap-admin-boundary.md`：重写。保留 §D1（auth.bootstrap 闭合契约）+ §D5（plane 分离）+ §D6（multi-pod 拒绝，改 fail-closed）；删 §D2/§D3/§D4/§D7/§D9/§D10 关于「bootstrap 模式」的所有表述。决策从 D1–D10 压缩到 D1–D5。
- `docs/operations/first-run-setup.md`：单一 interactive 模式描述
- `docs/guides/admin-bootstrap-paths.md`：删除「两条路径」选型矩阵；改为「single setup-driven path」
- `docs/ops/env-vars.md`：删 `GOCELL_SETUP_MODE` 行；`GOCELL_BOOTSTRAP_ADMIN_*` 描述只保留 operator Basic Auth 用途

治理（与本主题正交，但同 PR 一起修）：
- contract.yaml `auth.responses` 字段（middleware 注入的 4xx：401/429 列表）
- 治理 CH-04 + 新增 CH-XX：扫描 mount-time middleware 的 4xx，不只 handler AST
- `kernel/cell/auth/bootstrap_path.go`（新）：`IsBootstrapPath(path) bool` 单一精确谓词，按 path segment 匹配
- FMT-28 / `tools/archtest/setup_admin_auth_test.go` / runtime FinalizeAuth：4 处全部调用同一函数
- `kernel/metadata/schemas/contract.schema.json`：删 `Route.Bootstrap` 表述（已是 `BootstrapAuth`）

测试：
- `cmd/corebundle/setup_integration_test.go`：保留并扩展（已 GREEN）
- `examples/ssobff/walkthrough_test.go`：保留（已 GREEN，已是单一 interactive 路径）
- 删除：所有 `TestInit_WithInitialAdminBootstrap_*` / `TestInit_BootstrapAlreadyHasAdmin_*` / `TestInit_BootstrapUser_HasPasswordResetRequired` / `cell_initialadmin_test.go` 整文件 / initialadmin 包内所有 `lifecycle_test.go` `envdriven_test.go` 等

预估工作量：1 天（删的多于加的）。

---

## 6. 学习与教训

### 6.1 写回 memory

下面这条加入个人 feedback memory：

> **激进三原则自审必须覆盖三层，不是一层**：
>
> - L1 代码补丁：当前 PR 的代码改动是否彻底/不向后兼容/优雅简洁
> - L2 PR 整体：当前 PR 的多个决策**组合起来**是否一致（独立看每条决策都对，组合可能矛盾）
> - L3 概念模型：本 PR 涉及的 ADR 内部一致吗？跨 ADR 的决策矩阵闭合吗？
>
> 当同一类问题在第 N 轮 review 反复以新形态出现，根因几乎必然在 L3，不在 L1/L2。前 N-1 轮 fix 都是在错误地基上加补丁。

### 6.2 设计层信号

下面任一信号出现，**停止写代码**，回到 ADR：

- 同一类问题在第 3 次以上 review 中以新形态出现
- 修复一处后另一处同根症状立即冒出
- ADR 中需要写 §「特殊情况下…」或「当 X 模式时 Y 不适用…」 —— 这是概念矛盾的低保真信号
- 错位类比：「这个设计类似业界 X」，但 X 的语义和你的设计本质不同

### 6.3 单 PR 范围控制

PR #392 主体 + §D3 删 credfile + §D9 引入新模型，三件事捆绑导致组合矛盾不可见。下次此类大重构应：

- 拆 PR：一件事一个 PR，每个 PR 独立 review
- 如果三件事必须一起做（例如有依赖），ADR 必须显式列「组合后效果」一节，逐条 trace 决策矩阵

### 6.4 上游对照的判定标准

引用上游对照时应额外审视一层：

- ✅「他们做了 X，我们也做 X」 — 表面对照
- ✅「他们做 X 是因为 Y 约束，我们有同样的 Y」 — **真正的对照**
- ❌「他们做 X，看起来类似，所以我们也做」 — 错位类比

ADR §D9 「对照 MinIO root creds」属于第三种 —— MinIO 做 X 是因为 root principal 不在 user table 这个**前提**，GoCell 把 X 复制过来时这个前提不成立。

### 6.5 当模式分支带来「特殊情况」时，先问该分支是否应存在

bootstrap 模式当时是「credfile 时代的便利路径」。§D3 删 credfile 后，这个路径的存在理由消失。但我没问「该模式现在还应该存在吗」，反而花精力给它续命。

> **当一个抽象的存在前提消失时，第一选择是删除它，不是给它找新的存在前提**。给消失了前提的抽象找新前提，几乎一定会引入概念矛盾。

---

## 7. 行动建议

按风险/收益顺序：

### 7.1 立即行动（推荐）

走删除 bootstrap 模式路径：

1. 冻结当前 `refactor/527-sec-setup-closure` 分支
2. 在该分支上一个新 commit 实施 §5.3 的删除清单
3. ADR 重写
4. 跑全量验证（go test / lint / e2e / integration）
5. push，标本 PR 为 draft，等用户 review ADR 重写后再合并

### 7.2 替代路径（不推荐）

继续在现有 ADR 上修补 4 个 P1：
- 把 contract 加 401/429
- FMT-28 改单一精确谓词
- multi-pod 改 fail-closed
- bootstrap 模式 lifecycle 改成生成随机 admin 密码（不复用 env）→ 但这就部分恢复了 §D3 删的 credfile 路径

留下两个长期债：
- §D9 持久 startup credential 表述与「bootstrap 模式现在用临时随机密码」事实不符 —— 文档/代码持续漂移
- bootstrap 模式仍是特殊路径，长期衍生新 review finding

### 7.3 不可接受路径

接受现状合并 PR：上面 4 个 P1 中至少 P1#1 是真实安全风险（泄露 K8s Secret = 直接拿 admin token + 改密 + 长期 session），不应进入 develop。

---

## 8. 单句结论

**用户的本意从一开始就是对的（interactive 引导式 admin 配置）。错的是 bootstrap 模式：它在 §D3 删除 credfile 那一刻就应该一起删掉，因为它的存在理由消失了；之后 3 轮 fix 都在给一个本不该存在的分支续命，每续命一次就引出新的 P1。回归点是删除 bootstrap 模式，回到原始意图。**

---

## 附录 A：决策矩阵（修复前后对照）

| 维度 | 修复前 | 修复后（删 bootstrap） |
|------|--------|----------------------|
| 模式数 | 2 (`bootstrap`, `interactive`) | 1（无模式概念，唯一路径） |
| `GOCELL_SETUP_MODE` env | 必填 + 两值 | 完全删除 |
| `GOCELL_BOOTSTRAP_ADMIN_*` 用途 | operator Basic Auth + admin password | 仅 operator Basic Auth |
| `cells/accesscore/initialadmin/` 包 | ~30 个 .go 文件 | 整个包删除 |
| ADR 决策条目 | D1–D10 | D1–D5 |
| plane 分离原则 | bootstrap 模式踩穿 §D5 | §D5 唯一路径成立 |
| multi-pod 检查 | env 可选 hint，fail-open | 显式 single-pod 声明，fail-closed |
| 端到端测试 | interactive 完整 / bootstrap 残缺 | 唯一路径，完整覆盖 |
| 文档矩阵 | 两套并列 | 单一描述 |

## 附录 B：保留的部分

`refactor/527-sec-setup-closure` 中以下产物**正确且应保留**：

- §D1 auth.bootstrap 闭合契约（runtime/auth.Route.BootstrapAuth 单一字段、codegen 必填首参、cells 内禁自定义 RouteMux 的 archtest）
- 持久 operator Basic Auth 保护 setup/admin（每次启动都需要 env，但语义只剩「认证操作员」，不再物化成 admin 密码）
- token-bucket rate limiter（5/min, burst 10）+ slog onAuthFail observer
- e2e 凭据单一事实源 `tests/e2e/scripts/bootstrap-credentials.env`
- `runtime/auth.BootstrapCredentials` / `NewBootstrapMiddleware` API
- archtest CELLS-NO-ROUTEMUX-WRAPPER-01 / AUTH-ROUTE-BOOTSTRAP-FLAG-REMOVED-01 / SETUP-ADMIN-CODEGEN-BOOTSTRAP-AUTH-WIRED-01

这些是 PR #392 真正的核心价值，与 bootstrap 模式正交。
