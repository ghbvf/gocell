# Plan 044 — Journey 体系修正

## §0 Context

三次同源累积证据：
1. J-useronboarding 3 manual + backlog 软处理（PR #572 ship）
2. J-confighotreload 测错方向 — config-publish 走 consumer 路径冒充 producer 验收 / access-apply 不传 WithConfigGetter 走 nil-getter 死路（PR #590 关闭）
3. J-typed-envelope-roundtrip 体系错位 — 6 criterion 全测 pkg/ framework invariant（PR #591 关闭）

根因：
- **testing affordance 缺失**：tests/integration 受 Go internal-package 屏障，platform cell 层无公开 testutil 包，致 producer-side / state-apply-with-dependency 形态 journey criterion 在 gocell 内部 + 下游 import gocell 项目结构性不可达
- **journey 体系定位漂移**：被误用为"任何想系统化验收的不变量都往里塞"的兜底
- **软处理累积无反馈环**：同模板 backlog 重复登记不触发升格

---

## §1 立即随本 plan PR 落地

### 1.1 删 `journeys/J-typed-envelope-roundtrip.yaml`

### 1.2 修改 `journeys/status-board.yaml` — 删除 entry

```yaml
- journeyId: J-typed-envelope-roundtrip
  state: doing
  risk: low
  blocker: ""
  updatedAt: 2026-05-06
```

### 1.3 新建 `journeys/README.md`

````markdown
# Journeys — 业务验收规格

## 定义

Journey 是**业务/产品视角的端到端验收**。每个 `J-*.yaml` 必须对应一个能由产品/业务方陈述的具体业务场景，例如：

- "新用户首次登录可以成功签发 session"（J-useronboarding）
- "连续失败登录后账户自动锁定，管理员可手动解锁"（J-accountlockout）
- "登录事件落入审计 hash chain 且可查询"（J-auditlogintrail）

每条 `passCriteria` 必须对应该业务场景下的具体可观测断言。

## 与其他验收体系的边界

| 体系 | 验收语义 | 单位 | 视角 | 归属判断 |
|------|---------|------|------|---------|
| **journey** | 业务场景端到端可用 | `passCriteria` | 业务方 / 产品 / 终端用户 | 产品方能用业务语言说出"用户能 X" / "系统在 Y 场景能保持 Z" |
| **archtest** | framework 不变量 / 架构约束 | `INVARIANT: <ID>` rule | 架构维护者 | 约束是关于代码/调用关系/类型结构本身，与业务无关 |
| **cell conformance test** | cell 公开 interface 契约 | 一组 case 跑遍所有实现 | 下游 cell consumer | 约束是关于某 interface 的所有实现都必须满足的行为契约 |
| **pkg unit test** | 共享工具行为正确 | table-driven | 任何 import 该 pkg 的代码 | 测的是 pkg/ 层公开函数的输入→输出正确性 |

## 反模式（禁止作为 journey 立项）

1. **pkg layer behavior verification** — 测 `pkg/httputil.WriteError` / `pkg/errcode.Error.MarshalJSON` / `pkg/redaction.RedactSlogAttr` 等共享工具行为
2. **framework invariant** — 测 envelope shape / PII safety / dependency direction 等架构约束
3. **archtest 守卫的运行时重复** — 已有 archtest 静态守的约束不应再以 journey criterion 形式跑 runtime smoke
4. **interface conformance** — 测某 interface 所有实现的行为一致性

判定方法：候选 criterion 的主谓宾不含业务实体词（user / session / config / role / policy / device / order / audit / event 等），大概率不是 journey。

## Lifecycle 状态机

- `experimental` — 设计中 / 测试覆盖未稳定 — `board.state` 必须 `todo`
- `active` — 至少 1 个 mode:auto criterion + 有 docker-free seam — `board.state` 可 `doing` 或 `done`
- `deprecated` — 已废弃 — `board.state` 不约束

由 `kernel/governance/rules_journey.go::JOURNEY-STATUS-LIFECYCLE-01` 强制。

## passCriteria mode

- `mode: auto` — 必须有 `checkRef: journey.<id>.<suffix>`，解析到 `tests/integration/` 内 `TestJ<CamelCase(id)><CamelCase(suffix)>` 测试，`-tags=integration` 跑通，docker-free
- `mode: manual` — 必须 inline 注释说明为什么不能 auto + 引用 backlog ID 跟踪升级路径

mode:manual 合法理由：
- 架构上不可达 docker-free seam — 必须 backlog 引用 unblock 条目
- 由更强的静态守替代（archtest rule）— 必须 inline 引用 archtest rule ID

mode:manual 非合法理由：
- "现在没时间写" / "测试复杂" / "等业务完整后再补"（这种情况应当 lifecycle: experimental）

## 新建 journey 前自查

1. 这是不是真正的业务场景？产品方能用一句话说出来吗？
2. passCriteria 主谓宾是不是业务实体？
3. 是否已有 archtest / cell conformance / pkg unit test 覆盖？如有，不要重复
4. 每个 mode:auto criterion 的 seam 在 docker-free 下可达吗？参考 `.claude/rules/gocell/cell-patterns.md` 中 criterion semantic form → seam 选择表

## 现有 journey 一览

见 `journeys/status-board.yaml`。每条 lifecycle / state / 修复路径见 `docs/backlog/cap-14-tooling.md` 中 `JOURNEY-*` 系列条目。
````

---

## §2 任务 1 — Platform Cell Testutil Packages

**目标**：消除 tests/integration 内 producer-side / state-apply-with-dependency 形态 journey criterion 的结构性不可达，同时为下游 import gocell 项目提供同源 testing affordance。

### 2.1 设计原则

每个 platform cell 子树内加 `{X}test/` 公开包。Go internal-package 屏障对自子树是开的 → testutil 包可 import internal/ports 拿真接口，对外只暴露 `*Service` 出口 + testutil 自定义 Fake 类型，**internal 类型不外溢**。

### 2.2 `cells/configcore/configcoretest/` 包设计

文件结构：
```
cells/configcore/configcoretest/
├── doc.go                  # 包级 godoc：用途 + 与 production wiring 的关系 + import 范围约束
├── builders.go             # BuildWriteService / BuildSubscribeService
├── fakes.go                # FakeConfigRepository
└── builders_test.go        # testutil 自身的单元测试
```

> OutboxRecorder 不在本包内重复实现。复用 PR0 提供的 `kernel/outbox/outboxtest.Recorder`（路径含 `*test` 段，已被 `TESTUTIL-BOUNDARY-01` 自动 cover production-import 边界）。

公开 API：

```go
// BuildWriteService 构造 docker-free 的 configwrite.Service + 关联 Recorder。
// 默认装配：FakeConfigRepository（in-memory map） + DemoCellTxManager + outboxtest.NewRecorder() 替代 NoopEmitter + DiscardHandler logger + clock.Real。
// opts 允许覆盖任一组件，但不暴露 internal/ports 类型 — 走 testutil 包自定义 BuildWriteOption 类型。
func BuildWriteService(t *testing.T, opts ...BuildWriteOption) (*configwrite.Service, *outboxtest.Recorder)

// BuildSubscribeService 构造 docker-free 的 configsubscribe.Service。
// 默认装配：clockmock.New(testAnchor) + DiscardHandler logger + NopMetrics + TombstoneTTL=defaultTombstoneTTL。
func BuildSubscribeService(t *testing.T, opts ...BuildSubscribeOption) *configsubscribe.Service

// BuildWriteOption / BuildSubscribeOption 是 testutil 自定义 option 类型，不复用 internal Option 类型。
type BuildWriteOption func(*buildWriteConfig)
func WithWriteRepository(r FakeConfigRepository) BuildWriteOption
func WithWriteClock(c clock.Clock) BuildWriteOption

// FakeConfigRepository 是 internal/ports.ConfigRepository 的 testutil 实现。
// 内部用 sync.Map 持久化 entries，支持 Create / Update / Delete / GetByKey / List / Count；
// 同时暴露 testutil-only 探查方法用于断言：
type FakeConfigRepository struct { /* ... */ }
func NewFakeConfigRepository() *FakeConfigRepository
func (r *FakeConfigRepository) Snapshot() []domain.ConfigEntry        // 当前所有 entries 拷贝
func (r *FakeConfigRepository) CallsOf(method string) []FakeCall      // 方法调用记录（含参数）
func (r *FakeConfigRepository) Reset()                                // 清空状态 + 调用记录
```

### 2.3 `cells/accesscore/accesscoretest/` 包设计

文件结构：
```
cells/accesscore/accesscoretest/
├── doc.go
├── builders.go                  # BuildConfigReceiveService / BuildIdentityManageService
├── fake_config_getter.go        # FakeConfigGetter
├── fake_user_repo.go            # FakeUserRepo
├── fake_role_repo.go            # FakeRoleRepo
├── credential_invalidator.go    # 真 *credentialinvalidate.Invalidator 构造 helper（不 Fake，详见下方说明）
└── ...
```

> Ports lock（PR A 实施前已勘察 lock）：accesscore `internal/ports/` 实际暴露的 interface 是 `ConfigGetter` / `UserRepository` / `RoleRepository`；**不存在** `RbacAssign` / `CredentialInvalidator` 名字。`identitymanage.NewService` 第二参数是 concrete `*credentialinvalidate.Invalidator`（非 interface），不可 Fake — testutil 构造真实例，其内部依赖（如有）才 Fake。

公开 API：

```go
// FakeConfigGetter 实现 internal/ports.ConfigGetter，stubs 按 key 返回预设 entry 或错误。
type FakeConfigGetter struct { /* ... */ }
type ConfigGetterStub struct {
    Entry *domain.ConfigEntry  // 当前 entry；nil 走 Err
    Err   error                // 优先级高于 Entry；预设 errcode.ErrConfigNotFound / ErrAuthUnauthorized 等触发 handler 不同分支
}
func NewFakeConfigGetter(stubs map[string]ConfigGetterStub) *FakeConfigGetter
func (g *FakeConfigGetter) Calls() []string                           // 按调用顺序记录 GetEntry 的 key 参数
func (g *FakeConfigGetter) Reset()

// BuildConfigReceiveService 构造 configreceive.Service 默认带 FakeConfigGetter（空 stubs，所有 key 走 ErrConfigNotFound）。
func BuildConfigReceiveService(t *testing.T, opts ...BuildReceiveOption) *configreceive.Service
func WithReceiveConfigGetter(g *FakeConfigGetter) BuildReceiveOption

// FakeUserRepo 实现 internal/ports.UserRepository（identitymanage / sessionlogin 依赖）。
type FakeUserRepo struct { /* ... */ }
func NewFakeUserRepo() *FakeUserRepo
func (r *FakeUserRepo) SeedUser(u domain.User)
func (r *FakeUserRepo) Snapshot() []domain.User
func (r *FakeUserRepo) CallsOf(method string) []FakeCall

// FakeRoleRepo 实现 internal/ports.RoleRepository（identitymanage / rbacassign 依赖）。
type FakeRoleRepo struct { /* ... */ }
func NewFakeRoleRepo() *FakeRoleRepo
func (r *FakeRoleRepo) SeedAssignment(userID, roleID string)
func (r *FakeRoleRepo) Snapshot() map[string][]string                 // userID → roleIDs
func (r *FakeRoleRepo) CallsOf(method string) []FakeCall

// NewCredentialInvalidator 构造真 *credentialinvalidate.Invalidator 实例（不 Fake）。
// 默认依赖装配：clockmock.New(testAnchor) + DiscardHandler logger + Fake 子依赖（按实际签名 PR B day 1 lock）。
func NewCredentialInvalidator(t *testing.T, opts ...CredentialInvalidatorOption) *credentialinvalidate.Invalidator

// BuildIdentityManageService 构造 identitymanage.Service + 关联 FakeUserRepo + FakeRoleRepo + Recorder。
// 默认装配能让 identitymanage.Service.Create 端到端跑通（DemoCellTxManager + FakeUserRepo + 真 Invalidator + outboxtest.NewRecorder()）。
func BuildIdentityManageService(t *testing.T, opts ...BuildIdentityManageOption) (*identitymanage.Service, *FakeUserRepo, *FakeRoleRepo, *outboxtest.Recorder)
```

### 2.4 `cells/auditcore/auditcoretest/` 包设计

把 PR #588 的 `tests/integration/journey_auditlogintrail_helpers_test.go::buildAuditcoreChain` 从 inline test helper 提到公开包：

```go
// BuildAuditcoreChain 构造 docker-free 的 auditcore Cell + MemStore Ledger + 取出 auditappendsession handler。
// 默认装配：HMAC test key + MemStore + NoopEmitter + DemoCellTxManager + DiscardHandler logger + clock.Real。
// 返回值：
//   - handler: auditappendsession 的 outbox.EntryHandler，可直接调 handler(ctx, entry) 驱动消费
//   - store:   ledger.Store，可调 Tail / Verify 断言 hash chain 状态
//   - ctx:     context.Background()，调用方可自行 WithTimeout
func BuildAuditcoreChain(t *testing.T, opts ...BuildChainOption) (handler outbox.EntryHandler, store ledger.Store, ctx context.Context)

// CanonicalSessionCreatedEntry 构造一条 event.session.created.v1 entry，payload 符合 contract schema。
func CanonicalSessionCreatedEntry(sessionID, userID string) outbox.Entry
```

### 2.5 archtest 边界守卫（既有 TESTUTIL-BOUNDARY-01 已 cover）

> **修订记录（PR0）**：原计划新建 `CELLTEST-IMPORT-SCOPE-01` archtest，PR0 实施前勘察发现既有 `tools/archtest/testutil_boundary_test.go` 的 `TESTUTIL-BOUNDARY-01` 已 cover 同一下游 ban — 其 `isTestInfraPath` 自动发现含 `testutil` 段或长度 > 4 的 `*test` 段的路径，`cells/{X}/{X}test/` 自动落入发现集，production 文件 import 立即失败。新建规则纯属重复造轮子，撤销。

既有规则形态摘要（供 review 核对，源在 `tools/archtest/testutil_boundary_test.go`）：

- discovery-based：扫描 module 内全部 .go 文件，按路径段匹配自动聚合 testutil/test-infra 包集合
- 下游 ban：production .go 文件（非 `_test.go` 且非 test-infra 路径）import 该集合中任意包即 fail
- 上游评级 path-pattern Medium：archtest 自动发现 + 路径段匹配，不依赖 hand-crafted allowlist

**与 plan 044 §0 软处理批评的关系**：build tag 全栈迁移（`//go:build testutil` + archtest 守 header consistency）能把上游从 path-pattern Medium 升到编译期 Hard，但代价是 50+ 既有 testutil 包统一改造 + 所有 `go test ./...` → `-tags=testutil` + CI/IDE 配置同步。任务 1 scope 内不做该迁移；若未来真要升级，作为独立 plan 处理而非 backlog 软处理。

### 2.6 验证

```
go test ./kernel/outbox/outboxtest/... -run='^TestRecorder'    # PR0
go test ./cells/configcore/configcoretest/...                  # PR A
go test ./cells/accesscore/accesscoretest/...                  # PR B
go test ./cells/auditcore/auditcoretest/...                    # PR C
go test ./tools/archtest/ -run='^TestLayerTestutil$'           # 既有规则回归覆盖新增 *test/ 包
```

解锁的后续工作：
- J-useronboarding 3 manual criterion → auto（基于 `accesscoretest.BuildIdentityManageService`）
- J-confighotreload 重做 active（基于 `configcoretest.BuildWriteService` + `accesscoretest.FakeConfigGetter`）
- J-auditlogintrail `http.audit.list.v1` criterion 补全（基于 `auditcoretest.BuildAuditcoreChain` 扩 query handler）

---

## §3 任务 2 — Journey Business Semantic Governance

**目标**：`gocell validate` 拒绝把 pkg/framework invariant 包装成 journey criterion。

### 3.1 新文件 `kernel/governance/business_entities.go`

```go
package governance

// BusinessEntityWords 是 journey criterion text 必须含至少一个的词集合。
// 维护规则：新业务域引入新实体时同 PR 加入，每个词附 source 注释（哪个 contract / cell 引入）。
var BusinessEntityWords = []string{
    "user", "session", "config", "role", "policy", "device",
    "order", "audit", "event", "permission", "credential",
    "token", "subject", "actor",
}

// PkgFrameworkWords 是 journey criterion text 不允许独占的词集合（必须与 BusinessEntityWords 共存）。
// 命中这些词 + 不含 BusinessEntityWords 即 reject。
var PkgFrameworkWords = []string{
    "envelope", "handler", "middleware", "serialize", "marshal",
    "wire", "encode", "decode", "buffer", "redact",
}
```

### 3.2 新 governance rule `JOURNEY-BUSINESS-SEMANTIC-01`

加在 `kernel/governance/rules_journey.go`：

```go
// validateJOURNEYBusinessSemantic01 校验每条 journey passCriteria.text 含至少一个 BusinessEntityWords。
// 反面命中（仅含 PkgFrameworkWords）SeverityError 阻断。
// 大小写不敏感匹配；word boundary 用 \b regex。
func validateJOURNEYBusinessSemantic01(snapshot *MetadataSnapshot) []Diagnostic {
    var diags []Diagnostic
    for _, j := range snapshot.Journeys {
        for i, c := range j.PassCriteria {
            text := strings.ToLower(c.Text)
            hasBusiness := containsAnyWord(text, BusinessEntityWords)
            hasPkgFramework := containsAnyWord(text, PkgFrameworkWords)
            if !hasBusiness {
                diags = append(diags, Diagnostic{
                    RuleID:   "JOURNEY-BUSINESS-SEMANTIC-01",
                    Severity: SeverityError,
                    Path:     fmt.Sprintf("journeys/%s.yaml", j.ID),
                    Field:    fmt.Sprintf("passCriteria[%d].text", i),
                    Message:  "journey criterion text 不含业务实体词；可能错位为 framework invariant / pkg behavior。允许的业务实体词见 kernel/governance/business_entities.go。如真为业务场景，扩 BusinessEntityWords 白名单同 PR 落地",
                })
            } else if hasPkgFramework && !hasBusiness {
                // 不可达分支（!hasBusiness 已 return），保留作冗余防御
            }
        }
    }
    return diags
}
```

### 3.3 `journeys/journey.schema.json` 改动

加 `passCriteria.items.properties.text.description`：

```json
"text": {
  "type": "string",
  "description": "业务场景断言文本。必须含至少一个业务实体词（user / session / config / role / policy / device / order / audit / event / permission / credential / token / subject / actor），由 JOURNEY-BUSINESS-SEMANTIC-01 governance 校验。pkg/framework invariant 不应作为 journey criterion，归 archtest 或 pkg unit test。"
}
```

### 3.4 测试形态

`kernel/governance/rules_journey_test.go` 加 table-driven case：
- `business_word_present` — text = "user 登录后 session 持久化" → 0 diagnostic
- `business_word_absent` — text = "wire envelope 5xx strip details" → 1 diagnostic SeverityError
- `case_insensitive` — text = "USER 登录" → 0 diagnostic（大小写不敏感）
- `boundary` — text = "useragent 字段解析"（user 是子串但非 word）→ 1 diagnostic（word boundary 拒）

### 3.5 AI-Rebust 评级

- 下游 Hard：governance rule type-aware（不是字符串锚点，是 word list 解析 + 边界匹配）
- 上游 Medium：白名单维护是文档约束，archtest 守不到"新业务词必须加入白名单"。升级路径 = NLP 语义抽取（远期，需 LLM-in-CI 基建）

### 3.6 验证

```
go test ./kernel/governance/... -run='^TestValidateJourneyBusinessSemantic01$'
go run ./cmd/gocell validate --strict   # 全部现有 journey criterion 应通过
```

---

## §4 任务 3 — Response 5xx Redact Wire-path Archtest

**目标**：用 archtest 守"5xx wire path 必走 RedactSlogAttr"，替代被删 J-typed-envelope-roundtrip 中 `5xx-log-redact` criterion 的 wire-level 保护。pkg unit test 只保 `RedactSlogAttr` 函数本身正确，archtest 保 HTTP 5xx 路径真调它。

### 4.1 新文件 `tools/archtest/response_5xx_redact_funnel_test.go`

包级 godoc 写 `// INVARIANT: RESPONSE-5XX-REDACT-FUNNEL-01`。

### 4.2 规则形态（typed-aware）

扫描范围：
- `runtime/http/middleware/` 全部 production .go
- `pkg/httputil/` 全部 production .go

每个 CallExpr 满足条件 (a) AND (b) → 必须满足 (c)：
- (a) `Fun` 经 `*types.Info.Uses` 解析到 `log/slog` 包的 `Error` / `Warn` / `Info` / `Debug` 方法 OR `*slog.Logger` 同名方法
- (b) 参数列表中某 arg 经 `internal/typeseval.ResolveExpr` 解析为 `errcode.Error` 类型的字段访问 `*ast.SelectorExpr`，SelectorExpr.Sel.Name == "Details"
- (c) 该 Details 表达式必须先经 `pkg/redaction.RedactSlogAttr` 包装：扫上溯 30 行内的 `*ast.AssignStmt` 或 `*ast.CallExpr`，找形如 `redacted := redaction.RedactSlogAttr(err.Details)` 或直接 `redaction.RedactSlogAttr(err.Details)` 的调用，CallExpr.Fun 经 `*types.Info` 解析到 `pkg/redaction.RedactSlogAttr`

不满足 (c) → fail，diagnostic 指向违规 CallExpr 行号。

### 4.3 RED fixture

`tools/archtest/internal/response5xxredactfunnelfixture/`：
- `violator.go` — slog.Error 直接传 `err.Details` 未经 RedactSlogAttr，期望被规则识别
- `compliant.go` — `redacted := redaction.RedactSlogAttr(err.Details); slog.Error("...", "details", redacted)`，期望放过
- `unrelated_call.go` — slog.Info 不传 Details，期望放过（不在规则范围）

### 4.4 funnel 双向锁评级

- **下游 Hard**：typed CallExpr ban via `*types.Info` resolve — 形态唯一性（callee + arg type pair），无字符串锚点
- **上游 Medium**：`errcode.Error.Details` 字段是公开导出的（`[]slog.Attr`），包外可读 — 任何外部代码都能拿到 Details 引用 → archtest scope 只覆盖 runtime/http/middleware + pkg/httputil 两个目录，cells/* 中如有 slog 写 Details 不在 scope。升级路径 = `Details` 字段降为 unexported + 加 funnel accessor method（由 errcode 维护者主导）

### 4.5 验证

```
go test ./tools/archtest/ -run='^TestResponse5xxRedactFunnel01$'
```

---

## §5 任务 4 — Journey Criterion Semantic Pattern Matrix

**目标**：把"criterion 语义形态 → 测试 seam 选择"映射从隐式 tribal knowledge 转为机器可校验的 yaml schema 字段。

### 5.1 修改 `.claude/rules/gocell/cell-patterns.md`

在文件末尾新增章节 `## Journey Criterion Semantic Form → Seam 选择`：

```markdown
## Journey Criterion Semantic Form → Seam 选择

每个 `mode: auto` criterion 必须在 yaml 中声明 `semanticForm`，对应一种已 ship 测试范本：

| semanticForm | 适用形态 | 测试 seam | Ship 范本 |
|--------------|---------|----------|----------|
| `consumer-side` | cell 消费 event，断言 handler Ack / Reject + 内部状态变化 | `cell.NewXxxCell().Init(reg)` + `reg.Snapshot().Subscriptions[topic].Handler` 取出后直接调 | PR #588 J-auditlogintrail event-consume |
| `persistence-seam` | 通过公开 Store / Repository interface 验证持久化 round-trip | runtime/auth/session.Store 类公开接口的 `*test` 包构造 | PR #572 J-useronboarding login-verify |
| `producer-side` | 业务 service 触发 → 断言事件被 emit 到 outbox | `cells/{X}/{X}test.BuildWriteService` 拿 `*Service + *OutboxRecorder` | 待 plan 045 PLATFORM-CELL-TESTUTIL ship |
| `state-apply` | cell 消费 event 后通过 dependency（如 ConfigGetter）回源应用 | `cells/{X}/{X}test.Fake{Dep}` 注入 + spy calls 断言 | 待 plan 045 |
| `multi-cell` | 跨多 cell 协同（如 health probe 同时绿） | `tests/testutil/corebundle/` 共享 harness（触发型，未 ship） | （未 ship） |

`semanticForm: framework-invariant` 不允许在 journey 中出现 — `JOURNEY-BUSINESS-SEMANTIC-01` governance rule 拦截，引导转 archtest 或 pkg unit test。
```

### 5.2 `journeys/journey.schema.json` 加 `semanticForm` 字段

```json
"passCriteria": {
  "type": "array",
  "items": {
    "type": "object",
    "properties": {
      "text": { ... },
      "mode": { "enum": ["auto", "manual"] },
      "checkRef": { "type": "string" },
      "semanticForm": {
        "type": "string",
        "enum": ["consumer-side", "persistence-seam", "producer-side", "state-apply", "multi-cell"],
        "description": "criterion 语义形态，决定测试 seam 选择。见 .claude/rules/gocell/cell-patterns.md §Journey Criterion Semantic Form → Seam 选择。mode:auto 必填；mode:manual 可选（manual 时若声明，governance 校验同源 seam 在 testutil 包 ship 后立即应解锁）"
      }
    },
    "if": { "properties": { "mode": { "const": "auto" } } },
    "then": { "required": ["text", "mode", "checkRef", "semanticForm"] }
  }
}
```

### 5.3 新 governance rule `JOURNEY-SEMANTIC-FORM-SEAM-CONSISTENCY-01`

加在 `kernel/governance/rules_journey.go`：

```go
// validateJOURNEYSemanticFormSeamConsistency01 校验 semanticForm 与 checkRef 解析的测试文件路径模式一致。
// 规则：
//   - consumer-side / persistence-seam: 测试文件可在 tests/integration/，无额外约束
//   - producer-side / state-apply: 测试文件必须 import cells/{X}/{X}test 之一，否则 SeverityError
//   - multi-cell: 测试文件必须 import tests/testutil/corebundle，否则 SeverityWarning（harness 未 ship 前为 warning）
func validateJOURNEYSemanticFormSeamConsistency01(snapshot *MetadataSnapshot) []Diagnostic
```

实现细节：
- 取 criterion.checkRef，按 `verify.kebabToCamelCase` 解析为 `TestJ{...}{...}` 测试函数名
- 走 `internal/typeseval` 加载 tests/integration 包，找该测试函数所在 .go 文件
- 解析该文件 import 声明，按 semanticForm 校验

### 5.4 验证

```
go test ./kernel/governance/... -run='^TestValidateJourneySemanticFormSeamConsistency01$'
go run ./cmd/gocell validate --strict   # 全部 active journey 现有 criterion 应通过
```

---

## §6 任务 5 — Review Criterion Semantic Alignment Check

**目标**：阻止 PR #590 类"测试方向错"finding 漏检。

### 6.1 修改 `.claude/skills/ship/SKILL.md`

在 `## 阶段 7：Review` 章节末尾追加：

```markdown
### Journey criterion 测试 PR 的强制对齐检查

每个含 `journeys/J-*.yaml` 改动 或 `tests/integration/journey_*_test.go` 改动的 PR，reviewer prompt 必须包含以下步骤：

1. **逐条列出本 PR 涉及的所有 mode:auto criterion**（含 yaml text 主谓宾 + checkRef 测试函数名 + 测试文件路径）
2. **对每条 criterion，先按 text 标出主语 / 谓语 / 业务实体**（不是 framework 词汇）
3. **对照测试代码路径**，确认测的就是该主谓宾，**不是相邻 / 反向 / 下游 invariant**
4. **反例自问**：如果生产代码删除"实现主谓宾的关键调用"（如删 producer 的 emit / 删 state-apply 的 getter call），当前测试还会 PASS 吗？若 PASS → criterion 测错方向，Cx1 finding 阻断

测试 PASS + lint 0 + VERIFY-06 绿三连绿**不**构成达标判断 — 必须显式过这 4 步对齐才算 review 通过。
```

### 6.2 修改 `.claude/rules/gocell/ai-collab.md`

在 `## Review checklist` 段末尾追加：

```markdown
### Journey criterion 测试 PR 强制对齐表

涉及 `journeys/J-*.yaml` 或 `tests/integration/journey_*_test.go` 改动的 PR review 必须含显式对齐表：

| criterion | text 主谓宾 | 测试函数 | 测试驱动的代码路径 | 反例自问（删什么会让测试仍 PASS） | 对齐? |
|-----------|------------|---------|-----------------|-------------------------------|------|
| ... | ... | ... | ... | ... | ✓ / ✗ |

任一行 ✗ → Cx1 finding 阻断。该表是 reviewer agent 输出的强制字段，不是可选附录。

### Journey 体系定位错位拦截

reviewer 必须自问：本 PR 立项 / 修改的 journey criterion，主谓宾是否含业务实体词？若全部主谓宾都是 pkg / framework 词汇（envelope / archtest / serialize / wire 等），**reject 该 PR**，引导转 archtest 或 pkg unit test。范例：J-typed-envelope-roundtrip（PR #591 关闭）。
```

### 6.3 验证

无自动化验证 — 下次 journey 类 PR ship 时观察 reviewer 输出是否含强制对齐表。可选 archtest（远期）：grep `tools/reviewer/` 内 prompt 模板是否含 "criterion text 主谓宾" 关键字。

---

## §7 任务 6 — Backlog Soft-processing Escalation Feedback

**目标**：阻止"同源根因重复登记 backlog"的软处理累积。

### 7.1 修改 `.claude/rules/gocell/ai-collab.md`

在 `## Review checklist` 段末尾追加：

```markdown
### Backlog 同模板升格规则

某 backlog ID 模板（同 trigger 描述形态 + Files 路径集合重叠 ≥ 50%）出现 ≥ 2 个并行实例时，**禁止再加第三条同模板 backlog**，必须升格为 framework 立项 (P0/P1)。

判定方法：
- trigger 文本编辑距离 ≤ Levenshtein(min) × 0.3（即 ≥70% 相似）
- Files 字段路径 set Jaccard 相似度 ≥ 50%

ship 实例：plan 044 — J-useronboarding (PR #572) + J-confighotreload (PR #590) + J-typed-envelope-roundtrip (PR #591) 三次同源 backlog 软处理触发 PLATFORM-CELL-TESTUTIL-PACKAGES-01 framework 立项。
```

### 7.2 新 governance rule `BACKLOG-SOFT-PROCESSING-CLUSTER-WARNING-01`

加在 `kernel/governance/rules_misc_advisory.go`（advisory 级，不阻断 CI）：

```go
// validateBACKLOGSoftProcessingCluster01 扫 docs/backlog/cap-*.md 全部条目，
// 对 Flag=🟠 / 🟡 的条目按 (trigger 文本编辑距离 + Files 路径 Jaccard 重叠) 聚类，
// 簇大小 ≥ 2 时 emit SeverityWarning 提示人审是否升格 framework 立项。
//
// 算法：
//   1. parseBacklogTable 解析 cap-*.md 表格 → []BacklogEntry{ID, Description, Trigger, Files, Flag}
//   2. 按 Flag=🟠/🟡 过滤
//   3. 对每对 (a, b)：similarity = 0.5 * normalizedLevenshtein(a.Trigger, b.Trigger) + 0.5 * jaccardPathSet(a.Files, b.Files)
//   4. similarity ≥ 0.7 → 同簇
//   5. 簇大小 ≥ 2 → emit Warning，含两个条目 ID 列表 + 建议"考虑升格 framework 立项"
//
// 实现细节：
//   - normalizedLevenshtein: editDistance / max(len(a), len(b))，取 1 - distance
//   - jaccardPathSet: 路径分隔符 split 后 set 交集 / 并集
func validateBACKLOGSoftProcessingCluster01(snapshot *MetadataSnapshot) []Diagnostic
```

### 7.3 metadata 解析支持

需要 `kernel/metadata` 支持 backlog 表格解析（当前可能没有）。如果没有，新增 `kernel/metadata/backlog.go`：

```go
type BacklogEntry struct {
    ID          string
    Description string
    Type        string
    PriorityCx  string
    Flag        string  // 🔴 / 🟠 / 🟡 / 🟢 / ✅
    Trigger     string
    Files       []string  // 按 ` + ` 分隔解析
    Source      string
    SourceFile  string  // cap-XX-*.md 文件路径
    Line        int     // 行号
}

func ParseBacklogTable(path string) ([]BacklogEntry, error)
func ParseAllBacklog(rootDir string) ([]BacklogEntry, error)  // 扫 docs/backlog/cap-*.md
```

### 7.4 验证

```
go test ./kernel/governance/... -run='^TestValidateBacklogSoftProcessingCluster01$'
go run ./cmd/gocell validate --strict   # 当前 backlog 状态下应有若干 advisory warning（含 J-useronboarding / J-accountlockout LAZYUNLOCK 等已有 🟠 / 🟢 条目聚类）
```

期望 warning 形式：
```
[BACKLOG-SOFT-PROCESSING-CLUSTER-WARNING-01] cluster of 2+ similar 🟠/🟡 backlog entries detected:
  - JOURNEY-USERONBOARDING-AUTO-EXPANSION-01 (cap-14-tooling.md:95)
  - JOURNEY-ACCOUNTLOCKOUT-AUTO-EXPANSION-01 (cap-14-tooling.md:96)
similarity=0.78. Consider framework escalation. See ai-collab.md §Backlog 同模板升格规则.
```

---

## §8 任务间依赖

```
任务 1 (PLATFORM-CELL-TESTUTIL)        ─┬─→  任务 4 (SEMANTIC-PATTERN-MATRIX)  ─→  任务 2 (BUSINESS-SEMANTIC-GOVERNANCE)
                                         │     （semanticForm 字段先有 ship 范本，再加 governance）
                                         │
                                         └─→  解锁 J-useronboarding / J-confighotreload 重做 / J-auditlogintrail (a)

任务 5 (REVIEW-ALIGNMENT-CHECK)         独立可做（无依赖）

任务 6 (BACKLOG-CLUSTER-WARNING)        独立可做（需 kernel/metadata backlog 解析，无外部依赖）

任务 3 (RESPONSE-5XX-REDACT-ARCHTEST)   独立可做（无依赖；删 J-typed-envelope-roundtrip 后的 wire-level 不变量替代）
```

推荐 ship 顺序：
1. 任务 5 + 任务 6 + 任务 3 并行（小、独立）
2. 任务 1（大，单独排期）
3. 任务 4 → 任务 2（依赖任务 1 ship 后有 ship 范本）
