# 交付物 #4：为什么没用 go/analysis / analysistest / gorules DSL / depguard——是不是重复造轮子？

> 结论先行：**不是整体重复造轮子，但引擎层有一处"薄重复"且团队有据可查地主动选择了它。** archtest 实际是「在 `go/packages` 上自建了一个比 `go/analysis` 更轻、且多了一道安全闸的 Pass 模型」。它对 depguard 不是重复——它**主动把能交给 depguard 的都交了出去**。

---

## 0. 先厘清"轮子"指哪一层

| 候选工具 | 解决的问题 | 与 archtest 的关系 |
|---------|-----------|-------------------|
| **depguard** | 路径级 import 禁止 | archtest **依赖并复用**它，不重复 |
| **go-ruleguard (gorules DSL)** | AST 模式匹配（DSL 写规则） | 未采用；archtest 用全 Go 写规则 |
| **go/analysis + analysistest** | 标准 analyzer 框架（Pass / Fact / Requires DAG） | archtest **自建了等价但更轻的 Pass**，这是唯一的"薄重复" |

下面逐个说清"用了 / 没用 / 为什么"。

---

## 1. depguard —— 没有重复，反而是边界自觉的范例

`.golangci.yml` 里能直接读到 archtest 的自我克制：

```
# Architecturally equivalent to archtest's former LAYER-01..04 allow-list
#   ... [moved to depguard (.golangci.yml linters.settings.depguard.rules)]
```

`doc.go` 也写明：`LAYER-01..04` 这种"纯 import 路径禁止"**已经从 archtest 迁出，交给 depguard**。archtest 只保留 depguard **表达不了**的：

- `LAYER-05/06` 子包归属（"cell A 不能 import cell B 的 `internal/`"，需要 owner 语义）
- `LAYER-05T/06T` **传递闭包**（depguard 只看直接 import，看不到 N 层间接）
- `LAYER-08` 类型级缺席（走 `types.Scope()`，字符串扫描会误伤注释/struct tag）
- `LAYER-10` exported API 不得暴露具体 adapter 类型（需要类型信息）

**判定：对 depguard 零重复。** archtest 的载体决策原则（`.claude/rules/gocell/ai-robust.md`）明文规定"路径级 import ban → depguard"，这是分工不是重叠。

---

## 2. go-ruleguard（gorules DSL）—— 主动不用，理由成立

ruleguard 让你用一种类 Go 的 DSL 写 AST 模式（`m.Match("$x == nil")`）。archtest 没用它，全部规则是**纯 Go 测试函数**。

**为什么这个选择对 archtest 成立**（结合本项目宪法 `ai-robust.md`）：

1. **AI-robust 要"违反不可表达"，DSL 给不了类型系统级保证。** archtest 的 Hard 档大量依赖 Go 类型系统本身（sealed interface、unexported envelope、`*types.Package` 而非 `*packages.Package`、reflect 字段冻结）。这些在 ruleguard DSL 里**无法表达**——DSL 只能做语法/有限语义匹配，做不到"让违反编译不过"。
2. **archtest 的规则远超模式匹配。** 典型规则形态是「callsite allowlist + golden 字节锁 + 跨包 const 求值」，例如 `OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01`、`SPAN-SETATTR-REDACT-01` 的双向锁。ruleguard 的单模式匹配做不了这种组合断言。
3. **全 Go 可调试、可复用 helper。** 规则作者直接用 `EvaluateConstString` / `ResolveMethodCall`，比在 DSL 里拼模式更可控。

**代价（诚实）**：archtest 规则比 ruleguard 啰嗦得多——一条规则动辄几百行，而 ruleguard 可能 3 行。对"只想要简单模式检查"的项目，archtest 是重武器。

**判定：不是重复造轮子，是为 AI-robust 目标做的有意取舍。** 但若你的个人项目只需要"禁止某 AST 模式"，ruleguard 更划算。

---

## 3. go/analysis + analysistest —— 唯一的"薄重复"，但有白纸黑字的理由

这是最值得审视的一点。`pass.go` 的 `Pass`/`Run`/`RunTyped` 本质是在 `golang.org/x/tools/go/packages` 上**自建了一个 Pass 模型**，而没有用标准的 `go/analysis.Pass` + `analysistest.Run`。

引擎甚至**完全没有 import `go/analysis`**——它只在 `// ref:` 注释里参照其设计。`scanner/doc.go` 有专门一节解释：

> **# Why not go/analysis**
> GoCell archtests have no inter-rule dependencies (no Requires/FactType DAG), so the lighter AST scanner API is preferred over go/analysis.Analyzer.

### 3.1 团队给出的理由（成立）

go/analysis 的核心增量价值是 **analyzer 之间的依赖编排**：`Analyzer.Requires` 形成 DAG，`Fact` 在 analyzer 间传递，driver 负责拓扑排序与缓存。archtest 的规则**彼此完全独立**（一条规则一个 `Test*`，无 Requires/Fact），所以 go/analysis 的 DAG/Fact 机制对它是纯负担——白付框架复杂度，拿不到收益。

### 3.2 自建 Pass 还多换来一道闸（INV-1 防护）

archtest 的自建 Pass 不是简单重复，它故意做了一件 go/analysis 不做的事：

- `Pass.Pkg` 类型是 **`*types.Package`（go/types 标准库）而非 `*packages.Package`（x/tools）**。
- 因此规则作者**拿不到 `.Syntax`**，无法重新发起一次 `packages.Load`。
- 这从编译期消除了一类 bug（ADR 命名 **INV-1**）：拿 A 次 load 的 AST 节点去配 B 次 load 的 `types.Info`，会得到静默错误结果。

go/analysis 的 `Pass.Files` + `Pass.TypesInfo` 是平铺暴露的，挡不住这类误用。archtest 用"窄化的 Pass + depguard 禁止直接 import `go/packages` + meta-archtest 再查一遍"三道防线把它变成**不可表达**。这是 AI-robust 哲学（主要作者是 AI，要"违反不可表达"）的直接产物。

### 3.3 代价（诚实）

- **放弃了 analysistest 的 `// want "..."` 黄金范式**——archtest 自己实现了 `RunTypedDir`（红夹具独立 module）+ `AssertGolden` 来替代，确实是重写了 analysistest 已有的能力。
- **放弃了 go vet / golangci-lint 的 analyzer 生态对接**——archtest 规则只能 `go test` 跑，不能作为 `go vet` analyzer 被复用。
- 自建 Pass 的驱动逻辑（test-variant 排序、`*ast.File` 指针去重）是 ~200 行本可省掉的代码。

### 3.4 判定

**这是一处真实但理性的薄重复。** 衡量标准：
- 若目标是"接入 go vet / 标准 analyzer 生态 / 用 `// want` 写测试" → 用了 go/analysis 更好，archtest 在这点上是重复造轮子。
- 若目标是"AI-robust：让一类跨 load 误用编译期不可表达 + 不需要 analyzer DAG" → go/analysis 给不了 INV-1 闸，自建 Pass 是合理的。

GoCell 选了后者，并在 ADR `202605141519-adr-archtest-pass-funnel.md` 与 `scanner/doc.go` 留了决策记录——属于"知情的重复"，不是"无知的重复"。

---

## 4. 综合裁决

| 维度 | 是否重复造轮子 | 说明 |
|------|--------------|------|
| import 层禁止 | ❌ 否 | 主动交给 depguard，边界清晰 |
| AST 模式匹配 | ❌ 否（但更重） | 弃 ruleguard DSL 换全 Go + 类型系统级 Hard 保证 |
| Pass / 加载 / 测试范式 | ⚠️ **薄重复** | 自建 Pass 而非用 go/analysis，为 INV-1 闸 + 免 DAG 负担，有 ADR 背书 |
| 规则本身 | ❌ 否 | 313 条领域不变式，无任何现成工具能替代（见交付物 #2/#3） |

**一句话**：archtest 不是"不知道有 go/analysis 而瞎造"，而是"知道 go/analysis，判断它的 DAG/Fact 用不上、且它的平铺 Pass 挡不住 INV-1，于是造了一个更轻、约束更强的替代"。**重复的只是 Pass 驱动那 ~200 行，且换来了一道编译期安全闸。**

---

## 5. 对"独立给自己用"的影响

如果你把引擎抽出去自己用，这个"薄重复"决定了它的定位：

- 它**不会**变成一个 go vet 插件或 golangci-lint linter——它是个 `go test` 库。
- 它的卖点正是 go/analysis / ruleguard / depguard **都给不了**的那部分：**类型感知的、编译期不可绕过的架构不变式 funnel**（sealed holder / golden 字节锁 / callsite allowlist / `*types.Package` 窄化）。
- 反过来说：**如果你的个人项目用不到这种"重武器级"约束，直接上 golangci-lint（含 depguard + 内置 ruleguard 通道）就够了，搬这个引擎是过度工程。** 这条判断是交付物 #3 的前提。
