# 046 Spring 哲学对照与简化路线

**生成日期**：2026-05-22
**性质**：策略对照 + ROI 排序的路线图（非单 PR 实施计划）。每个 P0/P1 条目立项时必须再产出独立的 PR 级实施计划 + ADR。
**触发**：用户 2026-05-22 提问「GoCell 能像 Java Spring 那样大规模简化业务开发吗？」要求出分析 + 收敛路线。
**基准对照**：Spring 全家桶哲学（IoC + AOP + Convention-over-Config + Starter 生态）。

**与同目录既有 plan 的关系**：

| 既有 plan | 视角 | 与本文关系 |
|---|---|---|
| `202605131500-004-capability-gap-analysis.md` | 内部能力缺口（A–P 16 域，35 缺口） | 视角不同。004 看「框架自己缺什么能力」；本文看「业务开发者写多少胶水」 |
| `202605162100-005-framework-capability-roadmap-plan.md` | W0–W10 新增能力抽象 | 不同 lane。005 在做广组合能力；本文聚焦"已有能力的开发者体验包装"，与 005 正交可并行 |

> **本文不重述 004/005 的能力清单**，只在「业务开发者每天碰几个文件、写几行胶水」这一维度评估当前体验。

---

## 0. 一句话结论

**GoCell 能简化到 Spring Boot 60% 体感，但拿不到 Spring 全家桶哲学的精髓**——Spring 简化的本质是「运行时反射 + AOP 织入 + 约定扫描」，GoCell 的工程宪法（`.claude/rules/gocell/ai-robust.md` AI-robust 三档）把这三样判了 Soft。GoCell 能逼近 Spring 的「业务侧代码量」，但走的是另一条路：**重 codegen + 重 archtest + 重元数据声明，换编译期可证明的契约闭环**。

定位裁决：GoCell 的目标用户是「愿意为编译期契约保证投入 onboarding 成本的工程团队」，不是「想要 Rails/Spring 式 5 分钟上手的业务团队」。

---

## 1. Spring 全家桶哲学的 4 个支柱（基准）

| 支柱 | Spring 实现 | 业务侧体感 |
|------|-----------|----------|
| **IoC / DI** | 运行时反射扫描 `@Component`，自动按类型/名字装配 | 写一个 `@Service`，构造函数参数被自动注入 |
| **AOP** | 字节码增强 / 动态代理织入 `@Transactional` `@Cacheable` `@Async` | 在方法上贴注解，横切能力即得 |
| **Convention over Configuration** | 命名约定 + classpath 扫描 + 默认 bean | 几乎不写 XML，按惯例命名就 work |
| **Starter 生态** | `spring-boot-starter-xxx` 拖进依赖，相关 bean 全部自动 wire | 加一行依赖 → 加一段配置 → 能力上线 |

四个支柱叠加，效果是：**新手能在不懂底座的前提下产出可上线代码**。

---

## 2. GoCell 当前在每个支柱上的位置

### 支柱 1：IoC / DI —— 差 Spring 一档（架构决定，不可补齐）

- **现状**：`Cell.Init(ctx, reg)` 手写 + `Option` 链 + codegen 派生 `slice_gen.go` / `cell_gen.go`。composition root（`cmd/{id}/*_module.go`）全手工 ~13k LOC。
- **与 Spring 差距**：
  - Spring 用反射在 runtime 自动发现 `@Autowired` 字段；GoCell 工程宪法**禁止 reflection 自动装配**（`ai-robust.md` AI-robust 三档——reflection 装配违反不可表达性，列为 Soft）。
  - Spring 的 `@Component` 扫 classpath；GoCell 要求 cell.yaml + slice.yaml + assembly.yaml 三处显式声明。
- **能补齐的部分**：codegen 可以从 cell.yaml + slice metadata 自动生成 `{cell}_module.go`（K#10 已部分尝试，手写部分仍多），把 13k LOC 压到 3-4k。这是**「compile-time DI」**——Wire / Dagger 路线，不是 Spring 路线。
- **结论**：**不能拿到 Spring 体感，能拿到 Wire/Dagger 体感**。这是宪法约束下的最优解。

### 支柱 2：AOP —— 部分打平，剩余部分原理性达不到

| 横切能力 | Spring | GoCell |
|---------|--------|--------|
| 事务 | `@Transactional` AOP 织入 | `txRunner.RunInTx(ctx, fn)` 手工 wrap，**outbox 自动参与同一事务** |
| 可观测（trace/metric/log） | `@Traced` / micrometer 自动织入 | `runtime/http` middleware 堆栈 + `kernel/wrapper` 自动填 cell label，**业务无感知** |
| 鉴权 | `@PreAuthorize` | `auth.Mount` + RouteGroup Policy 路由级声明 |
| 限流 / 熔断 | `@RateLimiter` `@CircuitBreaker` | middleware 堆栈，路由级声明 |
| 缓存 | `@Cacheable` | 无声明式，业务手写 |
| 异步 | `@Async` | 无，业务手写 goroutine |

- **能补齐**：缓存可做声明式（contract.yaml 声明 cacheKey + ttl，codegen 生成 wrapper），异步可做"声明式 task contract + worker"。
- **不能补齐**：`@Transactional` 这种"在任意 method 贴注解就织入事务"——Go 无 AOP，编译期 codegen 又只能对**接口边界**起作用，不能对 method 内部任意调用织入。
- **结论**：**AOP 维度 GoCell 在框架边界（HTTP handler / event consumer / RouteGroup）已经打平甚至略优**（自动可观测、auth 路由级声明），业务内部的细粒度横切（method 级缓存/事务嵌套）原理性达不到。但这部分**Spring 的注解 AOP 本身就是公认的"魔法陷阱"**（嵌套事务、self-invocation 失效、proxy 边界混淆），GoCell 的显式 wrap 是更稳的工程实践。

### 支柱 3：Convention over Configuration —— 方向相反

Spring 是 **"约定在前、配置稀缺"**：按类名/方法名自动扫描，配置只用来覆盖约定。
GoCell 是 **"配置在前、约定隐形"**：contract.yaml / cell.yaml / slice.yaml / assembly.yaml 四源全显式声明，约定只活在 codegen 模板内部。

**两者哲学相反，不存在"补齐"路径**。GoCell 的设计选择背后有明确理由：
- 反 reflection（AI-robust Hard 档要求）
- 反"按命名匹配"（隐形依赖）
- 反"约定漂移到运行时才暴露"（archtest 把违反挡在编译期/CI）

**结论**：**GoCell 永远不会变成 Spring 体感的 convention-over-config**。它能做的是**让"配置"本身变得不重复**——通过 codegen funnel 让 cell.yaml 单点声明 → 派生到 cell_gen / module_gen / metadata / docs，让开发者觉得"YAML 只写一次"。

### 支柱 4：Starter 生态 —— GoCell 的真正机会

Spring 的 starter 不止是"代码模板"，而是 **"加一行 import + 一段配置 → 一整组 bean 自动 wire + 默认配置生效"** 的整体能力。

GoCell 当前的能力包：
- adapters/postgres、adapters/redis、adapters/rabbitmq 等是**手工 wire**的（要在 `*_module.go` 里加 Option 链）
- 没有 `import _ "gocell.io/starter/redis"` 这种 side-effect 注册路线（理由同上：反隐式扫描）

**能做到的 starter 形态**：
1. **assembly-level capability bundle**：`assembly.yaml` 中声明 `capabilities: [redis, postgres, otel-stdout]`，codegen 派生对应 module.go 片段 + 默认 config block。
2. **cell-level capability hint**：cell.yaml 声明 `requires: [redis-cache, outbox-pg]`，codegen 在 cell_gen.go 注入对应 Option 调用。
3. **runtime capability provider**：`runtime/cap/{redis,pg,...}` 提供标准 ProviderFunc，业务侧不再手写 wire。

**结论**：**这是 GoCell 距离 Spring 体感最近、性价比最高的投资方向**。本质是把"capability 拼装"从 `*_module.go` 手工 Option 链，下沉到 codegen 模板 + capability provider 库。

---

## 3. 总体判断（评分卡）

| 维度 | 当前水平 | 投资后水平 | 跟 Spring 比 |
|------|---------|----------|-------------|
| **代码量** | Slice ~200 LOC、Cell ~500 LOC、Composition Root ~13k LOC | Slice ~100、Cell ~200、Root ~3-4k | 略多于 Spring Boot Component，量级相当 |
| **横切能力** | 边界横切打平 / 业务内部 method 级达不到 | 同 | 边界打平；内部 method 级原理性差 1 档 |
| **上手成本** | 需理解 contract/cell/slice/assembly 四源模型 | 同（哲学不变） | 比 Spring 高 1.5-2 倍 |
| **可维护性** | 编译期 + CI 期把契约违反全部挡下，无运行时惊喜 | 同 | **明显优于** Spring（无反射 AOP 魔法陷阱） |
| **生态** | 当前 starter 体感缺失，是最大短板 | 60-70% Spring Starter 体感 | 最值得投资的差距 |

---

## 4. 收敛路线图（按 ROI 排序）

> **术语澄清**：本节 P0/P1 表达"ROI 排序档位"（P0 = 第一波必做，P1 = 第二波）。**与 Project v2 `Priority` 字段不同语义**——后者按 `docs/backlog/20260520/RERATING-RUBRIC.md` "P0 红线"仅 incident-driven 才进 P0；本路线 6 条均为架构 refactor，对应 Project Priority 顶到 P1（ROI-P0 三条）/ P2（ROI-P1 三条）。

### P0 — 高 ROI、与宪法对齐、补齐 starter 形态

**P0-1：Capability Provider 标准化（runtime/cap/）** — Issue [#854](https://github.com/ghbvf/gocell/issues/854)（Project P1 / Cx4）。把 adapters/{redis,postgres,rabbitmq,...} 上面套一层 `cap.RedisProvider` / `cap.PGProvider` 标准接口；assembly.yaml 新增 `capabilities` 字段，codegen 在 modules_gen.go 注入 provider wire。预期效果：composition root 从 ~13k LOC 压到 ~4k LOC。

**P0-2：Cell module.go 自动化（K#10 收尾）** — Issue [#855](https://github.com/ghbvf/gocell/issues/855)（Project P1 / Cx3）。从 cell.yaml `requires` + slice metadata，codegen 完整生成 `{cell}_module.go`，手写部分只剩"业务专属配置覆盖"。预期效果：新建 cell 的 wiring 工作量降 70%。

**P0-3：Event consumer 三处对齐自动生成** — Issue [#856](https://github.com/ghbvf/gocell/issues/856)（Project P1 / Cx2）。当前 contract.yaml `endpoints.subscribers` + slice.yaml `contractUsages` + cell.go `reg.Subscribe` 三处手对齐（参见 `.claude/rules/gocell/eventbus.md` §"声明对齐约束"），改为 slice.yaml `contractUsages[role=subscribe]` 单源 → codegen 派生其余两处。预期效果：新增 event consumer 从 4-5 文件编辑降到 2 文件。

### P1 — 中 ROI、补齐声明式横切能力

**P1-4：声明式 Cache wrapper** — Issue [#857](https://github.com/ghbvf/gocell/issues/857)（Project P2 / Cx3）。contract.yaml `endpoints.http.cache: {key, ttl, invalidateOn}` 声明，codegen 生成 handler wrapper 自动接 cap.RedisProvider。**不做 method-level `@Cacheable`**（违反"反隐形织入"原则）。

**P1-5：声明式 Config 注入** — Issue [#858](https://github.com/ghbvf/gocell/issues/858)（Project P2 / Cx2）。cell.yaml `configBindings: [{path: "session.timeout", into: "Service.SessionTimeout"}]` 声明，codegen 在 cell_gen.go 注入 Config.Get + OnConfigReload 回调。**不做 `@Value`**——保持显式声明，仅消除胶水。

**P1-6：Scaffold 提速** — Issue [#859](https://github.com/ghbvf/gocell/issues/859)（Project P2 / Cx2）。`gocell scaffold cell {id}` / `scaffold slice {cell}/{slice}` / `scaffold consumer {cell}/{slice} --event={spec}`，一条命令产出 cell.yaml + slice.yaml + skeleton + 第一个 conformance test。

### P2 — 低 ROI、长期投资（不进当前 backlog）

- **Starter 包发布机制**：`gocell-starter-{name}` 形式（独立 repo 或同 monorepo `starters/` 子目录），每个 starter 含 capability provider + 默认 config schema + onboarding doc。示例 starter：otel-stack / vault-auth / s3-blobstore。
- **OnBoarding doc 重组**：从"按层文档"（kernel/runtime/cells 各自一份）改为"按任务文档"（"我要加一个 HTTP endpoint" / "我要订阅一个 event" / "我要新建一个 cell"），每个任务 doc 30 行内打通并附 scaffold 命令。

### 反模式清单（明确不做）

- ❌ runtime reflection 自动装配（违反 AI-robust Hard 档）
- ❌ method-level `@Transactional` / `@Cacheable` 注解织入（违反"显式优于隐式"）
- ❌ classpath / package scan 自动发现 cell / slice / handler（违反"约定漂移到运行时"反对）
- ❌ Spring SpEL 这种 runtime 表达式（违反"编译期可证明"）

---

## 5. 立项前置门（必须先答）

**DG-0：本路线与 005 路线的优先级关系**
- 005 是「做广能力组合」（新增框架抽象）
- 本路线是「降低已有能力的使用成本」（减少胶水代码）
- 二者**正交、可并行**，但当资源紧张时建议**优先本路线**（直接改善每天写代码的业务团队体验，ROI 更直接），005 W0–W10 大多数 Wave 可暂缓。

**DG-1：AI-robust 立项门**（同 005 §0 DG-1）
P0-1 / P0-2 / P0-3 三项均引入新的 codegen funnel + slice/cell.yaml schema 字段，立项时需给 **AI-robust 三档评级**（Hard / Medium / Soft），Soft 严禁立项。具体到本路线：
- P0-1 `capabilities` 字段 → 上游 Hard（assembly.yaml schema 强制 enum）+ 下游 Hard（archtest 守 module.go 不得手工绕过 provider）
- P0-2 `requires` 字段 → 同上
- P0-3 单源派生 → 上游 Hard（contractUsages 是唯一权威源）+ 下游 Hard（archtest 守 contract.yaml subscribers 段必由 codegen 派生，不得手写）

**DG-2：与 005 的能力依赖**
P0-1 落地需要先确定 capability provider 接口形态。005 W0/W1 若涉及 capability registration / module wire，本路线 P0-1 应**等待 W0 接口稳定后再启动**，避免双源接口竞争。

---

## 6. 验证

1. **概念对齐**：本文 + Spring 四支柱对照表能否被 reviewer 接受为"GoCell 设计哲学的对外解释口径"
2. **路线图回灌**：P0-1/P0-2/P0-3 + P1-4/P1-5/P1-6 六项已登记为 GitHub Issue + Project v2 backlog 条目（cap-XX + flag-planned + type-feat）
3. **样本验证**：P0-1 落地后挑一个真实 cell（建议 examples/ssobff 系列）重写 module.go，对比改造前后的 LOC 与"业务开发者需理解的概念数"
4. **跨语言对照**（可选）：与 Kratos / go-zero 团队的实际框架使用者沟通，校准"Go 生态下业务开发者对'框架简化度'的实际期望"，避免按 Java 直觉过度承诺
