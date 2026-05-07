<!-- DO NOT EDIT: regenerate with scripts/audit/list-archtests.sh -->
# archtest + governance invariant inventory

> 派生自 `scripts/audit/list-archtests.sh`，由 `hack/verify-archtest-inventory.sh` 漂移闸守护。
> 路线图：`docs/plans/202605070431-pr403-funnel-fix-roadmap.md`。

## 概览

- archtest 文件总数：71
- archtest INVARIANT 锚点数：170
- governance `rules_*.go` 文件数：15

## archtest 规则清单

| INVARIANT | 文件 | 行 | 主题 |
|---|---|---|---|
| `ACCESSCORE-FACADE-A61-01` | `accesscore_facade_test.go` | 1 | accesscore |
| `ADAPTER-RETURNS-DECLARED-TYPES-01` | `adapter_returns_declared_types_test.go` | 1 | adapter |
| `ASSEMBLY-CELLMODULE-TYPE-04` | `assembly_invariants_test.go` | 378 | assembly |
| `ASSEMBLY-MAXCONSISTENCY-DERIVED-03` | `assembly_invariants_test.go` | 276 | assembly |
| `ASSEMBLY-MODULES-GEN-01` | `assembly_invariants_test.go` | 82 | assembly |
| `ASSEMBLY-MODULES-SWITCH-FORBIDDEN-02` | `assembly_invariants_test.go` | 182 | assembly |
| `ASSEMBLY-SNAPSHOTS-LOCKED-01` | `assembly_invariants_test.go` | 474 | assembly |
| `ASSEMBLYREF-METHOD-SET-01` | `assembly_invariants_test.go` | 1069 | assembly |
| `AUTH-AUTHTEST-BOUNDARY-01` | `auth_authtest_boundary_test.go` | 1 | auth |
| `AUTH-BOOTSTRAP-PATH-RESTRICTED-01` | `setup_admin_auth_test.go` | 7 | auth |
| `AUTH-PLAN-01` | `auth_plan_test.go` | 7 | auth |
| `AUTH-PLAN-02` | `auth_plan_test.go` | 9 | auth |
| `AUTH-PLAN-03` | `auth_plan_test.go` | 11 | auth |
| `AUTH-PLAN-04` | `auth_plan_test.go` | 12 | auth |
| `AUTH-ROUTE-BOOTSTRAP-FLAG-REMOVED-01` | `setup_admin_bootstrap_closure_test.go` | 19 | auth |
| `B2-A-11` | `postgres_constructor_error_first_test.go` | 6 | b2 |
| `BOOTSTRAP-PATH-PREDICATE-SOLE-01` | `bootstrap_path_predicate_test.go` | 1 | bootstrap |
| `BUILD-CONSTRAINT-INTEGRATION-TAG-01` | `build_constraint_test.go` | 1 | build |
| `CELL-INIT-CONTRACTUSAGE-01` | `cell_init_test.go` | 1 | cell |
| `CELLMETA-SINGLE-SOURCE-01` | `cellmeta_single_source_test.go` | 1 | cellmeta |
| `CELLMETA-SINGLE-SOURCE-02` | `cellmeta_single_source_test.go` | 12 | cellmeta |
| `CELLMETA-SINGLE-SOURCE-03` | `cellmeta_single_source_test.go` | 13 | cellmeta |
| `CELLS-NO-ROUTEMUX-WRAPPER-01` | `setup_admin_bootstrap_closure_test.go` | 12 | cells |
| `CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01` | `cells_no_wrapper_contractspec_import_test.go` | 1 | cells |
| `CI-PINNING-WORKFLOW-DIGEST-01` | `ci_pinning_test.go` | 1 | ci |
| `CLOCK-INJECTION-PROD-CALLSITE-01` | `clock_invariants_test.go` | 446 | clock |
| `CLOCK-INJECTION-TEST-CALLSITE-01` | `clock_invariants_test.go` | 261 | clock |
| `CODEGEN-CELL-GEN-01` | `codegen_invariants_test.go` | 56 | codegen |
| `CODEGEN-CELL-GEN-02` | `codegen_invariants_test.go` | 57 | codegen |
| `CODEGEN-CELL-GEN-03` | `codegen_invariants_test.go` | 58 | codegen |
| `CODEGEN-CELL-GEN-04` | `codegen_invariants_test.go` | 59 | codegen |
| `CODEGEN-CONTRACT-GEN-01` | `codegen_invariants_test.go` | 253 | codegen |
| `CODEGEN-CONTRACT-GEN-01` | `patch_optional_bool_pointer_test.go` | 68 | codegen |
| `CODEGEN-CONTRACT-GEN-02` | `codegen_invariants_test.go` | 254 | codegen |
| `CODEGEN-CONTRACT-USER-OVERLAP-01` | `codegen_invariants_test.go` | 255 | codegen |
| `COMMAND-PROJECTION-EXPLICIT-01` | `codegen_invariants_test.go` | 416 | command |
| `CONTRACT-KINDS-CLOSED-SET-01` | `contract_kinds_closed_set_test.go` | 1 | contract |
| `CONTRACTTEST-BOUNDARY-01` | `contracttest_boundary_test.go` | 1 | contracttest |
| `COREBUNDLE-DEPS-01` | `corebundle_deps_test.go` | 1 | corebundle |
| `CTXCANCEL-LOCAL-IMPL-BAN-01` | `repoerr_test.go` | 19 | ctxcancel |
| `DETAILS-SLOG-ATTR-01` | `errcode_invariants_test.go` | 1226 | errcode |
| `ERRCODE-KIND-LITERAL-01` | `errcode_invariants_test.go` | 182 | errcode |
| `ERROR-FIRST-API-01` | `errcode_invariants_test.go` | 517 | errcode |
| `ERROR-FIRST-TYPED-NIL-01` | `errcode_invariants_test.go` | 554 | errcode |
| `EVENT-DTO-CAMELCASE-01` | `event_camelcase_test.go` | 79 | event |
| `EVENT-PAYLOAD-CAMELCASE-01` | `event_camelcase_test.go` | 18 | event |
| `EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01` | `event_subscription_contractgen_coverage_test.go` | 1 | event |
| `EXPORTED-ERROR-NEW-01` | `errcode_invariants_test.go` | 1460 | errcode |
| `EXPORTED-ERROR-NEW-01` | `exported_error_new_fixtures_test.go` | 2 | errcode |
| `GOOSE-SESSION-LOCKER-01` | `goose_session_locker_test.go` | 3 | goose |
| `HANDLER-POLICY-REQUIRED-01` | `handler_policy_required_test.go` | 48 | handler |
| `HEALTH-AGG-01` | `health_aggregation_test.go` | 3 | health |
| `HTTP-METRICS-LABEL-CELLID-CTXSOURCE-01` | `http_metrics_label_test.go` | 20 | http |
| `HTTP-METRICS-LABEL-NO-ASSEMBLY-DERIVE-01` | `http_metrics_label_test.go` | 21 | http |
| `HTTP-METRICS-LABEL-NO-CONFIG-CELLID-01` | `http_metrics_label_test.go` | 22 | http |
| `HTTP-METRICS-LABEL-ROUTER-ATTRIBUTION-01` | `http_metrics_label_test.go` | 24 | http |
| `HTTP-METRICS-LABEL-RUNTIME-SENTINEL-01` | `http_metrics_label_test.go` | 23 | http |
| `HTTPUTIL-5XX-KIND-NORMALIZE-01` | `httputil_invariants_test.go` | 20 | httputil |
| `HTTPUTIL-5XX-LOG-REDACT-01` | `httputil_invariants_test.go` | 116 | httputil |
| `HTTPUTIL-SURFACE-REGISTERED-01` | `httputil_invariants_test.go` | 179 | httputil |
| `IDEMPOTENCY-LUA-HASHTAG-01` | `redis_idempotency_hashtag_test.go` | 3 | idempotency |
| `INTEGRATION-GUARD-01` | `integration_guard_test.go` | 1 | integration |
| `INTERNAL-CONTRACT-CLIENTS-REQUIRED-01` | `contract_spec_clients_test.go` | 1 | internal |
| `KERNEL-CLOCK-LEAF-FALLBACK-01` | `clock_invariants_test.go` | 559 | kernel |
| `KERNEL-CLOCK-RESET-RELATIVE-PROD-01` | `clock_invariants_test.go` | 793 | kernel |
| `KERNEL-METADATA-NO-WIRE-01` | `kernel_metadata_no_wire_test.go` | 1 | kernel |
| `KERNEL-POOLSTATS-LOCATION-01` | `kernel_poolstats_location_test.go` | 1 | kernel |
| `LAYER-01` | `archtest_test.go` | 46 | layer |
| `LAYER-01` | `lintgate_smoke_test.go` | 6 | layer |
| `LAYER-06` | `archtest_test.go` | 94 | layer |
| `LAYER-09` | `auth_plan_test.go` | 70 | layer |
| `LINT-GATE-SMOKE-01` | `lintgate_smoke_test.go` | 1 | lint |
| `LISTENER-DX-01` | `listener_dx_test.go` | 1 | listener |
| `LITERAL-01` | `errcode_message_const_fixtures_test.go` | 71 | literal |
| `MANAGED-RESOURCE-CONTRACT-01` | `managed_resource_contract_test.go` | 1 | managed |
| `MARKER-MISSING-FOR-WIRE-CALL-01` | `codegen_invariants_test.go` | 564 | marker |
| `MARKER-WIRE-SINGLE-SOURCE-01` | `codegen_invariants_test.go` | 566 | marker |
| `MARKERGEN-DRIFT-VERIFY-01` | `codegen_invariants_test.go` | 565 | markergen |
| `MESSAGE-CONST-LITERAL-01` | `errcode_invariants_test.go` | 286 | errcode |
| `MESSAGE-CONST-LITERAL-01` | `errcode_message_const_fixtures_test.go` | 2 | errcode |
| `META-QUERYPARAM-DRIFT-01` | `queryparam_drift_test.go` | 34 | meta |
| `METADATA-LIMITS-SINGLE-SOURCE-01` | `outbox_invariants_test.go` | 1645 | outbox |
| `MIGRATION-NO-TRANSACTION-RERUN-SAFE-01` | `migration_no_transaction_rerun_safe_test.go` | 11 | migration |
| `MODULE-ORDER-CONFIGCORE-FIRST-01` | `module_order_test.go` | 17 | module |
| `NO-DELETED-AUTH-SYMBOLS-01` | `no_deleted_auth_symbols_test.go` | 1 | no |
| `NO-MANUAL-CONTRACTSPEC-LITERAL-01` | `cells_no_wrapper_contractspec_import_test.go` | 7 | no |
| `NO-MANUAL-CONTRACTSPEC-LITERAL-01` | `no_manual_contractspec_literal_test.go` | 1 | no |
| `NO-METADATA-LITERAL-IN-CELLGO-01` | `codegen_invariants_test.go` | 562 | no |
| `NO-TEST-SERVICE-CONTEXT-IN-PRODUCTION-01` | `no_test_service_context_in_production_test.go` | 1 | no |
| `NO-WIRE-FIELDS-IN-YAML-01` | `codegen_invariants_test.go` | 563 | no |
| `OBS-01` | `observability_metrics_test.go` | 12 | obs |
| `OUTBOX-CELL-01` | `outbox_invariants_test.go` | 61 | outbox |
| `OUTBOX-HANDLERESULT-NO-RECEIPT-FIELD-01` | `outbox_invariants_test.go` | 600 | outbox |
| `OUTBOX-LEASE-ID-CAS-01` | `outbox_invariants_test.go` | 232 | outbox |
| `OUTBOX-MARK-RETURNS-BOOL-01` | `outbox_invariants_test.go` | 289 | outbox |
| `OUTBOX-METADATA-MAX-BYTES-01` | `outbox_invariants_test.go` | 352 | outbox |
| `OUTBOX-PAYLOAD-SIZE-01` | `outbox_invariants_test.go` | 472 | outbox |
| `OUTBOX-RELAY-LOST-METRIC-01` | `outbox_invariants_test.go` | 661 | outbox |
| `OUTBOX-RELAY-LOST-METRIC-01` | `outbox_invariants_test.go` | 743 | outbox |
| `OUTBOX-SERVICE-01` | `outbox_invariants_test.go` | 819 | outbox |
| `OUTBOX-SERVICE-02` | `outbox_invariants_test.go` | 820 | outbox |
| `OUTBOX-SERVICE-03` | `outbox_invariants_test.go` | 821 | outbox |
| `OUTBOX-SERVICE-04` | `outbox_invariants_test.go` | 822 | outbox |
| `OUTBOX-SERVICE-05` | `outbox_invariants_test.go` | 823 | outbox |
| `OUTBOX-TOPIC-FAILOPEN-01` | `outbox_invariants_test.go` | 1253 | outbox |
| `OUTBOX-TOPIC-FAILOPEN-01` | `outbox_invariants_test.go` | 1469 | outbox |
| `PANIC-REDACT-01` | `panic_invariants_test.go` | 25 | panic |
| `PANIC-REGISTERED-01` | `panic_invariants_test.go` | 129 | panic |
| `PATCH-OPTIONAL-BOOL-POINTER-01` | `patch_optional_bool_pointer_test.go` | 1 | patch |
| `PG-CONSTRUCTOR-MUST-FREE-01` | `postgres_constructor_error_first_test.go` | 3 | pg |
| `PGQUERY-01` | `pgquery_boundary_test.go` | 20 | pgquery |
| `POSTGRES-MIGRATOR-LOCK-ORDER-REGRESSION-01` | `goose_session_locker_test.go` | 7 | postgres |
| `PR-CI-6` | `prod_duration_fixtures_test.go` | 5 | pr |
| `PR-MODE-1` | `security_defaults_test.go` | 3 | pr |
| `PR-MODE-3` | `queryparam_drift_test.go` | 3 | pr |
| `PROD-CLOCK-INJECTION-01` | `clock_invariants_test.go` | 1110 | prod |
| `PROD-CLOCK-INJECTION-01` | `prod_clock_injection_fixtures_test.go` | 2 | prod |
| `PROD-CLOCK-INJECTION-01` | `test_sleep_discipline_test.go` | 14 | prod |
| `PROD-DURATION-CONST-01` | `prod_duration_fixtures_test.go` | 2 | prod |
| `PROD-DURATION-CONST-01` | `prod_invariants_test.go` | 35 | prod |
| `PROD-DURATION-CONST-01` | `test_time_literal_fixtures_test.go` | 7 | prod |
| `PROD-DURATION-CONST-01` | `test_time_literal_test.go` | 29 | prod |
| `PROVISION-STATE-AND-USERSOURCE-BOOTSTRAP-REMOVED-01` | `provision_state_removed_test.go` | 14 | provision |
| `READYZ-PROBE-NAMING-01` | `readyz_probe_naming_test.go` | 3 | readyz |
| `REFRESH-AMBIENT-TX-01` | `refresh_invariants_test.go` | 358 | refresh |
| `REFRESH-CROSS-STORE-TX-01` | `refresh_invariants_test.go` | 31 | refresh |
| `REFRESH-INVALID-INDEX-SINGLE-SOURCE-01` | `refresh_invariants_test.go` | 265 | refresh |
| `REPO-LOG-KEY-ID-REDACT-01` | `repoerr_test.go` | 20 | repo |
| `RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01` | `rmq_invariants_test.go` | 44 | rmq |
| `RMQ-CHANNEL-MAX-PER-CONN-01` | `rmq_invariants_test.go` | 335 | rmq |
| `RMQ-CHANNEL-MAX-PER-CONN-01` | `rmq_invariants_test.go` | 389 | rmq |
| `RMQ-CHANNEL-MAX-PER-CONN-01` | `rmq_invariants_test.go` | 499 | rmq |
| `RMQ-PUBLISHER-FAILURE-HANDLING-01` | `rmq_invariants_test.go` | 560 | rmq |
| `RMQ-PUBLISHER-FAILURE-HANDLING-01` | `rmq_invariants_test.go` | 613 | rmq |
| `RMQ-PUBLISHER-FAILURE-HANDLING-01` | `rmq_invariants_test.go` | 669 | rmq |
| `RMQ-PUBLISHER-FAILURE-HANDLING-01` | `rmq_invariants_test.go` | 723 | rmq |
| `RMQ-PUBLISHER-RELEASES-CHANNEL-01` | `rmq_invariants_test.go` | 945 | rmq |
| `RMQ-STOPINTAKE-INFLIGHT-WAIT-01` | `rmq_invariants_test.go` | 1064 | rmq |
| `RMQ-STOPINTAKE-INFLIGHT-WAIT-01` | `rmq_invariants_test.go` | 1164 | rmq |
| `RMQ-STOPINTAKE-INFLIGHT-WAIT-01` | `rmq_invariants_test.go` | 1246 | rmq |
| `RMQ-STOPINTAKE-INFLIGHT-WAIT-01` | `rmq_invariants_test.go` | 1313 | rmq |
| `ROLE-ADMIN-LITERAL-01` | `role_admin_literal_test.go` | 1 | role |
| `ROLE-ADMIN-LITERAL-02` | `role_admin_literal_test.go` | 2 | role |
| `SEC-FAIL-CLOSED-01` | `security_defaults_test.go` | 5 | sec |
| `SEC-FAIL-CLOSED-02` | `security_defaults_test.go` | 47 | sec |
| `SEC-FAIL-CLOSED-03` | `security_defaults_test.go` | 48 | sec |
| `SEC-FAIL-CLOSED-04` | `security_defaults_test.go` | 49 | sec |
| `SEC-FAIL-CLOSED-05` | `security_defaults_test.go` | 50 | sec |
| `SEC-FAIL-CLOSED-06` | `security_defaults_test.go` | 51 | sec |
| `SEC-FAIL-CLOSED-07` | `security_defaults_test.go` | 52 | sec |
| `SEC-FAIL-CLOSED-08` | `security_defaults_test.go` | 53 | sec |
| `SEC-FAIL-CLOSED-09` | `security_defaults_test.go` | 54 | sec |
| `SETUP-ADMIN-CODEGEN-BOOTSTRAP-AUTH-WIRED-01` | `setup_admin_bootstrap_closure_test.go` | 24 | setup |
| `SETUP-ADMIN-NOT-PUBLIC-01` | `setup_admin_auth_test.go` | 1 | setup |
| `SLOWGATE-ALLOWLIST-01` | `slowgate_allowlist_test.go` | 1 | slowgate |
| `SPAN-RECORD-ERROR-REDACT-01` | `span_record_error_redact_test.go` | 1 | span |
| `SPAN-RECORD-ERROR-REDACT-ARCHTEST-01` | `span_record_error_redact_test.go` | 22 | span |
| `SPEC-GEN-TOPIC-EQUALS-CONTRACT-ID-01` | `codegen_invariants_test.go` | 739 | codegen |
| `SPEC-GEN-VALUE-PARITY-01` | `codegen_invariants_test.go` | 417 | codegen |
| `STORAGE-BACKEND-MEMORY-NO-PG-01` | `storage_backend_test.go` | 11 | storage |
| `STORAGE-BACKEND-PG-WIRING-01` | `storage_backend_test.go` | 6 | storage |
| `SVCTOKEN-CALLER-CELL-REQUIRED-01` | `svctoken_caller_cell_test.go` | 1 | svctoken |
| `TEST-SLEEP-DISCIPLINE-01` | `slowgate_allowlist_test.go` | 20 | test |
| `TEST-SLEEP-DISCIPLINE-01` | `test_sleep_discipline_test.go` | 1 | test |
| `TEST-TIME-LITERAL-01` | `slowgate_allowlist_test.go` | 77 | test |
| `TEST-TIME-LITERAL-01` | `test_sleep_discipline_test.go` | 25 | test |
| `TEST-TIME-LITERAL-01` | `test_time_literal_fixtures_test.go` | 2 | test |
| `TEST-TIME-LITERAL-01` | `test_time_literal_test.go` | 1 | test |
| `TESTUTIL-BOUNDARY-01` | `testutil_boundary_test.go` | 1 | testutil |
| `VISIT-BUFFER-THEN-COMMIT-01` | `visit_buffer_then_commit_test.go` | 1 | visit |
| `WIRE-CODE-5XX-SINGLE-SOURCE-01` | `wire_code_5xx_single_source_test.go` | 1 | wire |

## governance `rules_*.go` 清单

| 文件 | 包含的规则 |
|---|---|
| `rules_advisory.go` | ADV-01, ADV-03, ADV-04, ADV-05, ADV-06, REF-14, TOPO-03, TOPO-07 |
| `rules_consistency.go` | CONSISTENCY-EMIT-01 |
| `rules_docs.go` | DOC-NAME-01 |
| `rules_fmt.go` | FMT-01, FMT-02, FMT-03, FMT-04, FMT-05, FMT-06, FMT-07, FMT-08, FMT-09, FMT-10, FMT-11, FMT-12, FMT-13, FMT-14, FMT-15, FMT-24, FMT-26, FMT-27, FMT-28, FMT-29, FMT-30, REF-12 |
| `rules_http_pathparam_uuid.go` | CH-04, CH-05 |
| `rules_http_response_alignment.go` | CH-04 |
| `rules_http_typed_envelope.go` | CH-04, CH-06 |
| `rules_outbox.go` | OUTGUARD-01 |
| `rules_ref.go` | FMT-07, REF-01, REF-02, REF-03, REF-04, REF-05, REF-06, REF-07, REF-08, REF-09, REF-10, REF-11, REF-12, REF-13, REF-14, REF-15, REF-16, REF-17 |
| `rules_slice.go` | FMT-03, REF-01, SLICE-CONSISTENCY-01 |
| `rules_strict.go` | DOC-NAME-01, FMT-14, FMT-16, FMT-17, FMT-18, FMT-19, FMT-30, FMT-A1, FMT-C1, VERIFY-06, WRAPPER-CONTRACTSPEC-IMPORT-01 |
| `rules_strict_extra.go` | FMT-09, FMT-20, FMT-21, FMT-22, FMT-23, FMT-25, FMT-CONTRACT-DIR-ID-MATCH-01 |
| `rules_topo.go` | FMT-03, REF-02, TOPO-01, TOPO-02, TOPO-03, TOPO-04, TOPO-05, TOPO-06, TOPO-07, TOPO-08, TOPO-09 |
| `rules_verify.go` | FMT-03, REF-09, VERIFY-01, VERIFY-02, VERIFY-03, VERIFY-04, VERIFY-05, VERIFY-06 |
| `rules_wrapper.go` | FMT-18, FMT-19, WRAPPER-CONTRACTSPEC-IMPORT-01, WRAPPER-NO-PACKAGE-STATE |
