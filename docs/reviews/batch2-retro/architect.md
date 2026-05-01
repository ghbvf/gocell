# Batch2 Retrospective — 架构席位

## 主张兑现 spot-check

| Plan 主张 | 现状 | 证据(path:line) |
| --- | --- | --- |
| I.1 `runtime/auth.NewServiceTokenAuthenticator` 改 `(Authenticator, error)`，构造期拒 nil ring / nil NonceStore / Noop kind | ✅ | `runtime/auth/authenticator.go:146-164`（三层 reject + ring/NonceStore/Kind 三 guard） |
| I.2 `sessionlogin.persistSessionWithRefresh` durable tx 跳过显式 cleanup（用 `isNoopTx`） | ✅ | `cells/accesscore/slices/sessionlogin/service.go:200-220, 233-236`（`isNoopTx` typed assertion `cell.Nooper`） |
| K' configcore PG repo 直接调 `ctxcancel.Wrap`，删 `wrapCtxCancel` thin wrapper | ✅ | `cells/configcore/internal/adapters/postgres/config_repo.go:452`（`ctxcancel.Wrap(scanErr, "Update", "key="+key)` 直调，无本地 wrapper） |
| K' `observeStaleCipher` slog 字段只剩 `key`；`stored_key_id` / `current_key_id` 走回调 | ✅ | `cells/configcore/internal/adapters/postgres/config_repo.go:412-418` |
| L admin gate + RoleInternalAdmin Policy on internal listener | ✅ | `cells/configcore/slices/configread/handler.go:50-65, 75-84` |
| M outbox fail-open metrics counter `outbox_emit_failopen_dropped_total{cell, topic}` | ✅ | `kernel/outbox/emitter.go:132-139, 196-200`（构造期注册 + Emit fail-open 分支自增 + tracker） |
| M typeseval helper（`tools/archtest/internal/typeseval/`）+ 旧 resolver 删除 | ✅ | `tools/archtest/internal/typeseval/typeseval.go:1-45`，`outbox_topic_test.go:14-20` 直接 import |
| H Lock/Unlock RMW 全在同一 RunInTx 闭包 | ✅ | `cells/accesscore/slices/identitymanage/service.go:357-383, 402-417` |
| G1 G.8 accesscore PG outbox 走 `shared.Topology.StorageBackend == "postgres"` 分支 + nil-check fail-fast | ✅ | `cmd/corebundle/access_module.go:112-124` |
| G1 G.9.3 module-order archtest 强制 `assemblies/corebundle/assembly.yaml::cells[0] == "configcore"` | ✅ | `tools/archtest/module_order_test.go:24-35` |
| G2-FU1 role assign/revoke `endpoints.clients=[]` 修正 | ✅ | `contracts/http/auth/role/assign/v1/contract.yaml:10` |
| **PR-A66 拆 5 sub-struct + Bootstrap 嵌入** | ❌ | `runtime/bootstrap/bootstrap.go:48-122`（**Bootstrap 字段仍 flat**；注释明文「Forcing a 'concern group' sub-struct layout would make those cross-cutting consumptions look like boundary violations」；只做了 phases 文件级拆分） |
| **G.9.1 storage-backend archtest 升级到 packages.Load + go/types**（plan 字面） | ❌ | `tools/archtest/storage_backend_test.go:30-40, 65-71`（仍用 `go/parser` + `parser.SkipObjectResolution`；用 `buildLocalVarValues` 同函数局部 var resolver）；与 typeseval 范式分裂 |

## 跨 PR 漂移 findings

| ID | Severity | Complexity | Evidence | Root cause | Fix direction |
| --- | --- | --- | --- | --- | --- |
| RA-B2-1 PLAN-IMPL-DRIFT-A66 | P1 | Cx2 | `runtime/bootstrap/bootstrap.go:48-69` 注释主动反驳 plan「拆 5 sub-struct」；archive plan L159 仍写 ✅ DONE | A66 实施期发现 sub-struct 反而误导（cross-cutting fields），决定保留 flat + phase 文件拆分；plan 进度速览未同步「实际只做文件级拆分」的语义降级 | 在 `docs/plans/202604260058-l4-virtual-taco.md` 进度表 PR-A66 加注「实际仅 phase 文件拆分，struct 保留 flat（fx/kratos 同模式）」；同时归档行同改 |
| RA-B2-2 ARCHTEST-RESOLVER-FORK | P1 | Cx2 | `tools/archtest/storage_backend_test.go:30-40` 用 `go/parser` + 自建 `buildLocalVarValues`；`tools/archtest/outbox_topic_test.go:14-20` 用 `typeseval`（packages.Load + go/types） | M.2.D 提取了 typeseval 共享 helper，但 G.9.1 storage_backend 没切到 typeseval（plan 原话「复用 PR-A49 packages.Load + go/types TypesInfo」未兑现）；两套常量传播范式并存导致维护面双倍 | storage_backend_test.go 切到 typeseval；规则注释里明确「跨函数 / 方法链 deliberately out of scope」时仍可受益于 go/types 的 same-package import resolution |
| RA-B2-3 KERNEL-OBS-METRICS-LAYER | P2 | Cx1 | `kernel/outbox/emitter.go:10` import `kernel/observability/metrics`；`kernel/observability/metrics/metrics.go:1-19` 是 kernel 自有的 provider-neutral 抽象 | 为 outbox emit fail-open counter 引入 kernel 内的 metrics 抽象层，与 plan 描述的「provider 接口在 `runtime/observability/metrics/` 暴露，kernel/outbox 通过依赖反转持有」**位置不一致**（实际放进了 kernel/） | 这是合理的——provider abstraction 必须在 kernel/ 才能让 cells/ 也直接调；plan 描述需更正；不是漂移，但 plan 文档与实施位置语义需同步 |
| RA-B2-4 BOOTSTRAP-RUNTIME-CIRC-RISK | P2 | Cx1 | `runtime/bootstrap/bootstrap.go:25-32` 同时 import `kernel/observability/metrics` 和 `runtime/observability/metrics`（别名 `metricsmiddleware`） | runtime 模块跨 kernel/runtime 双 metrics 命名空间。short term 没问题（runtime 允许依赖 kernel），但增加心智负担 | 文档化「kernel metrics provider = abstraction，runtime metrics = HTTP collector + middleware」分工，避免后续误用 |
| RA-B2-5 PLAN-PROGRESS-TABLE-A66 | P2 | Cx1 | `docs/plans/202604260058-l4-virtual-taco.md:25` PR-A66 标 ✅ 已合并 #333，但 PR scope 实际比 plan 收窄 | plan 文件没更新「实际工作 vs 字面工作」差异；retrospective 时易让人误以为 sub-struct 已落地 | 在 PR-A66 行加注释或单独行说明实际范围 |

## Seat Digest

- 主张兑现整体良好：12/14 spot-check 完全落地；2 条是 plan-vs-impl 文档同步问题，不是代码缺陷。I（fail-closed 四层）/ K'（ctxcancel + redact）/ L（admin gate）/ M（outbox metrics + typeseval）/ H（RMW 原子）/ G1（PG 装配 + module order archtest）跨 PR 边界清晰，无逆向依赖。
- 关键漂移：A66 sub-struct 拆分**主动放弃**（架构上正确决定，理由写在源码注释里），但 plan 文件和归档版仍写「拆 5 sub-struct」；G.9.1 storage_backend archtest 与 M typeseval 范式分裂（两套常量传播代码并存）。
- 不需要立即开 follow-up PR：两条 P1 都是 plan 文档/archtest 收敛性问题，可以并入下一个治理批次（或在 backlog 登记 ARCHTEST-TYPESEVAL-CONSOLIDATE-01 + PLAN-A66-RANGE-NOTE-01 两条）。无 P0。
