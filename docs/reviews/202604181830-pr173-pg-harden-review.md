# PR#173 六席审查报告 — fix(pg): harden schema guard + online migrations + relay integration tests

> 审查时间：2026-04-18 18:30
> 审查对象：PR https://github.com/ghbvf/gocell/pull/173
> 分支：`fix/294-pg-harden`
> Commit 范围：f237dd8..4f4ed17（5 个 commit）
> 审查基准：`/Users/shengming/.claude/plans/cozy-humming-mist.md`

## 摘要

共发现 **9 条 Finding**（Cx1: 4 / Cx2: 4 / Cx3: 0 / Cx4: 1）。

- **P0 Cx1**：1 条（F-1，编译错误，阻塞 merge）
- **P1 Cx2**：2 条（F-2、F-3，测试覆盖缺口）
- **P2 Cx1/Cx2**：5 条（可维护性 / 可观测性 / 文档）
- **Cx4**：1 条（backlog 条目拆分建议）

架构合规、SQL online-safety、migration 006、main.go fail-fast 接线均符合 plan 要求，无分层违规。

**结论**：不可合并。F-1 必须修复且集成测试重新跑通后才能 merge。

---

## Finding 清单

### F-1 集成测试文件缺少 `errcode` import，`-tags integration` 下编译失败

- **Cx 级别**：Cx1（P0）
- **维度**：测试
- **文件**：`adapters/postgres/outbox_relay_integration_test.go:389,398`
- **证据**：文件 import 块（行 5-18）中无 `"github.com/ghbvf/gocell/pkg/errcode"`，但 `countingPublisher.Publish` 和 `failingPublisher.Publish` 都用 `errcode.New(...)`。
- **建议**：import 块添加 `"github.com/ghbvf/gocell/pkg/errcode"`。
- **Why it matters**：`go test -tags integration ./adapters/postgres/...` 编译失败，A3 测试矩阵完全无法运行。

### F-2 `VerifyExpectedVersion` "DB ahead of binary" 路径无集成测试

- **Cx 级别**：Cx2（P1）
- **维度**：测试
- **文件**：`adapters/postgres/schema_guard_integration_test.go`（缺失）
- **证据**：只测了 `actual < expected`（DBLagged），plan D1 明确要求两个方向都覆盖。
- **建议**：新增 `TestVerifyExpectedVersion_DBAhead_Integration`，手动 INSERT `version_id = expected + 1` 模拟 binary 回滚场景。
- **Why it matters**：binary 回滚场景（新版本部署失败→回退）无保护。

### F-3 `schema_guard_test.go` 缺少 `VerifyExpectedVersion` / `DetectInvalidIndexes` 单元测试

- **Cx 级别**：Cx2（P1）
- **维度**：测试
- **文件**：`adapters/postgres/schema_guard_test.go`
- **证据**：只有 `TestExpectedVersion_FromEmbedFS` / `TestExpectedVersion_SyntheticFS`，未覆盖核心对比逻辑。Plan Commit 1 要求 sqlmock 三个 case + DetectInvalidIndexes。
- **建议**：用 `fstest.MapFS` + sqlmock 覆盖 match/behind/ahead + invalid-indexes 两个分支。
- **Why it matters**：核心 fail-fast 逻辑无单元回归；覆盖率低于 CLAUDE.md 80% 要求。

### F-4 `BrokerDisconnectRetry` 测试未模拟真实 broker 断连

- **Cx 级别**：Cx2（P2）
- **维度**：测试 / 产品
- **文件**：`adapters/postgres/outbox_relay_integration_test.go:173-207`
- **证据**：用 `countingPublisher` mock 替代真实 RMQ 断连。
- **建议**：用 `rmqContainer.Stop(ctx)` + `Start(ctx)` 模拟，或注释说明技术限制并记 backlog。
- **Why it matters**：集成测试的价值在于测真实 broker；复制 mock 行为 ROI 打折。

### F-5 `TestExpectedVersion_FromEmbedFS` 注释过时

- **Cx 级别**：Cx1（P2）
- **维度**：DX
- **文件**：`adapters/postgres/schema_guard_test.go:22`
- **证据**：注释 "Currently 5 migrations (001-005); 006 will be added in T4" 过时（006 已在本 PR commit 3 添加）。
- **建议**：改为 "Currently 6 migrations (001-006)"，断言从 `>= 5` 收紧为 `>= 6` 或精确 `== 6`。
- **Why it matters**：宽松断言失去新增 migration 时的保护作用。

### F-6 `DetectInvalidIndexes` 查询不返回表名

- **Cx 级别**：Cx1（P2）
- **维度**：运维 / DX
- **文件**：`adapters/postgres/schema_guard.go:125-127`
- **证据**：只 `SELECT indexrelid::regclass::text`，缺 `indrelid::regclass::text`（所属表）。
- **建议**：查询扩展返回 `(index, table)`，日志同步含 table 字段。
- **Why it matters**：多 schema 环境 DBA 定位慢。

### F-7 `VerifyExpectedVersion` tableName 未经 `validateIdentifier`

- **Cx 级别**：Cx1（P2）
- **维度**：安全 / DX
- **文件**：`adapters/postgres/schema_guard.go:74-78`
- **证据**：`NewMigrator` 对 tableName 做 `validateIdentifier` 防注入，`VerifyExpectedVersion` 跳过校验。
- **建议**：加 `if err := validateIdentifier(tbl); err != nil { return err }`。
- **Why it matters**：当前调用方都用字面量风险低，但接口一致性差，未来外部输入引入注入风险。

### F-8 `CleanShutdownMidPublish` 验证的是 reclaimStale 路径，不是 Stop() 立即释放

- **Cx 级别**：Cx2（P2）
- **维度**：测试 / 产品
- **文件**：`adapters/postgres/outbox_relay_integration_test.go:322-374`
- **证据**：Stop 后等 ClaimTTL + 2*ReclaimInterval，测的是 TTL 超时恢复，与 plan "Stop 后 release 回 pending" 语义错位。`_ = relay.Stop(ctx)` 丢弃错误。
- **建议**：注释澄清"通过 reclaimStale TTL 恢复"；或加独立测试验证另一 relay 接管；Stop 错误 require.NoError。
- **Why it matters**：测试注释与行为错位，误导维护者。

### F-9 backlog 中 T7 未独立条目标注

- **Cx 级别**：Cx4（P2）
- **维度**：产品
- **文件**：`docs/backlog.md:61`
- **证据**：T7 的完成被合并到 `~~A4~~` 描述里，无独立行。
- **建议**：加一行 `~~T7~~ | ✅ CONFIG-VERSIONS-CONFIG-ID-INDEX ...`。
- **Why it matters**：后续 PG-REPO 大项可追溯前置依赖。

---

## 修复分流

| Finding | 优先级 | 修复建议 |
|---------|--------|----------|
| F-1 | P0 Cx1 | 立即修 import，否则 CI 阻塞 |
| F-2, F-3 | P1 Cx2 | 补齐 plan 要求的测试覆盖 |
| F-5 | P2 Cx1 | 1 行注释 + 断言更新 |
| F-7 | P2 Cx1 | 1 行 identifier 校验 |
| F-6 | P2 Cx1 | SQL 查询扩展 + 日志字段 |
| F-4, F-8 | P2 Cx2 | 注释澄清或 backlog 记债 |
| F-9 | Cx4 | backlog 条目拆分 |

**派发**：F-1 ~ F-8 交 developer agent（/fix 技能）处理。F-9 是文档微调，可合入同一修复 commit。

---

## 结论

**不可合并**。F-1 P0 导致 CI 编译失败；F-2/F-3 P1 缺失 plan 要求的测试覆盖。

下一步：dispatch developer agent 修复 Cx1 + Cx2（共 8 条），F-9 搭车修复。CI 通过后再评估。
