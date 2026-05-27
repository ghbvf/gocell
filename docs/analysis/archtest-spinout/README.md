# archtest 独立化分析

> 问题：**tools/archtest 能否独立出仓库，成为一个 Go 编码规范检查器，给自己用？**
> 分析日期：2026-05-27 ｜ 范围：`tools/archtest`（629 个 `*_test.go`，~95k 行；183 个文件含 INVARIANT 锚点，293 条去重规则行）

---

## 一句话结论

archtest 是**两层叠加**：一个**项目无关、依赖干净（仅 `x/tools`+`x/sync`）的规则引擎**，加上**一套 86% 绑死 GoCell 架构的规则**。

- ✅ **引擎可以独立**，~1.5 天工作量，是真正值钱、可复用的资产。
- ❌ **规则不能作为"现成规范库"分发**：仅 ~16% 通用，其中可挑选移植的只有 ~20 条卫生规则。
- ⚠️ **没用 go/analysis 是知情取舍**（自建 Pass 换 INV-1 安全闸 + 免 DAG 负担），不是无知造轮子；对 depguard/ruleguard 是分工不是重复。

**行动建议：搬引擎，自己写规则。** 若个人项目用不到 sealed-holder / golden-funnel 这类重武器，直接用 golangci-lint（含 depguard + ruleguard）更划算。

---

## 交付物

| # | 文件 | 内容 |
|---|------|------|
| 1 | [01-extraction-manifest.md](01-extraction-manifest.md) | **精确抽离清单**：必搬文件（含行数）、3 处 repo 耦合改点、新 go.mod、搬迁步骤、~1.5 天工作量估算 |
| 2 | [02-catalog-A …](02-catalog-A-engine-meta-layering.md) ~ [F2](02-catalog-F2-http-obs-pg-adapter.md) | **完整能力清单**：293 条规则，逐条评级 / 机制类型 / 用途 / gocell 依赖 / 可移植性。按主题 7 个文件，每文件分批次 |
| 3 | [03-rule-reusability-assessment.md](03-rule-reusability-assessment.md) | **规则可移植性评估**：三档统计、可直接移植的 ~20 条点名、8 个可复用机制模式、为何 55% 专属 |
| 4 | [04-why-not-existing-tools.md](04-why-not-existing-tools.md) | **是否重复造轮子**：逐一对比 go/analysis / analysistest / ruleguard / depguard，裁决"薄重复但有据" |

### 交付物 #2 编目分册

| 分册 | 主题 | 规则数 |
|------|------|:------:|
| [A](02-catalog-A-engine-meta-layering.md) | 引擎自检 / meta-archtest / 分层 import | 33 |
| [B](02-catalog-B-outbox-rmq-saga.md) | Outbox / RMQ / Saga / Relay / Subscription | 40 |
| [C](02-catalog-C-cell-contract-assembly.md) | Cell / Contract / Assembly / Metadata | 44 |
| [D](02-catalog-D-codegen-scaffold-bootstrap.md) | Codegen / Scaffold / Bootstrap 装配 | 43 |
| [E](02-catalog-E-auth-session-security.md) | 认证 / 会话 / 凭证 / 安全 / 角色 | 45 |
| [F1](02-catalog-F1-hygiene-errcode-clock-test-ci.md) | 卫生：errcode / panic / clock / 测试纪律 / CI | 34 |
| [F2](02-catalog-F2-http-obs-pg-adapter.md) | HTTP / 可观测性 / Postgres / Adapter | 54 |

---

## 聚合看板

### 可移植性（293 条）

```
通用    ████████                          46  (16%)   ← 含 21 条引擎自检（随引擎走），真正可挑选 ~25
半通用  ███████████████                   87  (30%)   ← 收敛为 ~8 个可复用机制模式
专属    ███████████████████████████      160  (55%)   ← cell/contract/outbox/saga/session 语义，不可移植
```

### AI-robust 评级

```
Hard / Medium 占绝对多数，Soft ≈ 0
```
GoCell「严禁 Soft 立项」纪律的结果：搬走的规则要么编译期挡死、要么 archtest 静态挡死，不靠字符串约定。

### 引擎纯净度（已用 go list 核实）

```
引擎非测试代码  →  零 gocell 业务包依赖
第三方依赖      →  仅 golang.org/x/tools + golang.org/x/sync
repo 耦合点     →  仅 3 处（build-tag 清单 / generated 前缀 / 默认跳过目录），均易参数化
```

---

## 怎么读

- 想知道**搬不搬、怎么搬** → 先读 **#1**。
- 想知道**到底有哪些能力、哪条规则干什么** → 查 **#2** 对应主题分册。
- 想知道**哪些规则能抄进自己项目** → 读 **#3**。
- 想知道**为什么不用现成工具、是不是造轮子** → 读 **#4**。

> 注：`_master_data.txt` 是生成编目用的原始 ID→文件→依赖映射，可删。
