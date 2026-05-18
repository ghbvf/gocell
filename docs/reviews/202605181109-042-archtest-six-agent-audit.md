# 202605181109-042 archtest 六-agent 全量审计报告

> 范围：`tools/archtest/` 全量 139 个 `*_test.go`、~200 个 INVARIANT、façade 层
> （pass/scope/walk/resolve/content/fixture/golden）+ `internal/scanner` +
> `internal/typeseval`。方法：6 个并行子 agent 按域切分，每域统一四维分析
> （能力清单+AI-rebust 评级 / 合并候选 / Soft 升级路径+开源对标 / 暴露面收缩），
> 本文为交叉综合。纯分析，未改动任何 enforcement 代码。
>
> 评级口径见 `.claude/rules/gocell/ai-collab.md`（Hard / Medium / Soft；
> Soft 严禁新立项，既有 Soft 不得 silent carryover）。

## 0. 全景结论

- 规模：139 文件 / ~200 INVARIANT / ~54k LOC。
- 评级分布：**绝大多数 Medium**（AST + `*types.Info`），**少数 Hard**
  （私有字段 / sealed interface / codegen funnel / compile-time），
  **~30 条仍 Soft**（字符串锚点 / 注释豁免 / 名字约定 / 路径 allowlist）。
- 系统性结构债三类，优先级高于任何单条规则强化：
  - **(a) 半闭环 funnel**：下游 Hard、上游 Soft（mutation 路径无封口 token）。
  - **(b) 碎片化**：sub-rule 后缀爆炸 + 同主题文件未按命名规则归并。
  - **(c) 重复造轮子**：命令式 boundary 测试在重实现 depguard/go-arch-lint。

## 1. 能力地图（域 × 评级）

| 域 | Hard 代表 | Medium 主体 | Soft 残留 |
|---|---|---|---|
| Infra/Façade | PASS-FUNNEL-FIXTURE-TAG、TYPESEVAL-EVAL-PREDICATE、TYPESUTIL-IMPLEMENTS-FUNNEL | PASS-FUNNEL-EACHFILE/LOADPACKAGES/RESOLVE、SCANNER-FRAMEWORK-USAGE | sealedMarkerFiles 列表、`VISIT_BUFFER_*` env skip、golden 仅重生、PACKAGES-IMPORT 字面量、PRODUCTION-LOADER 跨函数逃逸 |
| errcode/可观测 | PANIC-REGISTERED-01、PROM-CELL-LABEL-FUNNEL-01、CELL-REPO-READYZ(下游)、HEALTH-REDACTED-ERROR-MSG-FUNNEL(上游) | ERRCODE-KIND-LITERAL、MESSAGE-CONST-LITERAL、DETAILS-SLOG-ATTR、EXPORTED-ERROR-NEW、WIRE-CODE-5XX | PANIC-REDACT-01、SPAN-RECORD-ERROR-REDACT-01/-ARCHTEST、REPO-LOG-KEY-ID-REDACT-01、HTTPUTIL-5XX-(KIND/LOG/SURFACE)、OBS-01、READYZ-PROBE-NAMING-01、HTTP-METRICS-LABEL ×5、ERROR-FIRST-API-01、HEALTH-VERBOSE-SCAN-COVERAGE |
| Cell/codegen/contract | NO-MANUAL-CONTRACTSPEC(字面量)、MARKERGEN-DRIFT-VERIFY、SCAFFOLD-DERIVED-FORCEOVERWRITE、CELL-IFACE-ISP-METHODSETS、CONTRACTSPEC-FRAMEWORK-BUILDERS-EXIST | codegen/assembly/scaffold/contract.yaml 全族 | CONTRACTTEST-BOUNDARY-01、CODEGEN-CELL-GEN-02 头注释 |
| auth/session/authz | CREDENTIAL-AUTHORITY-ASSERT-FUNNEL、JWT-CLAIMS-NO-AUTHZ-EPOCH、DOMAIN-AUTHZ-FIELD-PRIVATE、CACHING-SESSION-REVOKE-DELEGATE-ONLY、AUTH-BOOTSTRAP-CLIENTS-MUTEX、SEED-ROLE-IFACE | credential-invalidate 三 funnel(下游)、sessionvalidate/sessionrefresh epoch 族、SESSION-PROTOCOL-COMPOSITION-ROOT | NO-DELETED-AUTH-SYMBOLS-01、AUTH-PLAN-01/02/03、ROLE-ADMIN-LITERAL-01、PROVISION-STATE-REMOVED、CELLS-NO-ROUTEMUX-WRAPPER、若干 SEC-FAIL-CLOSED |
| adapters/eventbus/clock | REDIS-KEY-NAMESPACE(参数类型)、ADAPTER-ERROR-CLASSIFICATION-TRANSIENT、OUTBOX-HANDLERESULT-NO-RECEIPT、REGISTRY-SUBSCRIBE-CELLID-POSITIONAL | RMQ/OUTBOX/migration/goose/redis 全族 | 无纯 Soft；clock 注入为 comment-marker（自承"曾 Soft"），SQL 类规则 Medium 即天花板 |
| governance/CI/boundary | CLI-UNIMPL-HIDE-01、AUTH-BOOTSTRAP-CLIENTS-MUTEX-01 | KERNEL-INTERNAL-DAG、CELL-IFACE-ISP、5×*_boundary、KERNEL-MUSTCTOR、TESTUTIL-BOUNDARY | GOVERNANCE-RULE-ERROR-MESSAGE-FIX-SUFFIX、CI-PINNING、CI-INTEGRATION-DISCOVERY、SLOWGATE-ALLOWLIST、BOOTSTRAP-PATH-PREDICATE-SOLE、LISTENER-DX-01、ACCESSCORE-FACADE-A61-01 |

## 2. 合并候选（跨域去重）

### 2a. sub-rule 后缀塌缩（~200 → ~150 ID，零覆盖损失）

下列 `-A/-B/-C/-D` 与 `01..05` 是同一不变量的**测试分区**，应收为单 ID + 子 `t.Run`：

- `RMQ-CHANNEL-MAX-PER-CONN-01-A/B/C`、`RMQ-PUBLISHER-FAILURE-HANDLING-01-A/B/C/D`、
  `RMQ-STOPINTAKE-INFLIGHT-WAIT-01-A/B`（`rmq_invariants_test.go`，12→3）
- `OUTBOX-SERVICE-01..05`（`outbox_invariants_test.go`）
- `HTTP-METRICS-LABEL-*` ×5（`http_metrics_label_test.go`，5→1）
- `SESSIONVALIDATE-EPOCH-COMPARE-01` + `-SOURCE-01`（同一 `!=` 的算子 vs 操作数）
- 范例（已正确合并，勿动）：`CELLMETA-SINGLE-SOURCE-01/02/03`

### 2b. 同主题文件归并（违反 ai-collab "≥3 同主题 → `{theme}_invariants_test.go`"）

| 新主题文件 | 并入 | 收益 |
|---|---|---|
| `testonly_import_boundary_invariants_test.go` | auth_authtest / auth_keystest / celltest / contracttest / testutil(+negprobe) | **最大单笔**：5 文件 → 1 张表 `{importPath, testInfraPredicate, deletedSymbols, exemptLayers}` + 1 个共享 `parseImports`；negprobe 随共享 classifier 自动扩到全部五条，覆盖反增 |
| `redaction_funnel_invariants_test.go` | PANIC-REDACT / SPAN-RECORD / REPO-LOG-KEY / HTTPUTIL-5XX-LOG | 消除 4 处各自重推 redaction import alias 的手写匹配器，统一 `pkg/redaction` types.Info resolver |
| `contractspec_funnel_invariants_test.go` | NO-MANUAL-CONTRACTSPEC + CELLS-NO-CONTRACTSPEC-IMPORT(allowlist 已空，可删) + BASESLICE-CTOR-FUNNEL | 同一"仅经 funnel 构造框架类型"形态 |
| `scaffold_invariants_test.go` | SCAFFOLD-WRITE-FUNNEL + DERIVED-FORCEOVERWRITE + BUNDLE ×3 | 命名规则强制 |
| `cell_codegen_invariants_test.go` | CODEGEN-CELL-GEN-01..04 + USER-FILE-OVERLAP + INIT-INTERNAL + NO-METADATA-LITERAL + MARKER-WIRE | 共享 `mustParseProject` 去冗余 ProjectMeta walk |
| `ci_meta_invariants_test.go` | CI-PINNING + CI-INTEGRATION-DISCOVERY + SLOWGATE + BUILD-CONSTRAINT | CI-meta Soft 集群隔离 |

补充：`PROD-DURATION-CONST-01` 与 `TEST-TIME-LITERAL-01` 显式互为 prod/test 分身，
`TEST-SLEEP-DISCIPLINE-01` 是其子集 → 合 `DURATION-CONST-DISCIPLINE-01`，sleep 降为子检。

### 2c. 命令式 boundary → 声明式配置（等效 Hard，维护量骤降）

- `cell_init` import-ban 半部、`corebundle_deps` → `.golangci.yml` depguard
  scoped 规则（已有 kernel/runtime/adapters-isolation 覆盖同语义，CI lint 同样 merge-blocking）。
- 五条 test-only boundary → depguard `files:` 反选，或引入
  `fe3dback/go-arch-lint` 的 `.go-arch-lint.yml` component 矩阵（per-cell
  LAYER-05/06 + test-only 一处声明）。
- **保留 Go archtest**（声明式不可表达）：KERNEL-INTERNAL-DAG（depgraph 传递闭包）、
  CELL-IFACE-ISP（类型形态）、CLI-UNIMPL-HIDE、AUTH-BOOTSTRAP-CLIENTS-MUTEX
  （composite-literal 字段关联）、CELL-ID-PATTERN-SINGLE-SOURCE（typed const-eval）。

净效果：**~139 → ~120 文件，~200 → ~150 ID，去除 4–5 份重复 import-walk 逻辑**。

## 3. Soft 清单 + 升级路径 + 开源对标

### 3a. 合规红线（P0，零代码改动）

以下 Soft 缺少 ai-collab 强制的 backlog 升级条目，构成 "silent carryover" 违章，
**必须先登记或显式 accept**：

- `LISTENER-DX-01`、`ACCESSCORE-FACADE-A61-01`（deletion-guard，godoc 无 backlog 引用）
- `NO-DELETED-AUTH-SYMBOLS-01`（AST `sel.X.Name=="auth"` 非类型解析，无 backlog）
- **credential-invalidate 三 funnel 的未封口上游**：`session.Store.RevokeForSubject` /
  `refresh.Store.RevokeUser` / `UserRepository.BumpAuthzEpoch` 下游 Hard、
  上游**完全无锁**且无配对 backlog —— 这是整个吊销安全模型唯一结构性开口。

### 3b. Soft→Hard 通用范式（跨域复用 + 开源对标）

| Soft 类别 | 升级范式 | 开源对标 |
|---|---|---|
| call-site redaction（PANIC/SPAN/REPO-LOG/HTTPUTIL ×6） | 收口到 **sink 侧**：`obs.SafeSpan.RecordError` 内部脱敏 + slog handler middleware，call site 无法传原始 error | **HashiCorp Vault** audit broker + `log_raw=false` 默认（raw 值结构上到不了 formatter） |
| probe 名 / 字符串约定（READYZ-PROBE-NAMING、ROLE-ADMIN-LITERAL、GOVERNANCE-FIX-SUFFIX） | **string-typed concept funnel**：`type ProbeName/Role/DiagFix string` + 构造期 Validate；`reg.Health` 只收 `ProbeName` | Go capability idiom（`context` 未导出 key、`crypto/tls`）；ArchUnit 同类天花板=Medium |
| 生成文件头注释识别（CODEGEN-CELL-GEN-02、scaffold marker、HTTPUTIL-SURFACE doc 表） | codegen 发 **typed marker 符号** + golden-byte diff，CI `--verify-only` | **Kubernetes code-generator**：`// +k8s:` tag 单源 + 重生成 + `git diff` 失败即红 |
| clock 注入（comment-marker） | sealed `clock.Clock` 接口 + `clock.System()` 唯一 prod 构造 + depguard 全包禁 `time.Now/Sleep/NewTimer` | **benbjohnson/clock** + **uber fx** DI hook |
| caller path-prefix allowlist（半闭环 funnel） | sealed unexported **token 参数**，仅 funnel 包可构造 → 包外字面量不可表达 | **Vault** audit broker（路径只持 broker）；Go `crypto/tls.Config` |
| HandleResult 字面量 allowlist | 字段全 unexport，只留 `Ack/Requeue/Reject` 方法 | **Watermill** `message.Ack()/Nack()` on opaque Message |
| YAML/CI 正则（CI-PINNING/INTEGRATION/SLOWGATE） | YAML 无类型系统，Medium 即天花板；SHA-pin 外包 `zizmor`/`pinact` | OSS 同类（`squawk` for SQL、`zizmor` for actions）即业界 SOTA |
| 命令式 layer ban | 声明式 depguard / go-arch-lint，lint gate 同样 merge-blocking | **depguard / fe3dback/go-arch-lint / fdaines/arch-go / ArchUnit `layeredArchitecture()`** |

> 网络受限，开源对标基于 ADR 内 `ref:` 引用 + 训练知识（best-effort，已声明）。
> 语义上 GoCell 的 `*types.Info` funnel 已**强于** semgrep `pattern-not-inside` /
> ArchUnit runtime-graph（二者天花板=Medium）；唯一差距是未封口上游，
> 仅 Go capability/sealed-token 可补。

### 3c. 覆盖缺口

`api-versioning.md` 的 v1→v2 breaking 规则**无 archtest 强制**。对标
`buf breaking`（基线镜像 + 机检 breaking）应补一条 contract baseline gate。

## 4. 暴露面收缩 / 统一接口（按 ROI 排序）

| # | 收口动作 | 消灭 / 降级的规则 | 类别 |
|---|---|---|---|
| 1 | `reg.Health(string,fn)` → `RegisterReadiness(ProbeName, Prober)` | READYZ-PROBE-NAMING(Soft) 变编译错误；吸收 CELL-REPO-READYZ N1/N2 duck-type | 可观测最高 ROI |
| 2 | credential-invalidate **sealed fenceToken**：三 mutation 方法收未导出 token，仅 `Invalidator` 可造 | 三 funnel 上游 Soft→闭环 Hard；堵住吊销模型唯一结构开口 | 安全最高 ROI |
| 3 | `errcode.WithDetails/WithInternal` → sealed `PublicDetail/InternalDetail` newtype | DETAILS-SLOG-ATTR + MESSAGE-CONST 两 Medium → 一 Hard 类型 | 错误模型 |
| 4 | `outbox.HandleResult` 字段全 unexport（kernel 内部 builder 分离） | OUTBOX-HANDLERESULT-FACTORY-PREFERRED + FIELDS-FROZEN 退役 | 事件总线 |
| 5 | sink 侧脱敏（SafeSpan + slog middleware） | 删 4 条 call-site redaction Soft archtest | 可观测 |
| 6 | façade 收缩：删 `FileContext` alias、unexport `LoadContentFiles`、3 个 build-constraint 自由函数并入 `Pass`、5 个 `Run*` 入口统一为带 scope 选项的单 `Run`（顺带闭 PASS-PRODUCTION 上游 Soft） | resolve.go 导出 8→5；façade 误用面缩小 | 框架自身 |
| 7 | typed `Clock` funnel + depguard 禁 `time.Now` | clock 注入 comment-marker → Hard | 适配器 |
| 8 | `identitymanage.NewService` 把 lastAdminGuard 改必填参数 | IDENTITYMANAGE-LAST-ADMIN(Medium) → 编译错误，archtest 整条删除 | 安全 |
| 9 | `contractspec` framework 上游：未导出 authority token 替代 path-string allowlist | NewFrameworkHTTP 上游 Soft→Hard | 契约 |

## 5. 落地顺序

1. **P0 合规（零改码）**：为 §3a 四组 silent-carryover Soft 登记 backlog 升级条目，
   或在 godoc 显式 accept（ai-collab Review checklist 硬性要求，当前违章）。
2. **P1 高 ROI funnel（可并行）**：§4 #1 ProbeName、#2 fenceToken、#3 errcode sealed
   details —— 各为"一次类型收口消灭多条 Medium/Soft"。
3. **P2 整合（机械、低风险）**：§2a sub-rule 塌缩 + §2b theme 归并 + §2c 声明式迁移。
4. **P3 开源范式吸收**：sink 脱敏、verify-only golden、typed clock、buf 式 contract
   breaking gate（补 §3c 缺口）。

## 附：方法与可信度

- 6 agent 域切分：(1) Infra/Façade+meta、(2) errcode/observability、
  (3) cell/codegen/contract、(4) auth/session/authz、(5) adapters/eventbus/clock、
  (6) governance/CI/boundary。
- 每域产出统一四维分析，本文为人工交叉综合（去重 + 优先级排序）。
- 局限：开源对标 best-effort（网络受限，依赖 ADR `ref:` + 训练知识，已声明）；
  agent 摘要描述意图，落地前需逐条复核源码。
