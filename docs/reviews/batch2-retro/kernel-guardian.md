# Batch2 Retrospective — Kernel Guardian 治理席位

基线：`develop @ 1958a5a8`，11 PR 累计 diff `5313793b..1958a5a8`（920 文件）。
plan：`docs/plans/202604260058-l4-virtual-taco.md`。

## 治理主张兑现 spot-check

| Plan 主张 | 现状 | 证据 |
|---|---|---|
| FMT-20 walker 覆盖 allOf/anyOf/oneOf/prefixItems/if/then/else | ✅ 兑现 | `kernel/governance/rules_strict_extra.go:189-208`（`if/then/else` 用 visit-as-object，`allOf/anyOf/oneOf/prefixItems` 用数组迭代） |
| ADV-06 `endpoints.subscribers ↔ contractUsages[role=subscribe]` 双向校验 | ✅ 兑现 | `kernel/governance/rules_advisory.go:137-228`（`adv06ContractToSlice` + `adv06SliceToContract` 双向检测、SeverityError） |
| VERIFY-01 `verify.contract ↔ contractUsages` 闭环 | ✅ 兑现 | `kernel/governance/rules_verify.go:24-60` 注册于 `validate.go:159` |
| 生成器 fingerprint 全 contract kind 覆盖 | ✅ 兑现 | `kernel/assembly/generator.go:209-328` 走 `canonicalEncode(normalizeContract)` 反射全字段（http/event/command/projection），无 kind switch；`generator_fingerprint_test.go` 存在 |
| boundary regenerate-and-diff CI 门禁（M.6） | ⚠️ 策略变更 | plan 写的 `--boundary-only` flag 未实现（`main_test.go:274 TestRunGenerateAssemblyBoundaryOnlyRejected` 显式锁定 `flag provided but not defined`）。CI 改为 `go run ./cmd/gocell verify generated`（`_build-lint.yml:155-167`），从 metadata 派生 manifest；语义等价但 plan 未同步纠错 |
| typeseval helper + 旧 topic_const_resolver 删除 | ✅ 兑现 | `tools/archtest/internal/typeseval/{typeseval.go,typeseval_test.go}` 存在；`tools/archtest/topic_const_resolver*` 已物理删除（无 wrapper） |
| metadata schema 合规（旧字段名死透） | ✅ 兑现 | grep 全仓 `^cellId:` `^sliceId:` `^assemblyId:`(metadata) `^callsContracts:` `^publishes:` `^consumes:` `^ownedSlices:` `^authoritativeData:` `^producer:` `^consumers:` 在 `cells/`/`contracts/`/`assemblies/`/`journeys/` 下零命中（boundary.yaml 的 `assemblyId:` 是 generator 产物字段 schema，非 metadata 解析对象，无关） |
| no-dash id 强制（FMT-16/C1/A1） | ✅ 兑现 | `cells/`/`assemblies/` 下 cell.yaml/slice.yaml/assembly.yaml `^id:` 零 dash 命中；目录名亦无 dash |
| `actors.yaml` external Actor 登记 | ✅ 兑现 | 4 entries（edge-bff / external-audit-sink / example-iot-platform / example-order-platform），含 maxConsistencyLevel + REF-17 contract 内置说明 |
| `consistencyLevel` / `schema.primary` / `verify.smoke` 必填 | ✅ 兑现 | `gocell validate --strict` 0 errors |
| OBS-01 typed 静态守卫（`tools/metricschema.CheckOBS01`） | ✅ 兑现 | `tools/metricschema/schema.go` + `tools/generatedverify/generatedverify.go:189` 调用，6 个 schema_test 函数覆盖 helper-param / helper-return / branch-taint / comma-ok 路径 |
| REPO-LOG-KEY-ID-REDACT-01（K' 与 G1 typed event 双覆盖） | ✅ 存在 | `tools/archtest/repoerr_test.go:20,514` 注册 |
| `runtime/auth` 第四层 fail-closed | ✅ 兑现 | `runtime/auth/authenticator.go:146-162` `NewServiceTokenAuthenticator` error-first，构造期 reject nil-ring / nil-NonceStore / NonceStoreKindNoop |
| `Contract.ValidateErrorResponse` 全 envelope 校验（PR-CFG-L） | ✅ 兑现 | configread `contract_test.go` 三个 negative 测试均调（plan §L 描述与代码一致） |

## 治理 Findings

| ID | Severity | Complexity | Evidence | Root cause | Fix direction |
|---|---|---|---|---|---|
| KG-RETRO-01 | 🔴 Error | Low | `go test ./tools/archtest/...` 失败 — `auth_authtest_boundary_test.go:63` 把 `/Users/shengming/Documents/code/gocell/worktrees/217-pr-a64c-lint-full-enable/tools/archtest/testdata/prod_duration_fixtures/package_load_error/usage.go` 当作活文件 AST-scan，而它是 sibling worktree 内 deliberate-bad-syntax fixture（`expected declaration, found THIS`） | `collectGoFiles` 仅 SkipDir 当前 repo 的 `tools/archtest`，未 SkipDir `worktrees/`；任何并存 worktree 的 archtest fixture 都会污染主仓库测试 | `collectGoFiles` 加 `worktrees` 到 SkipDir 列表，或改用 `go list ./...` 拿模块内文件而非 filepath.Walk repo root |
| KG-RETRO-02 | 🔴 Error | Low | `TestListenerDXA52Guard` 报 13 处违规，全部位于 `bak/docs/*` 与 `docs/bak/*`（含 batch2 期间归档的 plan、capability map、PR341 cleanup backlog），命中 `HTTPRegistrar` / `Delegated` / `WithInternalMiddleware` 等已删 listener 旧 API 字面量 | `listener_dx_test.go` 的 active-docs walker 未把 `**/bak/**` 归入 archive；archived 文档不应触发"active docs"守卫 | `listener_dx_test.go` 路径过滤加 `bak/` 与 `docs/bak/` 前缀豁免；或统一搬到 `docs/plans/archive/` 与 plan 027/028 一致 |
| KG-RETRO-03 | 🟡 Warn | Low | plan §M.6 主张新增 `cmd/gocell/app/generate.go --boundary-only` flag；实际 `main_test.go:274 TestRunGenerateAssemblyBoundaryOnlyRejected` 显式锁定 flag 不存在；CI gate 改为 `gocell verify generated`（语义等价、更稳） | 执行期发现 verifier 从 metadata 派生 manifest 比 generator stdout diff 更可靠，方向正确，但 plan §M.6 文本未回写 | plan §M.6 段补一行"实际改用 verify generated metadata-driven 而非 --boundary-only flag"，避免后续读者按废弃描述误改 |

## 工具自检
- `gocell validate --strict` errors: **0**（warnings: 0）
- `go test ./tools/archtest/...` 状态：**FAIL** — 2 testcase（KG-RETRO-01 / -02 上面），`internal/typeseval` 子包 ok
- 旧字段名残留：**全仓零命中**（cells/contracts/assemblies/journeys 下 grep `^(cellId|sliceId|callsContracts|publishes|consumes|ownedSlices|authoritativeData|producer|consumers):` 全部空集；metadata-model-v3 迁移在 batch2 后无 backslide）

## Seat Digest
治理硬约束（FMT-20 walker 全 schema-composition 覆盖 / ADV-06 双向 / VERIFY-01 闭环 / generator 全-kind fingerprint / typeseval 替换旧 resolver / runtime/auth 第四层 fail-closed / no-dash id / metadata-v3 旧字段名零残留）batch2 全部兑现，`gocell validate --strict` 0 errors 是真的。两条 archtest 失败（worktree 路径漏过滤 + bak 文档守卫漏豁免）是 batch2 期间归档/并行 worktree 引入的边缘 case，需 1-2h 收尾。plan §M.6 描述与代码反向（flag 未加，CI 用 `verify generated` 代替），等价但要回写避免误导。
