# PR #450 — Kernel Guardian 审查

- 分支：`refactor/554-pg-s7-audit-ledger` → `develop`
- 范围：90 文件 / +6267 / −3587
- 核心改造：`runtime/audit/ledger` typed Protocol + Store interface；`adapters/postgres.LedgerStore` + migration 020/021；`cells/auditcore` 重构成 framework 消费者；`pkg/redaction` 单源；4 个 auditappend* slice 拆分 + 共享 `internal/appender`；新增 2 archtest。

## 总体判断

**合规 — 可合并，附 P2 跟进建议**。

证据：
- `go build ./...` 通过；`go test ./tools/archtest/` 通过（含 2 个新规则）；`go run ./cmd/gocell validate` 报 `0 error(s), 0 warning(s)`。
- 分层依赖方向全部正确（kernel 无外侵；runtime/audit/ledger 只依赖 kernel + pkg；adapters/postgres 实现 runtime 接口；cells/auditcore 不 import adapters）。
- HMAC raw key 流向收敛在 `cmd/corebundle/audit_module.go` 与 `examples/ssobff/app.go` composition root；cells/auditcore 生产代码无 `WithChainHMAC` / `WithHMACKey` 引用。
- 元数据闭环：4 个新 slice.yaml + cell.yaml + audit.appended.v1 contract 全套通过 `gocell validate`（含 ADV-05 / ADV-06 / VERIFY-01）。
- 新增 archtest 拦截能力实际有效（命中测试构造合规所有路径，调用点仅出现在豁免位置）。

未发现 P0/P1 阻塞项。下列 P2 是「治理强度可再提升」的非阻塞建议，建议在 backlog 登记后续追跟。

## Findings

| ID | 严重度 | Cx | 文件:行 | 问题 | 建议 |
|----|--------|----|---------|------|------|
| K-01 | P2 | Cx2 | `tools/archtest/audit_ledger_composition_root_test.go:75-78` | archtest 仅匹配 selector 包名 `ledger`。若违规方使用 import alias（`auditledger "…/runtime/audit/ledger"; auditledger.NewProtocol(...)`），AST 中 `pkg.Name` 为 `auditledger`，规则会漏判。这是该 archtest 的 Soft 残面：约定靠 import 别名而非 type identity。 | 在 `EachFile` 内先解析 `fc.File.Imports`，构建「import path == runtime/audit/ledger → local name set」的映射，再用此 set 替换 `pkg.Name != "ledger"` 检查。同等 type-aware 改造可提到 Hard。或在 `internal/appender/doc.go` / archtest godoc 显式声明「不准 alias 此包」并补 `IMPORT-ALIAS-FORBIDDEN` 锚点测试。 |
| K-02 | P2 | Cx2 | `cells/auditcore/internal/appender/service.go:40` | `appender.WithTxManager` 形参为 raw `persistence.TxRunner`（cells 子包内部），不是 sealed `persistence.CellTxManager`。当前调用链 cell.go → appender 是合法的（因 cell-patterns Sealed Marker Wrap 仅约束 cell-level 公开 Option，且 `CellTxManager` 嵌入 `TxRunner` 可隐式满足），但 internal 包看到 raw TxRunner 让「cells 不见 raw infra」的不变量在子包边界出现裂缝——只是被「internal 包不导出」的物理屏障兜底。 | 把 `appender.WithTxManager` 形参改为 `persistence.CellTxManager` 并让 Service 持 `CellTxManager`，与 cell.go 字段类型完全同型；compose 调用不变（cell.go 已持 sealed）。这是 AI-rebust 一致性：raw infra 类型整条 cell 子树都看不到，未来重构无须依赖 `internal` 物理边界。 |
| K-03 | P2 | Cx1 | `cells/auditcore/slices/auditquery/service.go:13`（未变更，但路径相关） | 未触发；记录确认点。`auditquery` slice 通过 runtime/audit/ledger.Store 接口读 ledger，无 adapters 直接依赖；redaction 仅在出站 HTTP 路径生效（handler.go:137），落库保留原始 payload，符合 observability.md §Audit Payload Redaction。 | 无修复项。建议在 ADR `202605101800-adr-audit-ledger-protocol.md` 增加一节明确「读侧 ledger.Store 接口契约 + 出站 redaction」对应关系，便于后续 cell 复用 ledger framework 时 mirror 该约束。 |
| K-04 | P2 | Cx2 | `runtime/audit/ledger/storetest/suite.go:11-13, 60-73` | `storetest` 子包内 `NewMemStoreForSuite` 调用 `ledger.NewProtocol`，archtest 已通过「runtime/audit/ledger/ 前缀豁免」豁免；但 godoc 仅口头说明，未在 archtest allowlist 中列出独立 anchor，等于把豁免规则与 archtest 实现绑死。新增 ledger sub-package（如 `runtime/audit/ledger/dump`）会被默默放行。 | 把 `audit_ledger_composition_root_test.go:53` 的 prefix allowlist 显式列为常量 + godoc 注释枚举允许的 sub-package；或更激进做法：豁免精确到 `runtime/audit/ledger/` + `runtime/audit/ledger/storetest/`，新建子目录默认 fail。 |
| K-05 | P2 | Cx1 | `adapters/postgres/migrations/020_audit_ledger.sql` 与 `021_audit_entries_event_id_unique.sql` | 020 与 021 顺序依赖隐含在文件序号；021 的 ref 注释指向 `selectFingerprintSQL` 应用层 fingerprint，但「021 必须随 020 一起上 production」这条 deploy invariant 仅在 020 SQL 注释里写「audit_entries is a new table introduced in migration 020 and deployed together with this migration」自然语言陈述。 | 工具化校验目前无对应 archtest / governance rule。建议在 `tools/archtest/` 增加 `MIGRATION-PAIR-DEPLOY-01` 类规则（Medium），扫描 migration 文件 godoc 中的 deploy-pair 锚点；或在 ADR 中固化 deploy-pair 约束。属于通用治理，不必在本 PR 内完成。 |
| K-06 | P2 | Cx1 | `cells/auditcore/cell.yaml:9` | `schema.primary: cell_audit_core` 是逻辑 schema 名，不对应实际 PG 命名空间（migration 020 的 `audit_entries` 表无 schema 前缀；命名空间由 `ledger.Protocol.Namespace()` runtime 标识）。FMT 校验只看字段存在性，已通过 validate；但「schema.primary」与「ledger namespace」的对应关系无显式映射文档。 | ADR 里增加一节「cell.yaml schema.primary ↔ ledger NamespaceID ↔ PG audit_entries.namespace 的三方等价关系」，避免下一次重构时被误认为是 dead field。 |

## AI-rebust 评级核对表

| Archtest INVARIANT ID | 文件 | 类型 | 拦截手段 | 评级 | 评级理由 |
|-----------------------|------|------|---------|------|----------|
| AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01 | `tools/archtest/audit_ledger_composition_root_test.go` | archtest type-aware AST scan | scanner.EachFile + EachNode[ast.CallExpr]，匹配 SelectorExpr.pkg.Name == "ledger" + forbidden name | **Medium** | 配对 typed Protocol（type-system Hard 防线：`*ledger.Protocol` 在 cells/runtime/adapters 必须 inject）。Soft 残面：依赖 import 包名而非 path identity（K-01）。godoc 已注明 AI-rebust 等级 + 不能 Hard 的理由（不能从 cells 移除 import）。符合 AI-collab 章程「≥ Medium 立项硬门槛」。 |
| AUDITCORE-APPENDER-SINGLE-SOURCE-01 | `tools/archtest/auditcore_appender_single_source_test.go` | archtest type-aware AST scan | EachNode[ast.TypeSpec] + ast.FuncDecl，检查 type alias 形态 + 禁止 method/`NewService`/`With*`/任意 top-level func | **Medium** | 配对 type-system Hard 防线：`type Service = appender.Service` 在 Go 编译期禁止 method on alias-to-non-local-type（语言级 Hard），sealed `Spec`/`ActorMode` + `MustNewSpec` whitelist 也是 Hard。archtest 仅作「slice 包不被弃用 alias 形态」的 Medium 兜底。godoc 已记录 Hard/Medium 关系。 |

## 检验路径与命令

| 项 | 命令 | 结果 |
|----|------|------|
| 全局 build | `go build ./...` | 通过 |
| 全 archtest | `go test ./tools/archtest/` | PASS（73.6s） |
| 新 archtest | `go test ./tools/archtest/ -run 'TestAuditLedgerProtocol_CompositionRootOnly\|TestAuditcoreAppenderSliceFacadesAreThin' -v` | 2/2 PASS |
| 元数据 | `go run ./cmd/gocell validate` | 0 error / 0 warning |
| HMAC key 流向（非测试） | `grep -rn 'WithChainHMAC' cells/ runtime/audit/ledger \| grep -v _test.go` | 仅出现在 `runtime/audit/ledger/storetest/suite.go`（test helper）与 `runtime/audit/ledger/protocol.go`（定义）；cells/auditcore 生产代码 0 命中。 |
| Protocol 构造（非测试） | `grep -rn 'ledger.NewProtocol\|ledger.MustNewProtocol' --include='*.go' \| grep -v _test.go` | `cmd/corebundle/audit_module.go`、`examples/ssobff/app.go`、`runtime/audit/ledger/storetest/suite.go`（合规豁免）|

## 分维度评分（Phase 评审 7 维度）

| 维度 | 评分 | 证据 |
|------|------|------|
| A. 工作流完整性（S0-S8） | 绿 | docs/architecture/ ADR + docs/plans/ 计划齐全；commit 历史显示 RED→GREEN→archtest 三步 |
| B. 工具合规 | 绿 | 4 个 slice.yaml 元数据由 scaffold 模式产出；archtest 用 scanner 框架（与 PR-Φ 后的 EachNode pattern 一致）；migration SQL 手写但 SQL 不在工具生成范围 |
| C. 角色完整性 | 绿 | Architect ADR、PG worker、redaction owner 均参与（commit 历史可见）|
| D. 内核集成健康度 | 绿 | kernel 层只新增 `pkg/errcode.ErrAuditChainBroken` 一个常量；无 kernel 依赖反向；archtest pass |
| E. 标准文件齐全度 | 绿 | ADR 202605101800、backlog 条目、observability.md §Audit Payload Redaction、plan 文档全齐 |
| F. 反馈闭环 | 绿 | commit 历史可见 `683a63cb` RED → `71682a0b` GREEN 的 dedup PR-PR450-DEDUP 单源化反馈被执行 |
| G. Tech Debt 趋势 | 绿 | 解决多份 fork 出的 4 个 ~150 行 service.go ⇒ 1 个 ~150 行 `appender.Service`；删除 `cells/auditcore/internal/{adapters/postgres,adapters/s3archive,mem,domain,ports}` 共 ~2200 行旧代码；净减少。新增 [TECH] 标签：未观察到。 |

## 必须修复项

无。所有 finding 均为 P2 治理强度改进，不阻塞合并。建议把 K-01、K-02、K-04 三条登记到 `docs/backlog.md` 显式跟踪（避免 silent carryover，参考 MEMORY: PR 范围切割必须显式 backlog）。
