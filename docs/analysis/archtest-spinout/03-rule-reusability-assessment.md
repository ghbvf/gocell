# 交付物 #3：规则层可移植性评估——哪些能给其他项目用？

> 数据来源：交付物 #2 的 7 份编目（293 条规则行，按实际表格统计；下列数字以表格行为准，与各 agent 汇总行的口径略有出入是因为子规则 -A/-B/-C 拆行）。

---

## 1. 聚合数据

### 1.1 可移植性三档（全局）

| 档 | 条数 | 占比 | 含义 |
|----|------|------|------|
| **通用** | 46 | ~16% | 概念可直接迁移、实现近乎照搬 |
| **半通用** | 87 | ~30% | 思路可迁移，但绑了项目专属包/抽象，需重写载体 |
| **专属** | 160 | ~55% | 只在 GoCell 架构语义下成立 |

### 1.2 按主题分布（可移植性）

| 编目 | 主题 | 通用 | 半通用 | 专属 |
|------|------|:----:|:-----:|:----:|
| A | 引擎自检 / meta / 分层 | **21** | 10 | 2 |
| B | outbox / rmq / saga | 2 | 4 | **34** |
| C | cell / contract / assembly | 0 | 3 | **41** |
| D | codegen / scaffold / bootstrap | 2 | 13 | **28** |
| E | auth / session / security | 1 | 16 | **28** |
| F1 | 卫生：errcode/clock/test/ci | **17** | 11 | 6 |
| F2 | http/obs/pg/adapter | 3 | **30** | 21 |

**两个信号一眼可见：**
1. **通用规则高度集中在 A（21）和 F1（17）= 38/46**。其余主题几乎全是专属或半通用。
2. **Soft 档全局 ≈ 0**（293 条里仅个位数）。这是 GoCell「AI-robust 严禁 Soft 立项」纪律的直接体现——绝大多数是 Hard 或 Medium。这点对"给自己用"是好事：搬过去的规则要么编译期挡死，要么 archtest 静态挡死，不靠字符串约定。

---

## 2. 关键纠偏：A 的 21 条「通用」是引擎自带，不是「业务规则」

A 组那 21 条标"通用"的（`PASS-FUNNEL-*`、`SCANNER-FRAMEWORK-USAGE-*`、`INVENTORY-ANCHOR-*`、`TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01`、`ARCHTEST-TESTMAIN-01`、`PRODUCTION-LOADER-FUNNEL-01` …）是**引擎用来约束"规则作者别乱用引擎"的 meta-archtest**。

- 它们"通用"是指**不含 gocell 业务语义**，但**只有在你使用 archtest 引擎本身时才有意义**。
- 它们**随引擎自动搬走**（交付物 #1 已把它们划入引擎自检），不需要你单独"采纳"。
- 所以谈"哪些业务规则能给新项目用"时，**应把 A 的 21 条排除**——它们是引擎的一部分，不是可挑选的卫生规则。

**排除 A 后，真正"可挑选移植到任意 Go 项目"的通用规则 = 25 条**（F1 17 + F2 3 + B 2 + D 2 + E 1）。

---

## 3. 第一档：真正可直接移植的规则（~20 条，建议照搬）

排除引擎自检后，这些是**换任意 Go 项目都成立、实现可几乎照搬**的——它们就是你想要的"Go 编码规范检查器"的内容：

### 3.1 测试纪律（最干净，零 gocell 依赖）

| 规则 | 作用 | 移植前提 |
|------|------|---------|
| `TEST-SLEEP-DISCIPLINE-01` | 测试禁裸 `time.Sleep` | 无，照搬 |
| `TEST-TIME-LITERAL-01` | 测试禁裸 wall-clock 时间字面量 | 无，照搬 |
| `TEST-EVENTUALLY-FUNNEL-01` | 轮询等待统一走 testwait 出口 | 建等价 testwait 包 |
| `TEST-POLLING-EXTERNAL-REASON-LITERAL-01` | 强制轮询附理由字面量 | 同上 |
| `TESTUTIL-BOUNDARY-01` | testutil 包不得被生产代码 import | 无，照搬 |
| `INTEGRATION-GUARD-01` | 集成测试容器启动失败 fail-fast 而非 skip | 替容器函数名 |
| `SLOWGATE-ALLOWLIST-01` | 慢测试白名单防孤立 | 替 allowlist 路径 |

### 3.2 错误 / panic / 时钟卫生（思路通用，绑一个你自己的小抽象）

| 规则 | 作用 | 移植前提 |
|------|------|---------|
| `EXPORTED-ERROR-NEW-01` | 禁 exported `errors.New`，走统一 error code | 项目有统一 error 包 |
| `PANIC-REGISTERED-01` | 所有 panic 过统一 `Approved` 标记 | 建等价 panicregister 包 |
| `PROD-CLOCK-INJECTION-01` | 生产禁裸 `time.Now` | 项目有 clock 抽象 |
| `PROD-DURATION-CONST-01` | 时长必须命名常量，禁裸字面量 | 无，照搬 |
| `CONTROL-PLANE-CARVEOUT-ALLOWLIST-LIVE-01` | carve-out 白名单自验证（白名单项必须真实存在） | 照搬模式 |
| `KERNEL-MUSTCTOR-PRODUCTION-DECL-01` | 生产包禁 `Must*` 前缀函数 | 调整 carve-out |

### 3.3 CI / 供应链 / 配置卫生（与 Go 项目无关，纯工程纪律）

| 规则 | 作用 | 移植前提 |
|------|------|---------|
| `CI-PINNING-WORKFLOW-DIGEST-01` | GitHub Actions 按 digest pin | 任何用 GHA 的项目 |
| `DEPENDABOT-COVERAGE-GOLANGCI-01` | dependabot 覆盖校验 | 用 dependabot 即可 |
| `LINT-GATE-SMOKE-01` | lint 配置冒烟自验 | 替 golangci 路径 |
| `CI-INTEGRATION-DISCOVERY-01` / `CI-RACE-LANE-SUBSET-01` | CI 测试发现防 hardcode、lane 子集包含 | 调整发现脚本 |

### 3.4 其他零散通用

`EVENT-PAYLOAD-CAMELCASE-01` / `EVENT-DTO-CAMELCASE-01`（JSON 字段 camelCase）、`ROLE-ADMIN-LITERAL-01`（魔法字符串单源常量）、`YAML-QUOTE-FUNNEL-01`（YAML 转义统一出口）、`DISTLOCK-LOCK-NOT-CONTEXT-01`（类型不得意外实现某接口）、`SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01` / `PATHSAFE-PLANSET-FUNNEL-01`（typed-id / 私有字段 funnel，模式通用）。

> **这 ~20 条就是 archtest 规则库里"可作通用 Go 检查器"的全部内容。** 注意它们几乎全是"卫生 / 纪律"类，没有一条是"架构语义"类——后者天然绑项目。

---

## 4. 第二档：可移植的「机制模式」（87 条半通用 → 收敛为 ~8 个模式）

87 条半通用不值得逐条搬（载体都绑了 gocell 包），但它们体现的**机制模式**可在新项目复用。把它们去重，得到约 8 个可复用模式：

| 模式 | 代表规则 | 在新项目怎么用 |
|------|---------|---------------|
| **clock 注入 funnel** | `CLOCK-INJECTION-*` / `KERNEL-CLOCK-*` | 你引入 clock 抽象后，照此约束注入点 |
| **error-first 构造** | `ERROR-FIRST-API-01` / `ERROR-FIRST-TYPED-NIL-01` | 构造函数 `(*T, error)` + nil-guard |
| **message const-literal（防 PII）** | `MESSAGE-CONST-LITERAL-01` / `DETAILS-SLOG-ATTR-01` | 错误 message 禁 runtime 拼接 |
| **redaction funnel** | `SPAN-*-REDACT-*` / `PANIC-REDACT-01` / `REPO-LOG-KEY-ID-REDACT-01` | 日志/trace 出站统一脱敏出口 |
| **single-source const / string-typed funnel** | `*-SINGLE-SOURCE-*` / `OPS-CONTRACT-STRING-FUNNEL-01` | 魔法字符串类型化 + 单源声明 |
| **sealed holder / 私有字段** | `*-HOLDER-SEAL-*` / `*-FIELDS-FROZEN-*` | 唯一持有者 + 包外不可构造 |
| **callsite allowlist** | `*-CALLER-ALLOWLIST-*` / `*-CALLER-01` | 某敏感方法只能在 N 处被调 |
| **migration / SQL 纪律** | `MIGRATION-*` / `SQLSTATE-SINGLE-SOURCE-01` / `PGQUERY-01` | 迁移可重入、SQL builder 归位 |

**这一档才是引擎的真正价值落点**：引擎提供"语法"（`ResolveMethodCall` / `EvaluateConstString` / `AssertGolden` / sealed-holder 检测），你用这些语法把上述模式**贴着自己项目的包**重新表达成规则。

---

## 5. 第三档：160 条专属，不可移植

B（outbox/rmq/saga 34）、C（cell/contract/assembly 41）、E（auth/session 28）、D（codegen 28）的主体——它们锁的是 GoCell 的 cell/slice/contract/outbox/saga/assembly 模型与 accesscore 的 session/credential/epoch 协议。**换项目这些概念不存在，规则一文不值。** 这 55% 是 archtest 不可作为"通用规则库"分发的根本原因。

---

## 6. 给「自己用」的结论

| 你想要的东西 | 该怎么做 |
|-------------|---------|
| 一套现成的「Go 编码规范」规则 | **只有 ~20 条可搬**（§3），且全是卫生/纪律类。值得抄进新项目，但量不大——很多还能直接用 golangci-lint 现成 linter 覆盖（见交付物 #4）。 |
| 写自己架构不变式的「引擎」 | **搬引擎（交付物 #1），自己写规则**。86% 的规则（半通用+专属）证明：规则必须贴着每个项目的架构长出来，引擎给你写规则的能力，而不是规则本身。 |
| 「重武器级」约束（sealed holder / golden funnel / callsite allowlist） | 引擎能给——这是 golangci/ruleguard/depguard 给不了的（交付物 #4 §5）。但**先确认你的个人项目真有这种需求**，否则是过度工程。 |

**一句话**：可直接移植的规则只有 ~20 条卫生规则（占 7%），且大半能被 golangci-lint 覆盖；archtest 真正值得带走的是**引擎 + 第二档那 8 个机制模式的"写法"**，而不是规则清单。这与交付物 #1 的结论一致：**搬引擎，不搬规则。**
