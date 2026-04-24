# PR #245 — Kernel Guardian Review

- Branch: `refactor/520-pr-a5c-outbox-emitter-unify` (HEAD `3447eb9d`, 6 commits vs `develop`)
- Scope: PR-A5c OUTBOX-EMITTER-UNIFY — kernel 层新增 `DirectPublishModeForDurability` / `DurabilityReporter` / `ReportDurable`; Cell Option API 收敛到 `WithEmitter`+`WithOutboxDeps`; 删除 `runtime/outbox/envelope.go`; 新增 archtest `OUTBOX-CELL-01`; accesscore 拆出 `cell_providers.go`.
- Viewpoint: 分层合规 / 元数据一致性 / 契约完整性.

## Findings

| # | 文件:行号 | Cx | Scope | 描述 |
|---|-----------|----|-------|------|
| 1 | `kernel/cell/mode_resolver.go:1-9` | Cx0 | IN_SCOPE | 新 helper 仅 import `log/slog` + `kernel/outbox` + `kernel/persistence` + `pkg/errcode`. 无 runtime/adapters/cells 反向依赖，kernel 分层干净. |
| 2 | `kernel/outbox/emitter.go:104-151` | Cx0 | IN_SCOPE | `DurabilityReporter` 接口放在 `kernel/outbox` 合理——它是 Emitter 自身行为契约，`WriterEmitter.Durable()` / `DirectEmitter.Durable()` 各自语义都在该包内定义; `ReportDurable` 对 `nil`/未实现者安全降级为 false (fail-closed)，符合 "可选依赖在构造出口 fallback" 原则. |
| 3 | `tools/archtest/outbox_cell_test.go:122-137` | Cx0 | IN_SCOPE | `isCellFile` 精确匹配 `cells/<name>/cell.go` 三段 path，显式排除 `_test.go`、`slices/`、`internal/`、`vendor`. 测试 helper 若取名 `WithPublisher` 但位于 `_test.go` 或 `cell_test.go`/`options_test.go` 不会误报. 执行 `go test ./tools/archtest -run TestCellsDoNotExposeRawOutboxOptions` PASS. |
| 4 | `tools/archtest/outbox_cell_test.go:158-169` | Cx1 | IN_SCOPE | OUTBOX-CELL-01 与已有 OUTBOX-SERVICE-05 关系为**分层互补**（Cell 入口层 vs service 实现层）而非重叠；但两规则未在 doc-comment 中交叉引用。建议在 `outbox_cell_test.go` header 加一行 `// ref: outbox_service_test.go OUTBOX-SERVICE-05`. 非阻塞. |
| 5 | `cells/accesscore/cell_providers.go:1-14` | Cx0 | IN_SCOPE | 仅 import `cells/accesscore/internal/ports` + `runtime/auth`，在 `cells/` 允许依赖范围内. `cell.yaml` 不声明 `allowedFiles`（该字段是 **slice 级**强制，非 cell 级），FMT-14 不适用. |
| 6 | `runtime/outbox/envelope.go` (DELETED) | Cx0 | IN_SCOPE | 当前仓 self-consumed, 内部调用方全部指向 `kernel/outbox.MarshalEnvelope` (见 `DirectEmitter.Emit:85`). 无跨仓 consumer 依赖，契约无破坏. |
| 7 | `gocell validate --strict` | Cx0 | IN_SCOPE | 输出 `0 error(s), 1 warning(s)` — 仅 pre-existing REF-16 "assembly corebundle no boundary.yaml"，PR #245 未新增任何元数据问题. |
| 8 | `scripts/kg-verify.sh` | Cx0 | OUT_OF_SCOPE | 现仍 3 项 pre-existing 失败（kernel/cell/celltest→runtime/auth; cells/internal/testoutbox 跨 cell; go.mod whitelist drift）. PR #245 未触碰这些路径；4 个被 PR 修改的 test 文件 (accesscore/auth_integration_test.go 等) 的 testoutbox 导入均 pre-existing，非本 PR 引入. |

## 维度评分（七维度）

| 维度 | 评分 | 证据 |
|------|------|------|
| A 工作流完整性 | Green | 6 commits 涵盖 F2-F8，无阶段跳跃. |
| B 工具合规 | Green | archtest / validate 均由工具产出结果. |
| C 角色完整性 | N/A | 单席位审查. |
| D 内核集成健康度 | Green | kernel/outbox + kernel/cell 新增 surface 清晰，接口可选 (`DurabilityReporter`) 无破坏性. |
| E 标准文件齐全度 | Green | plan / backlog / tests 齐. |
| F 反馈闭环 | Green | A5a-R4/R5 在 PR #245 被销，plan 已登记. |
| G Tech Debt 趋势 | Green | 净删除 `runtime/outbox/envelope.go`+394 行 test，无新增 [TECH]. |

## 整体结论

**APPROVE**

分层合规 / 元数据 / 契约三维度全部清洁，archtest 新增规则 scope 精确无 false-positive，内核 surface 的 `DurabilityReporter` 作为可选接口设计得体。非阻塞建议：Finding #4 的跨规则引用可在 follow-up 补。

## 关键路径速查

- 内核新 surface: `kernel/cell/mode_resolver.go:137-146`; `kernel/outbox/emitter.go:104-146`
- archtest 新规则: `tools/archtest/outbox_cell_test.go:57-77,122-137`
- Cell 提供者拆分: `cells/accesscore/cell_providers.go`
- 删除: `runtime/outbox/envelope.go`, `runtime/outbox/envelope_test.go`
