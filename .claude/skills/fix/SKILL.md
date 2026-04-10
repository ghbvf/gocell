---
name: fix
description: "问题诊断与修复: 验证问题是否存在+根因分析+调用链/数据流追踪+架构评估+复杂度分级+修复方案+可选关联PR/分支。当用户说'这个问题存在吗''帮我分析这个bug''诊断一下这个模块''修复这个问题'时触发。也支持接收多方审查报告（review findings）作为批量输入。"
argument-hint: "<问题描述|文件:行号|review报告路径> [--branch <name>] [--pr <number>] [--attach-to <branch>]"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent, AskUserQuestion]
---

# 问题诊断与修复

接收一个问题描述（可以是 bug 报告、backlog 条目 ID、文件:行号、自然语言描述、
**或一份多方审查报告**），执行完整的诊断→根因→修复流程。

**核心原则：先复现再修复，先理解再动手，先评估能不能做再决定做不做。**

> 设计参考：Google Passerine BRT Agent（复现测试先行 +30% 修复率）、
> AutoCodeRover（AST 级定位）、SWE-Search/Moatless（MCTS 回溯探索）、
> Meta SapFix/Getafix（模板→变异→回退分层策略）、Aider（逐编辑测试循环）。

---

## 输入解析

用户输入可能是以下任一形式：

```
# 单一问题
/fix session refresh 有并发问题
/fix cells/access-core/slices/sessionrefresh/service.go:81
/fix P3-TD-10

# 多方审查报告（批量输入）
/fix docs/reviews/202604061401-pr39-six-role/findings.md
/fix --from-review stage-6  （读取最近一次 S6 review 产出）

# 带 Git 关联
/fix "eventbus Close 和 Subscribe 有竞态" --branch fix/eventbus-race
/fix P3-TD-10 --pr 55
```

解析规则：
1. 如果包含 `文件路径:行号` → 直接定位到代码
2. 如果包含 backlog ID（如 `P3-TD-10`、`R1B1-01`）→ 从 `docs/backlog.md` 解析条目
3. 如果指向 review 文档（.md 文件含多条 findings）→ 进入**批量模式**
4. 如果是自然语言 → 用 Grep/Glob 在代码库中定位相关代码
5. `--branch` / `--pr` / `--attach-to` 参数控制修复的 git 关联方式

### 批量模式（多方审查报告）

当输入是 review 文档时：
1. 解析文档中的所有 findings（按 ID、文件、描述提取）
2. 对每条 finding 执行阶段 1（验证是否存在）
3. 输出汇总表：哪些确认存在（CONFIRMED）、哪些已修（RESOLVED）、哪些需要更多信息
4. **默认修全部 CONFIRMED 条目** — 不等用户选择，直接按分析结果决策：
   - CONFIRMED + C1 + 满足自动执行条件 → 直接修
   - CONFIRMED + C2 → 执行推荐方案（最小或彻底，由时机判断决定）
   - CONFIRMED + C3/C4 → 只输出方案，标注"需人工决策"
   - RESOLVED → 标注已修，跳过
   - CANNOT_VERIFY → 标注待确认，跳过
5. 按复杂度从低到高排序执行：先批量处理 C1，再逐条处理 C2，最后汇总 C3/C4 方案

---

## 阶段 1: 问题定位（必须完成）

### 1.0 Backlog 关联检查

修复开始前，先在 `docs/backlog.md` 中查找是否已有对应条目：

1. Grep 问题关键词 / backlog ID → 确认是否已登记
2. 已登记 → 读取条目，获取上下文（预估、依赖、状态）
3. 未登记 → 记录，修复完成后补登

### 1.1 找到问题代码

- 如果有明确文件路径：直接 Read 目标文件
- 如果是模糊描述，按精度递进搜索：

  **层 1: 结构感知搜索（优先）**
  ```bash
  # 搜索类型/接口定义
  Grep "type.*SessionService.*struct" --type go
  Grep "func.*Refresh" --type go
  # 搜索方法签名（而非调用）
  Grep "func \(.*\) Refresh\(" --type go
  ```

  **层 2: 语义搜索（层 1 不够时）**
  ```bash
  # 搜索错误码/常量关联
  Grep "ERR_SESSION" --type go
  # 搜索注释/doc
  Grep "// .*session.*refresh" -i --type go
  ```

  **层 3: 调用图搜索（跨包时）**
  使用 Agent(subagent_type=Explore) 从入口点 trace 完整调用链

- 如果三层搜索均无法定位 → **用 AskUserQuestion 向用户确认**

### 1.2 追踪调用链

从问题代码出发，**向上和向下**追踪完整调用链：

```
向上（谁调用了这个函数）:
  HTTP handler → service method → 问题函数
  
向下（这个函数调用了谁）:
  问题函数 → repository → database
```

具体操作：
1. Grep 函数名，找到所有调用方
2. Read 每个调用方，理解调用上下文
3. Read 被调用方，理解下游行为
4. 用 Agent(subagent_type=Explore) 做深度追踪（如果调用链跨 3 个以上包）

### 1.3 追踪数据流

识别关键数据从哪里来、经过哪些变换、到哪里去：

```
数据源 → 变换 1 → 变换 2 → 最终消费者
例: HTTP request body → JSON decode → service.Refresh() → session.Update → DB write
```

关注：
- 数据在哪一步可能被并发修改
- 数据在哪一步可能丢失或被静默忽略
- 错误在哪一步被吞掉或转换

### 1.4 确认问题是否存在

读完代码后，做出判定：

| 状态 | 含义 | 下一步 |
|------|------|--------|
| **CONFIRMED** | 问题真实存在，可以复现 | → 进入阶段 2 |
| **RESOLVED** | 问题已被修复（给出证据：哪行代码、哪个 PR） | → 向用户报告，结束 |
| **CHANGED** | 代码重构过，问题形态变化 | → 向用户描述新形态，确认是否继续 |
| **CANNOT_VERIFY** | 无法确认（缺少上下文、需要运行时验证） | → AskUserQuestion 请求更多信息 |

**输出格式：**

```markdown
## 诊断结果

**状态**: CONFIRMED / RESOLVED / CHANGED / CANNOT_VERIFY
**位置**: `文件路径:行号范围`
**调用链**:
  HTTP POST /api/v1/xxx
    → handler.XXX() [handler.go:30]
      → service.XXX() [service.go:69]
        → repo.XXX() [repo.go:42]
**数据流**:
  request.refreshToken → session object → new token pair → DB update
**问题描述**: （用自己的话总结，不是照搬 backlog）
```

### 1.5 复现测试（Reproduction Test First）

CONFIRMED 后、修复前，先构造一个能**复现问题**的测试用例：

1. 基于调用链和数据流分析，编写最小测试用例触发问题
2. 运行测试确认 FAIL（证明问题可复现）
3. 将此测试作为修复验收标准

| 场景 | 操作 |
|------|------|
| 已有测试可稍加修改复现 | 修改已有测试 + 确认 FAIL |
| 需新写测试 | 在 `_test.go` 新增 `TestXxx_Bug描述` |
| 并发问题 | 写 `go test -race` 可触发的竞态测试 |
| 无法在单测中复现（需运行时状态） | 标注 `RUNTIME_ONLY`，跳过此步 |

**为什么先写测试：**
- 迫使精确理解问题触发条件（不是模糊的"可能有问题"）
- 修复后直接有回归保护
- 避免"修了个寂寞"—— 测试一开始就没 FAIL 说明问题理解有误

---

## 阶段 2: 根因分析 + 复杂度分级（CONFIRMED 后执行）

### 2.1 为什么会有这个问题

从三个维度分析：

**代码层面：** 具体是哪行代码、哪个设计决策导致的
  - 例: "service.Refresh() 在 line 81-131 之间执行了非原子的读-检查-修改-写序列"

**架构层面：** 是否是分层/模块设计导致的系统性问题
  - 检查：这个问题是否可能在其他模块重复出现？
  - 检查：是否违反了 CLAUDE.md 中的架构约束？
  - 检查：是否是分层边界划分不当导致的？
  
**历史层面：** 为什么当初这样写
  - 如果能找到相关 commit/PR 信息，说明当时的决策上下文
  - 例: "Phase 0 优先跑通流程，TOCTOU 保护被延迟到 Phase 3"

### 2.2 影响范围评估

```
直接影响: 这个 bug 本身会导致什么？
间接影响: 修复这个 bug 会影响哪些其他模块？
同类问题: 其他模块是否有相同模式的问题？
```

用 Grep 搜索相同模式，例如如果问题是"读-检查-修改-写无原子保护"，
搜索其他 service 中是否有相同的 GetXxx → check → Update 模式。

### 2.3 复杂度分级

**必须对问题做复杂度判定**，这决定了后续方案的形态：

| 等级 | 判定标准 | 方案形态 |
|------|---------|---------|
| **C1 简单** | 改 1-2 个文件，不跨包，不改接口 | 直接修 |
| **C2 中等** | 改 3-5 个文件，跨 1-2 个包，接口不变 | 最小修复 + 可选彻底方案 |
| **C3 复杂** | 改 5+ 个文件，跨 3+ 个包，或需改 kernel 接口 | 必须给出三级方案（最小/彻底/重构） |
| **C4 架构级** | 需要新增/重构子模块，或改变数据流方向 | 只做方案设计，不直接执行 |

判定依据（按顺序检查）：
1. 修复涉及多少个文件？（`Grep` 搜索所有受影响的调用点）
2. 是否需要修改 `kernel/` 层的接口或类型？
3. 是否需要修改数据库 schema（migration）？
4. 是否影响 wire/bootstrap 层的组装逻辑？
5. 同类问题在其他模块是否重复出现？（1 处=局部，3+=系统性）

### 2.4 是否存在架构问题

判断标准：
- 如果只在一个地方出现 → **局部代码问题**，直接修
- 如果在 3+ 个地方出现相同模式 → **架构缺陷**，需要系统性修复
- 如果修复需要改 kernel/ 接口 → **接口稳定性问题**，需要慎重评估

如果判定为架构问题 → **用 AskUserQuestion 与用户沟通**，确认是做局部修复还是系统性重构。

### 2.5 历史修复搜索

在 git 历史中搜索同类问题的已有修复：

```bash
# 搜索 commit message 中的相关关键词
git log --oneline --all --grep="<问题关键词>" -- "*.go"
# 搜索曾经修改过同一文件的 fix commit
git log --oneline --all --grep="fix" -- <问题文件>
```

目的：
- 发现团队已有的修复惯例（follow existing patterns）
- 避免重复引入已经修过又退化的问题
- 如果找到相关历史修复 → 在方案中注明 `ref: <commit hash>`

**输出格式：**

```markdown
## 根因分析

**代码层面**: ...
**架构层面**: 局部问题 / 架构缺陷（说明理由）
**历史层面**: ...
**复杂度**: C1 / C2 / C3 / C4
**影响范围**:
  - 直接: ...
  - 间接: ... (列出受影响的文件)
  - 同类: ... (Grep 发现的相同模式，数量)
**历史修复**: (git log 发现的相关修复，或"无")
```

---

## 阶段 3: 修复方案设计

### 3.0 对标参考查询（C2+ 必须执行）

C2 及以上问题，在设计修复方案前，**必须逐层查询参考实现**。
三层参考按权威性递减排列，优先采纳上层的做法。

#### 层 1: Go 标准库官方实现（语言级权威）

问题涉及以下领域时，**先看标准库怎么做的，再动手**：

| 领域 | 标准库参考 | 关注点 |
|------|-----------|--------|
| 并发保护 | `sync`（Mutex / RWMutex / Once / WaitGroup / Map） | 锁粒度、Once 惯用法、copyChecker 模式 |
| 原子操作 | `sync/atomic` | Load/Store/CompareAndSwap 的正确用法 |
| Context 传播 | `context` | 取消传播、Value 的正确使用边界 |
| HTTP 处理 | `net/http`（Server / Handler / Transport） | Shutdown 优雅关闭、中间件链组合、超时设置 |
| 连接池 | `database/sql`（DB / Conn / Pool） | SetMaxOpenConns / SetConnMaxLifetime 策略 |
| IO 与资源释放 | `io`（Closer / Pipe / ReadAll） | defer Close 惯用法、Pipe 组合模式 |
| 错误处理 | `errors`（Is / As / Join / Unwrap） | 错误链设计、sentinel error vs 类型断言 |
| 密码学 | `crypto/*` | 常量时间比较、随机数生成 |
| 测试模式 | `testing`（T / B / TB） | Cleanup / Parallel / Helper 惯用法 |

**查询方式：**
```bash
# 示例：修复 graceful shutdown 问题 → 看标准库 Server.Shutdown
WebFetch https://raw.githubusercontent.com/golang/go/master/src/net/http/server.go
# 提取 Shutdown 方法的实现模式（sync.Once / context deadline / listener close 顺序）
```

**判定：标准库有直接对应实现 → 必须遵循其模式，不自创。**

#### 层 2: 组件官方库（基础设施权威）

问题涉及外部组件交互时，**查官方库的推荐用法和已知陷阱**：

| GoCell 模块 | 官方库 | GitHub 路径 | 重点关注 |
|-------------|--------|-------------|---------|
| adapters/postgres | `jackc/pgx/v5` | `jackc/pgx` | 连接池配置、事务隔离级别、COPY 协议、pgxpool 生命周期 |
| adapters/redis | `redis/go-redis/v9` | `redis/go-redis` | Pipeline/Tx 用法、连接池参数、Pub/Sub 重连、ClusterClient |
| adapters/rabbitmq | `rabbitmq/amqp091-go` | `rabbitmq/amqp091-go` | Channel 生命周期（不跨 goroutine）、Connection vs Channel 重连、Confirm 模式 |
| runtime/http | `go-chi/chi/v5` | `go-chi/chi` | 中间件顺序、RouteContext、子路由挂载 |
| runtime/auth/jwt | `golang-jwt/jwt/v5` | `golang-jwt/jwt` | SigningMethod 注册、Claims 校验顺序、kid 查找 |
| adapters/oidc | `coreos/go-oidc/v3` | `coreos/go-oidc` | Provider 缓存、Verifier 配置、JWKS 刷新策略 |
| adapters/s3 | `aws/aws-sdk-go-v2` | `aws/aws-sdk-go-v2` | Retry 策略、Context 超时、分片上传 |
| adapters/websocket | `nhooyr.io/websocket` | `nhooyr.io/websocket` | 并发写保护（单 writer）、Close handshake、心跳 |
| adapters/otel | `go.opentelemetry.io/otel` | `open-telemetry/opentelemetry-go` | TracerProvider 生命周期、Span 传播、Shutdown 顺序 |
| adapters/prometheus | `prometheus/client_golang` | `prometheus/client_golang` | Registry 隔离（测试用）、Collector 注册时机 |
| DB migration | `pressly/goose/v3` | `pressly/goose` | 版本锁、并发 migration 保护、Down 幂等 |
| 集成测试 | `testcontainers-go` | `testcontainers/testcontainers-go` | Container 生命周期、Parallel 隔离、Cleanup |
| 文件监听 | `fsnotify/fsnotify` | `fsnotify/fsnotify` | Watcher 重复事件、Rename 语义差异 |

**查询方式：**
```bash
# 示例：RabbitMQ Channel 竞态 → 看官方 _examples/ 和 README 警告
WebFetch https://raw.githubusercontent.com/rabbitmq/amqp091-go/main/README.md
WebFetch https://raw.githubusercontent.com/rabbitmq/amqp091-go/main/_examples/consumer/consumer.go
```

**同时检查官方 Issues 中的已知陷阱：**
```bash
WebSearch "<组件库名> <问题关键词> site:github.com/<owner>/<repo>/issues"
```

**判定：官方库有明确推荐模式 → 必须遵循；官方 Issues 有已知陷阱 → 必须在方案中规避。**

#### 层 3: 对标框架（设计模式参考）

查 `docs/references/framework-comparison.md`，找到问题模块的 primary/secondary 对标：

| GoCell 模块 | primary | secondary |
|-------------|---------|-----------|
| kernel/cell | uber-go/fx | go-kratos/kratos |
| kernel/outbox + idempotency | ThreeDotsLabs/watermill | micro/go-micro |
| runtime/http/middleware | go-kratos/kratos | zeromicro/go-zero |
| runtime/config | micro/go-micro | go-kratos/kratos |
| runtime/worker | zeromicro/go-zero | — |
| runtime/auth/jwt | micro/go-micro | go-kratos/kratos |
| pkg/errcode | go-kratos/kratos | zeromicro/go-zero |
| adapters/ | ThreeDotsLabs/watermill | micro/go-micro |
| cells/ 声明模型 | kubernetes/kubernetes | — |

```bash
# 示例：eventbus Close 竞态 → 看 Watermill 如何管理 Subscriber 生命周期
WebFetch https://raw.githubusercontent.com/ThreeDotsLabs/watermill/main/message/subscriber.go
```

#### 三层综合决策

```
层 1（Go 标准库）有做法 → 遵循，不偏离
  ↓ 标准库无直接对应
层 2（组件官方库）有推荐 → 遵循，注意已知陷阱
  ↓ 官方库无明确指导
层 3（对标框架）有实现 → 参考，可根据 GoCell 语境偏离但须注明理由
  ↓ 三层都无参考
WebSearch "<问题关键词> golang best practice" → 社区惯例
```

**方案输出中必须包含参考来源：**
```markdown
### 方案 B: Channel-per-goroutine 重连
**参考来源**:
  - Go 标准库: net/http server.go:2847 — Shutdown 用 sync.Once + channel 通知
  - 组件官方: rabbitmq/amqp091-go README — "Channel is not safe for concurrent use"
  - 对标框架: watermill subscriber.go:89 — 每个 handler 独立 channel + reconnect loop
  - 偏离: watermill 用 middleware 做重试，GoCell 用 ConsumerBase 内置，不需要额外 middleware 层
```

#### 何时跳过

- C1 问题 → 全部跳过
- 纯业务逻辑 bug（条件写反、映射缺失）→ 全部跳过
- C2 但只涉及层内改动且不涉及下表领域 → 只查层 3

**不可跳过的领域**（即使 C2 也必须查层 1 + 层 2）：
- 并发 / 锁 / atomic
- 连接池 / 资源释放 / 生命周期
- 重连 / 重试 / 超时
- 密码学 / 认证 / 令牌
- 事件发布 / 消费 / 幂等

---

### 3.1 方案分级（根据复杂度等级调整）

**C1 简单** → 直接给一个方案 + 修复计划，跳过多方案比较。

**C2 中等** → 给出两个方案：
- 方案 A: 最小修复 — 只改问题本身，不扩大范围
- 方案 B: 彻底方案 — 顺便解决相关的同类问题

**C3 复杂** → 给出三个方案：
- 方案 A: 最小修复 — 绷带方案，止血用，明确标注遗留什么
- 方案 B: 彻底修复 — 完整解决，但不改架构
- 方案 C: 重构方案 — 如果需要新接口/新模块，描述目标架构

**C4 架构级** → 只输出设计文档，不给修复计划：
- 目标架构描述
- 迁移路径（分几步从现状到目标）
- 与用户沟通后决定是否执行

每个方案必须说明：

```markdown
### 方案 X: (名称)
**改动范围**: 哪些文件、哪些函数
**原理**: 为什么这样改能解决问题
**优点**: ...
**缺点**: ...
**遗留**: 这个方案不解决什么（仅最小修复需要）
**预估改动量**: X 行新增 / Y 行修改 / Z 行删除
**参考来源**: (3.0 对标查询的结果摘要，C2+ 必填)
```

### 3.2 时机判断：现在做还是后面做

**必须给出明确的时机建议**，回答三个问题：

**Q1: 推荐现在做还是后面做？**

| 推荐 | 判定条件 |
|------|---------|
| **现在做** | 安全漏洞 / 运行时崩溃 / 阻塞其他工作 / 改动量 ≤ 50 行 |
| **本迭代做** | 有明确影响但不紧急 / 改动量 50-200 行 / 不阻塞他人 |
| **下迭代做** | 设计级问题 / 改动量 200+ 行 / 需要先完成其他前置工作 |
| **记录不做** | 理论风险但实际不触发 / 修复代价远大于收益 |

**Q2: 能不能现在做？**

检查前置条件：
- 是否依赖其他未完成的工作？（检查 `docs/backlog.md` 依赖关系）
- 是否与当前进行中的 PR/分支冲突？（`git branch -a` 检查活跃分支）
- 修复涉及的模块是否有其他人正在改？
- 如果需要改 kernel 接口，是否有消费方需要同步修改？

**Q3: 如果选最小修复，后续彻底方案什么时候做？**

- 给出建议的时间窗口（哪个 Tier / 哪个 Phase 之后）
- 说明最小修复的有效期（在什么条件下会变得不够用）

**输出格式：**

```markdown
## 时机建议

**推荐**: 现在做 / 本迭代做 / 下迭代做 / 记录不做
**理由**: ...
**能否现在做**: 能 / 不能（前置条件: ...）
**如果选最小修复**: 彻底方案建议在 ... 之后做，因为 ...
```

### 3.3 详细修复计划

对推荐方案，给出文件级的修改清单：

```markdown
## 修复计划

### 步骤 1: 修改 xxx.go
- [ ] 函数 A: 改动说明
- [ ] 函数 B: 改动说明

### 步骤 2: 修改 yyy_test.go
- [ ] 新增测试用例: 场景描述

### 步骤 3: 验证
- [ ] go build ./...
- [ ] go test ./path/to/package/...
- [ ] go test -race ./path/to/package/... (如果涉及并发)
```

### 3.4 决策逻辑（默认不需用户确认）

**根据复杂度和时机分析自动决策，不逐条问用户。**

| 复杂度 | 时机判断 | 自动决策 |
|--------|---------|---------|
| C1 | 现在做 | 直接修，不确认 |
| C1 | 下迭代做 | 记录到报告，不修 |
| C2 | 现在做 + 能做 | 执行推荐方案（最小或彻底由分析决定） |
| C2 | 现在做 + 不能做（有前置依赖） | 记录到报告，标注阻塞原因 |
| C3 | 任何 | 只输出三级方案到报告，标注"需人工决策" |
| C4 | 任何 | 只输出设计文档到报告，标注"需人工决策" |

**仅以下情况才用 AskUserQuestion：**
- C3/C4 用户追加了 `--auto` 参数（矛盾，需确认意图）
- 测试失败且无法自动修正
- 修复过程中发现新问题超出原始 scope

### 3.5 自动执行判定

当同时满足以下**全部条件**时，跳过用户确认，直接进入阶段 4 执行修复：

| # | 条件 | 检查方式 |
|---|------|---------|
| 1 | 复杂度 = C1 | 阶段 2.3 判定结果 |
| 2 | 改动 ≤ 2 个文件 | 修复计划中的文件列表 |
| 3 | 不改 kernel/ 层接口 | Grep 确认无 interface 签名变更 |
| 4 | 不改数据库 schema | 无 migration 文件 |
| 5 | 不改 bootstrap/wire 组装逻辑 | 不涉及 cmd/ 或 bootstrap 调用 |
| 6 | 有现成测试可验证 | 目标包已有 _test.go |

**可以自动执行的典型场景：**

```
- 删除死代码（如 HS256 分支）
- 修改错误码常量名/值不一致
- 补 compile-time interface check
- 修改子串匹配为精确映射
- 补缺失的测试用例
- 修复 doc.go 包注释冲突
- 添加 nil 检查或 guard clause
```

**不可以自动执行（必须确认）：**

```
- 涉及并发语义变更（mutex / channel / atomic）
- 修改接口签名
- 添加新依赖
- 修改数据流方向
- 任何 C2+ 的修复
```

自动执行时，在输出中标注 `[AUTO-FIX]`，让用户知道这是自动决策。
用户可通过 `--confirm` 参数强制所有修复都需确认（覆盖自动判定）。

### 3.6 Go 常见修复模板（C1 快速通道）

对 C1 问题，优先匹配已知修复模板，跳过多方案比较：

| 模板 ID | 问题模式 | 修复模式 |
|---------|---------|---------|
| T-NIL | 空指针解引用 | 添加 nil guard + early return |
| T-ERRWRAP | 裸 errors.New 对外暴露 | 替换为 errcode.New + fmt.Errorf wrap |
| T-LOCK | 读-改-写无锁保护 | 添加 mutex.Lock/Unlock（仅单实例场景） |
| T-DEFER | 资源泄漏（未 Close） | 添加 defer closer.Close() |
| T-DEADCODE | 不可达代码分支 | 删除死代码 + 补注释说明 |
| T-IFACE | 缺少 compile-time interface check | 添加 `var _ Interface = (*Impl)(nil)` |
| T-ERRIGNORE | `_ = someFunc()` 忽略错误 | 显式处理或 slog.Error 记录 |
| T-SHADOW | 变量遮蔽（:= 在 if 内） | 提取变量到外层作用域 |
| T-SLICE | append 到 nil slice | 初始化或 make 预分配 |
| T-CTX | context 传递断裂 | 补全 ctx 参数链 |

匹配流程：
1. 识别问题是否匹配已知模板
2. 匹配 → 直接应用模板修复，标注 `[TEMPLATE: T-xxx]`
3. 不匹配 → 进入正常方案设计流程

模板可通过实际使用积累扩展（在修复报告中标注 `NEW_TEMPLATE_CANDIDATE`）。

---

## 阶段 4: 执行修复

### 4.1 Git 关联处理

根据用户参数选择工作流。**修复完成且测试通过后，自动执行 git 收尾。**

```
无参数（默认）:
  → 在当前分支直接修改
  → 测试通过后不自动 commit（用户可能想审查变更）

--branch <name>:
  → 派发子 Agent（isolation: "worktree"）执行修复
  → 子 Agent 在隔离的 worktree 中完成：阶段 4.4 ~ 4.7
  → 测试通过后子 Agent 自动 commit + push + gh pr create
  → 主 Agent 接收结果（PR URL 或失败报告）

--attach-to <branch>:
  → checkout 到指定分支
  → 执行修复
  → 测试通过后自动 commit（追加，不 amend）+ push

--pr <number>:
  → gh pr checkout <number>
  → 在 PR 分支上追加修复
  → 测试通过后自动 commit（引用 PR number）+ push
```

**`--branch` 子 Agent 派发模板：**
```
Agent(
  description: "fix/<name> worktree 修复",
  isolation: "worktree",
  prompt: """
    在 worktree 中执行以下修复计划：
    - 问题: <诊断摘要>
    - 方案: <选定方案>
    - 修复计划: <步骤清单>
    - 复现测试: <1.5 产出的测试>
    执行逐编辑测试循环（4.4），全部通过后：
    1. git add <具体文件>（不 add -A）
    2. git commit（4.2 格式）
    3. git push -u origin <name>
    4. gh pr create（4.3 格式）
    返回 PR URL 和测试结果。
  """
)
```

**安全约束：** 只 add 修复涉及的文件；不 amend；push 前检查远端分支；PR 创建前检查重复。

### 4.2 Commit Message 格式

自动生成 Conventional Commits 格式：

```
fix(<scope>): <问题简述>

<根因一句话>

修复方案: <方案名称>
复杂度: C1/C2/C3
Refs: <backlog ID 或 review 来源>

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
```

scope 规则：kernel 层 → `fix(kernel/xxx)`，runtime → `fix(runtime/xxx)`，
cells → `fix(cells/xxx)`，pkg → `fix(pkg/xxx)`。

### 4.3 PR 创建格式

`--branch` 模式自动创建 PR，body 包含：

```markdown
## Summary
- **问题**: <一句话>
- **根因**: <一句话>
- **方案**: <方案名称>
- **复杂度**: C1/C2/C3

## 调用链
<阶段 1 的调用链>

## 变更文件
<文件列表 + 改动说明>

## 测试结果
- go build: PASS
- go test: PASS
- go test -race: PASS/N/A

## 遗留事项
<最小修复时说明遗留什么>

---
Generated with [Claude Code](https://claude.com/claude-code) via `/fix`
Refs: <backlog ID>
```

### 4.4 执行代码修改（逐编辑测试循环）

对每个修改步骤，执行 Edit-Test Loop：

1. Read 目标文件
2. Edit / Write 修改代码
3. `go build ./...` — 编译检查
4. `go test ./修改的包/...` — **立即运行测试**（含阶段 1.5 的复现测试）
5. 如果测试失败：
   - 分析失败原因
   - 如果是当前编辑引入 → 立即修正，重回步骤 2
   - 如果是暴露了后续步骤的依赖 → 记录，继续下一步骤
6. 测试通过 → 进入下一个修改步骤

问题在引入的那一步就被发现，不会在最后才发现一堆互相纠缠的失败。

### 4.5 最终测试

全部修改完成后，运行完整测试：

```bash
go build ./...
go test ./path/to/modified/package/...
go test -race ./path/to/modified/package/...  # 涉及并发时
go test ./src/kernel/...                       # 改了 kernel 时
```

确认阶段 1.5 的复现测试从 FAIL → PASS。

### 4.6 测试失败处理（分层回退策略）

**Round 1-2: 在当前方案上迭代修正**
1. 分析失败原因（读错误信息 + 对比预期行为）
2. 修正代码，重新进入逐编辑测试循环

**Round 3: 回溯到替代方案（Backtrack）**
1. `git stash` 当前修改
2. 重新审视阶段 3 的方案列表
3. 如果有备选方案 → 切换到备选方案重新执行阶段 4
4. 如果方案 A 和 B 都失败 → 进入 Round 4

**Round 4: 降级处理**

| 情况 | 操作 |
|------|------|
| C1 问题，两个方案都失败 | 回滚，标注 ESCALATE，输出诊断报告 |
| C2 问题 | 回滚到最小修复方案（即使不完美），标注遗留 |
| 已有测试与修复逻辑冲突 | 停止，AskUserQuestion |

回滚方式：
- 无 git 关联: `git checkout -- <修改的文件>`
- 有 git 关联: `git reset HEAD~1 && git checkout -- <修改的文件>`

关键区别：不是在同一条路上死磕 3 轮，而是第 3 轮切换到完全不同的修复路径。

### 4.7 验证修复

重新执行阶段 1 的定位逻辑，确认：
- 原问题代码已被替换
- 数据流已正确保护
- 测试覆盖了问题场景

### 4.8 Git 收尾（测试通过后自动执行）

按 4.1 的关联模式自动执行 commit → push → PR。

---

## 阶段 5: 输出报告

三种报告形态，共享字段：问题（来源/位置/状态/复杂度）、调用链、数据流、根因。

| 形态 | 触发条件 | 额外字段 |
|------|---------|---------|
| **A 诊断报告** | 未执行修复 | 方案概要表（方案/改动量/解决程度/风险）+ 时机建议 |
| **B 修复报告** | 执行了修复 | 选择的方案 + 变更文件表 + 测试结果 + 遗留事项 + Git 关联（分支/PR） |
| **C 批量验证** | 来自审查报告 | 验证汇总表（CONFIRMED/RESOLVED/CHANGED） + 分级表（ID/复杂度/时机） + 执行顺序 |

---

## 报告保存

所有报告自动保存到 `tools/docs/` 目录，命名遵循 CLAUDE.md 规则（`yyyyMMddHHmm-编号-实际功能或问题.md`）：

| 类型 | 文件名格式 | 示例 |
|------|-----------|------|
| 诊断报告 | `yyyyMMddHHmm-diagnose-{问题简称}.md` | `202604091430-diagnose-session-race.md` |
| 修复报告 | `yyyyMMddHHmm-fix-{问题简称}.md` | `202604091430-fix-eventbus-close.md` |
| 批量验证 | `yyyyMMddHHmm-review-verify-{来源}.md` | `202604091430-review-verify-pr39.md` |

编号部分用类型前缀（`diagnose` / `fix` / `review-verify`）替代序号，`date "+%Y%m%d%H%M"` 取当前时间。

## Backlog 更新（修复完成后必须执行）

修复完成后，同步更新 `docs/backlog.md`：

| 修复结果 | Backlog 操作 |
|---------|-------------|
| FIXED（修复成功） | 状态改为 `✅`，追加 PR 编号 |
| ESCALATE（需升级处理） | 状态不变，追加诊断结论和阻塞原因 |
| 发现新问题 | 新增条目到对应 Tier，标注来源 `(discovered via /fix <原问题>)` |
| 未登记的问题被修复 | 补登条目 + 标 `✅` |

---

## 沟通规则

**默认按分析结果自动决策。** 仅以下情况用 AskUserQuestion：
- 无法定位问题代码
- 测试失败且 4 轮回退后仍无法修正
- 修复过程中发现新问题超出原始 scope
- C3/C4 用户追加了 `--auto` 参数（矛盾，需确认意图）
