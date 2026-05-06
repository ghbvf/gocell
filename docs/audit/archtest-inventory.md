# archtest + governance invariant inventory

> 派生自 `scripts/audit/list-archtests.sh`，可重跑刷新。
> 路线图：`docs/plans/202605070431-pr403-funnel-fix-roadmap.md`。

## 概览

- archtest 文件总数：**70**（合并前 104，本 PR 净减 34；含 6 个 fixture + 2 个 helper）
- governance rules_*.go 文件数：~24（含 _test.go 配对，规则定义 ~12）
- governance 规则总数：60+ 条（FMT-01..30, REF-01..17, TOPO-01..09, VERIFY-01..06, CH-04/05/06 等）
- 11 个 `*_invariants_test.go` 主题文件取代 45 个原拆分文件 + 11 个 INVARIANT 锚点补齐

## 本 PR 处置（同主题聚并）

| 主题 | 源文件数 | 目标 | 处置 |
|---|---|---|---|
| outbox | 7 | `outbox_invariants_test.go` | 聚并 |
| rmq | 5 | `rmq_invariants_test.go` | 聚并 |
| clock | 5（4 clock_ + 1 prod_clock_injection） | `clock_invariants_test.go` | 聚并（含 prod_clock_injection 与 clock_injection_prod_callsite 是否重复审查） |
| refresh | 3 | `refresh_invariants_test.go` | 聚并 |
| handler | 5 | `handler_invariants_test.go` | 聚并 |
| errcode | 5（constructor / message_const / error_first / details_slog_attr / exported_error_new） | `errcode_invariants_test.go` | 聚并 |
| panic | 2 | `panic_invariants_test.go` | 聚并 |
| httputil | 3 | `httputil_invariants_test.go` | 聚并 |
| assembly | 3 | `assembly_invariants_test.go` | 聚并 |
| prod | 3（duration_const / duration_const_internal / fixtures） | `prod_invariants_test.go` | 聚并（fixtures 文件仍保留） |
| codegen | 5（cell_gen / contract_gen / kind_parity / unified / spec_gen_topic） | `codegen_invariants_test.go` | 聚并 |

聚并源文件总数：~46 → 11 主题文件 + fixtures 保留。**净减 ~30 文件**。

## 不在本 PR 范围（明确 defer）

| 类型 | 处置 | 后续 PR |
|---|---|---|
| HTTP 响应 / 请求验证（HANDLER-* / CH-04..06）codegen 化 | 改 codegen 工具，性质不同 | PR-FUNNEL-02 |
| governance rules_*.go 60+ 条聚并 | 影响 gocell validate 全流程 | PR-FUNNEL-03 |

## 修补无 INVARIANT ID 的非 fixture 文件

未声明 ID 的非 helper / fixture 文件，本 PR 顺手补 `// INVARIANT: {ID}` 锚点：

- `accesscore_facade_test.go`
- `auth_authtest_boundary_test.go`
- `build_constraint_test.go`
- `cell_init_test.go`
- `ci_pinning_test.go`
- `contracttest_boundary_test.go`
- `corebundle_deps_test.go`
- `integration_guard_test.go`
- `listener_dx_test.go`
- `managed_resource_contract_test.go`
- `testutil_boundary_test.go`

不补 ID 的（helper 或 fixture，无独立规则）：
- `archtest_test.go` / `helpers_test.go`（共享 helper）
- `*_fixtures_test.go` × 6（夹具，不是规则）

---

## archtest 文件清单（70 个）

| 文件 | INVARIANT ID | 主题 |
|---|---|---|
| `accesscore_facade_test.go` | ACCESSCORE-FACADE-A61-01 | _misc |
| `adapter_returns_declared_types_test.go` | ADAPTER-RETURNS-DECLARED-TYPES-01 | _misc |
| `archtest_test.go` | _未声明_ | _misc |
| `assembly_invariants_test.go` | ASSEMBLY-MODULES-GEN-01 | assembly |
| `auth_authtest_boundary_test.go` | AUTH-AUTHTEST-BOUNDARY-01 | _misc |
| `auth_plan_test.go` | AUTH-PLAN-01 | _misc |
| `bootstrap_path_predicate_test.go` | BOOTSTRAP-PATH-PREDICATE-SOLE-01 | _misc |
| `build_constraint_test.go` | BUILD-CONSTRAINT-INTEGRATION-TAG-01 | _misc |
| `cell_init_test.go` | CELL-INIT-CONTRACTUSAGE-01 | _misc |
| `cellmeta_single_source_test.go` | CELLMETA-SINGLE-SOURCE-01 | _misc |
| `cells_no_wrapper_contractspec_import_test.go` | CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01 | _misc |
| `ci_pinning_test.go` | CI-PINNING-WORKFLOW-DIGEST-01 | _misc |
| `clock_invariants_test.go` | CLOCK-INJECTION-TEST-CALLSITE-01 | clock |
| `codegen_invariants_test.go` | CODEGEN-CELL-GEN-01
CODEGEN-INIT-INTERNAL-01
CODEGEN-USER-FILE-OVERLAP-01 | codegen |
| `contract_kinds_closed_set_test.go` | CONTRACT-KINDS-CLOSED-SET-01 | _misc |
| `contract_spec_clients_test.go` | INTERNAL-CONTRACT-CLIENTS-REQUIRED-01 | _misc |
| `contracttest_boundary_test.go` | CONTRACTTEST-BOUNDARY-01 | _misc |
| `corebundle_deps_test.go` | COREBUNDLE-DEPS-01 | _misc |
| `errcode_invariants_test.go` | ERRCODE-KIND-LITERAL-01 | errcode |
| `errcode_message_const_fixtures_test.go` | MESSAGE-CONST-LITERAL-01 | errcode |
| `event_camelcase_test.go` | EVENT-PAYLOAD-CAMELCASE-01 | _misc |
| `event_subscription_contractgen_coverage_test.go` | EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01 | _misc |
| `exported_error_new_fixtures_test.go` | EXPORTED-ERROR-NEW-01 | errcode |
| `goose_session_locker_fixtures_test.go` | _未声明_ | _misc |
| `goose_session_locker_test.go` | GOOSE-SESSION-LOCKER-01 | _misc |
| `handler_invariants_test.go` | HANDLER-NO-INLINE-LIMIT-PARSE-01 | handler |
| `health_aggregation_test.go` | HEALTH-AGG-01 | _misc |
| `helpers_test.go` | _未声明_ | _misc |
| `http_metrics_label_test.go` | HTTP-METRICS-LABEL-CELLID-CTXSOURCE-01 | _misc |
| `httputil_invariants_test.go` | HTTPUTIL-5XX-KIND-NORMALIZE-01 | httputil |
| `integration_guard_test.go` | INTEGRATION-GUARD-01 | _misc |
| `kernel_metadata_no_wire_test.go` | KERNEL-METADATA-NO-WIRE-01 | _misc |
| `kernel_poolstats_location_test.go` | KERNEL-POOLSTATS-LOCATION-01 | _misc |
| `lintgate_smoke_test.go` | LINT-GATE-SMOKE-01 | _misc |
| `listener_dx_test.go` | LISTENER-DX-01 | _misc |
| `managed_resource_contract_test.go` | MANAGED-RESOURCE-CONTRACT-01 | _misc |
| `migration_no_transaction_rerun_safe_test.go` | _未声明_ | _misc |
| `module_order_test.go` | MODULE-ORDER-CONFIGCORE-FIRST-01 | _misc |
| `no_deleted_auth_symbols_test.go` | NO-DELETED-AUTH-SYMBOLS-01 | _misc |
| `no_manual_contractspec_literal_test.go` | NO-MANUAL-CONTRACTSPEC-LITERAL-01 | _misc |
| `no_test_service_context_in_production_test.go` | NO-TEST-SERVICE-CONTEXT-IN-PRODUCTION-01 | test |
| `observability_metrics_test.go` | _未声明_ | _misc |
| `outbox_invariants_test.go` | OUTBOX-CELL-01 | outbox |
| `panic_invariants_test.go` | PANIC-REDACT-01 | panic |
| `patch_optional_bool_pointer_test.go` | PATCH-OPTIONAL-BOOL-POINTER-01 | _misc |
| `pgquery_boundary_test.go` | _未声明_ | _misc |
| `postgres_constructor_error_first_test.go` | PG-CONSTRUCTOR-MUST-FREE-01 | _misc |
| `prod_clock_injection_fixtures_test.go` | PROD-CLOCK-INJECTION-01 | clock |
| `prod_duration_fixtures_test.go` | PROD-DURATION-CONST-01 | prod |
| `prod_invariants_test.go` | PROD-DURATION-CONST-01 | _misc |
| `provision_state_removed_test.go` | PROVISION-STATE-AND-USERSOURCE-BOOTSTRAP-REMOVED-01 | _misc |
| `queryparam_drift_test.go` | PR-MODE-3 | _misc |
| `readyz_probe_naming_test.go` | READYZ-PROBE-NAMING-01 | _misc |
| `redis_idempotency_hashtag_test.go` | IDEMPOTENCY-LUA-HASHTAG-01 | _misc |
| `refresh_invariants_test.go` | REFRESH-CROSS-STORE-TX-01 | refresh |
| `repoerr_test.go` | CTXCANCEL-LOCAL-IMPL-BAN-01 | _misc |
| `rmq_invariants_test.go` | RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01 | rmq |
| `role_admin_literal_test.go` | ROLE-ADMIN-LITERAL-01 | _misc |
| `security_defaults_test.go` | PR-MODE-1 | _misc |
| `setup_admin_auth_test.go` | SETUP-ADMIN-NOT-PUBLIC-01 | _misc |
| `setup_admin_bootstrap_closure_test.go` | CELLS-NO-ROUTEMUX-WRAPPER-01 | _misc |
| `span_record_error_redact_test.go` | SPAN-RECORD-ERROR-REDACT-01 | _misc |
| `storage_backend_test.go` | STORAGE-BACKEND-PG-WIRING-01 | _misc |
| `svctoken_caller_cell_test.go` | SVCTOKEN-CALLER-CELL-REQUIRED-01 | _misc |
| `test_sleep_discipline_test.go` | TEST-SLEEP-DISCIPLINE-01 | test |
| `test_time_literal_fixtures_test.go` | TEST-TIME-LITERAL-01 | test |
| `test_time_literal_test.go` | TEST-TIME-LITERAL-01 | test |
| `testutil_boundary_test.go` | TESTUTIL-BOUNDARY-01 | _misc |
| `visit_buffer_then_commit_test.go` | VISIT-BUFFER-THEN-COMMIT-01 | _misc |
| `wire_code_5xx_single_source_test.go` | WIRE-CODE-5XX-SINGLE-SOURCE-01 | _misc |

## kernel/governance/rules_*.go 清单（27 个文件）

| 文件 | 包含的规则 |
|---|---|
| `rules_advisory.go` | ADV-01, ADV-03, ADV-04, ADV-05, ADV-06, REF-14, TOPO-03, TOPO-07 |
| `rules_consistency_test.go` | CONSISTENCY-EMIT-01 |
| `rules_consistency.go` | CONSISTENCY-EMIT-01 |
| `rules_docs_test.go` | DOC-NAME-01 |
| `rules_docs.go` | DOC-NAME-01 |
| `rules_fmt_test.go` | FMT-13, FMT-26, FMT-27, FMT-28, FMT-29 |
| `rules_fmt.go` | FMT-01, FMT-02, FMT-03, FMT-04, FMT-05, FMT-06, FMT-07, FMT-08, FMT-09, FMT-10, FMT-11, FMT-12, FMT-13, FMT-14, FMT-15, FMT-24, FMT-26, FMT-27, FMT-28, FMT-29, FMT-30, REF-12 |
| `rules_http_pathparam_uuid_test.go` | CH-05 |
| `rules_http_pathparam_uuid.go` | CH-04, CH-05 |
| `rules_http_response_alignment_test.go` | CH-04 |
| `rules_http_response_alignment.go` | CH-04 |
| `rules_http_typed_envelope_test.go` | CH-06 |
| `rules_http_typed_envelope.go` | CH-04, CH-06 |
| `rules_outbox.go` | OUTGUARD-01 |
| `rules_ref.go` | FMT-07, REF-01, REF-02, REF-03, REF-04, REF-05, REF-06, REF-07, REF-08, REF-09, REF-10, REF-11, REF-12, REF-13, REF-14, REF-15, REF-16, REF-17 |
| `rules_slice_test.go` | REF-01, SLICE-CONSISTENCY-01 |
| `rules_slice.go` | FMT-03, REF-01, SLICE-CONSISTENCY-01 |
| `rules_strict_extra_fmt20_test.go` | FMT-20 |
| `rules_strict_extra_test.go` | FMT-13, FMT-20, FMT-21, FMT-22, FMT-23, FMT-25 |
| `rules_strict_extra.go` | FMT-09, FMT-20, FMT-21, FMT-22, FMT-23, FMT-25, FMT-CONTRACT-DIR-ID-MATCH-01 |
| `rules_strict_test.go` | FMT-01, FMT-16, FMT-17, FMT-A1, FMT-C1, REF-05, VERIFY-06 |
| `rules_strict.go` | DOC-NAME-01, FMT-14, FMT-16, FMT-17, FMT-18, FMT-19, FMT-30, FMT-A1, FMT-C1, VERIFY-06, WRAPPER-CONTRACTSPEC-IMPORT-01 |
| `rules_topo_test.go` | FMT-03, TOPO-09 |
| `rules_topo.go` | FMT-03, REF-02, TOPO-01, TOPO-02, TOPO-03, TOPO-04, TOPO-05, TOPO-06, TOPO-07, TOPO-08, TOPO-09 |
| `rules_verify.go` | FMT-03, REF-09, VERIFY-01, VERIFY-02, VERIFY-03, VERIFY-04, VERIFY-05, VERIFY-06 |
| `rules_wrapper_test.go` | FMT-18, FMT-19, WRAPPER-CONTRACTSPEC-IMPORT-01 |
| `rules_wrapper.go` | FMT-18, FMT-19, WRAPPER-CONTRACTSPEC-IMPORT-01, WRAPPER-NO-PACKAGE-STATE |
