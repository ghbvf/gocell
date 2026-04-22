# Review Plan 1: 按 PR 逐一并行 Review

> 从 PR#7 开始，每个 PR 作为独立 review 单元，并行执行。每个 PR 启动 4-6 个子 agent。
>
> **设计原则**: PR#31 是 Phase 3 的集成合并入口（22 commits, 包含 PR#7~#17 + Wave 4 全部内容），
> 单次审查难以发现细节问题。因此保留所有子 PR（含已关闭的 #28/#29/#30）作为独立 review 单元，
> PR#31 仅做集成级审查（合并冲突、commit 顺序、遗漏内容）。

## PR 关系说明

```
Phase 3 子 PR（全部 → feat/002-phase3-adapters 集成分支）:
  PR#7  (W0) bootstrap interface injection      ✅ MERGED
  PR#8  (W0) crypto/rand UUID                   ✅ MERGED
  PR#9  (W1) Docker Compose + Makefile           ✅ MERGED
  PR#10 (W1) PostgreSQL adapter                  ✅ MERGED
  PR#11 (W1) Redis adapter                       ✅ MERGED
  PR#12 (W1) RabbitMQ adapter                    ✅ MERGED
  PR#14 (W2) Outbox + OIDC + S3 + WebSocket     ✅ MERGED
  PR#15 (W3) cells rewire + product fixes        ✅ MERGED
  PR#16 (W3) security hardening RS256            ✅ MERGED
  PR#17 (W3) kernel lifecycle + governance       ✅ MERGED
  PR#28 (W4) integration test stubs              ❌ CLOSED (内容直接进集成分支)
  PR#29 (W4) doc.go + guides                     ❌ CLOSED (内容直接进集成分支)
  PR#30 (W4) W4 合并版 (#28+#29+kg-verify)       ❌ CLOSED (内容直接进集成分支)
                    │
                    ▼
  PR#31 feat/002-phase3-adapters → develop       ✅ MERGED (22 commits, 集成入口)

Phase 4 (当前 open):
  PR#32 feat/003-phase4-examples-docs → develop  🟡 OPEN (23 commits)
```

> **#28/#29/#30 虽已关闭，其代码已进入集成分支并通过 #31 合并。**
> 保留它们作为 review 单元的原因：拆分审查粒度，避免 #31 过大遗漏问题。
> Review 时基于这些 PR 的变更文件列表读取当前代码（非 diff），确保审查的是最终状态。

## 覆盖范围

| Batch | PR | 状态 | Title | 变更量 | 关键文件 |
|-------|-----|------|-------|--------|----------|
| A | #7 | MERGED | bootstrap interface injection + outbox doc | 3 files, +99/-19 | runtime/bootstrap, kernel/outbox |
| A | #8 | MERGED | crypto/rand UUID + seed script | 11 files, +145/-19 | pkg/id, pkg/uid, cells/*/service.go |
| A | #9 | MERGED | Docker Compose + Makefile + healthcheck | 4 files, +160/0 | docker-compose.yml, Makefile |
| B | #10 | MERGED | PostgreSQL adapter | 17 files, +1485/-1 | adapters/postgres/* |
| B | #11 | MERGED | Redis adapter | 13 files, +1242/0 | adapters/redis/* |
| B | #12 | MERGED | RabbitMQ adapter | 9 files, +2022/0 | adapters/rabbitmq/* |
| C | #14 | MERGED | Wave 2 — Outbox, OIDC, S3, WebSocket | 35 files, +4199/0 | adapters/oidc/*, adapters/s3/*, adapters/websocket/*, adapters/postgres/outbox_* |
| D | #15 | MERGED | cells: outbox.Writer rewire + product fixes | 24 files, +951/-133 | cells/access-core/*, cells/audit-core/*, cells/config-core/* |
| D | #16 | MERGED | security hardening — RS256, trustedProxies | 13 files, +975/-79 | runtime/auth/*, runtime/http/middleware/* |
| D | #17 | MERGED | kernel: lifecycle LIFO + BaseCell mutex | 22 files, +523/-52 | kernel/*, pkg/errcode, runtime/* |
| E | #28 | CLOSED | W4: integration test stubs + coverage | 20+ files | adapters/*/integration_test.go, cmd/gocell/* |
| E | #29 | CLOSED | W4: doc.go + guides | 19 files | adapters/*/doc.go, runtime/*/doc.go, docs/guides/* |
| E | #30 | CLOSED | W4 合并: tests + docs + kg-verify | 20+ files | 上述合集 + scripts/kg-verify.sh |
| F | #31 | MERGED | **Phase 3 集成入口** (22 commits) | 全量 | **集成级审查**（非逐文件） |
| G | #32 | OPEN | Phase 4: Examples + Docs (23 commits) | 70 files, +5446/-183 | examples/*, cells/device-cell/*, cells/order-cell/*, tests/integration/* |

## 执行策略

### 每个 PR 启动的子 Agent 角色

**标准 PR（#7~#17, #28~#30, #32）— 每个 PR 6 agents:**

| # | Agent 角色 | subagent_type | 职责 |
|---|-----------|---------------|------|
| 1 | **架构合规** | `architect` | 分层依赖规则、接口稳定性、Cell 边界 |
| 2 | **安全审查** | `reviewer` | OWASP Top 10、密钥管理、注入风险、错误信息泄露 |
| 3 | **测试覆盖** | `reviewer` | 覆盖率 ≥80%/90%(kernel）、table-driven、边界条件 |
| 4 | **Kernel 守卫** | `kernel-guardian` | 元数据合规、契约完整性、分层隔离验证 |
| 5 | **编码规范** | `reviewer` | errcode 使用、slog 规范、命名规则、认知复杂度 |
| 6 | **产品验收** | `product-manager` | 功能完整性、API 版本策略、响应格式、用户体验 |

**集成 PR（#31）— 4 agents（不做逐文件审查，聚焦集成问题）:**

| # | Agent 角色 | subagent_type | 职责 |
|---|-----------|---------------|------|
| 1 | **集成架构** | `architect` | 22 commits 的合并顺序、跨 PR 接口兼容性、是否有 PR 间的冲突遗漏 |
| 2 | **Kernel 守卫** | `kernel-guardian` | 集成后的全局元数据一致性、契约完整性 |
| 3 | **回归检查** | `reviewer` | 对比 #28/#29/#30 的预期内容 vs #31 最终状态，检查是否有遗漏 |
| 4 | **产品完整性** | `product-manager` | Phase 3 scope 覆盖度、spec 对齐 |

### 并行分批（Batch）

按依赖关系 + PR 类型分 7 个 Batch，每个 Batch 内的 PR 并行执行：

```
Batch A (W0 基础设施):   PR#7 + PR#8 + PR#9                → 18 agents 并行
                         ↓ 完成后
Batch B (W1 适配器):     PR#10 + PR#11 + PR#12             → 18 agents 并行
                         ↓ 完成后
Batch C (W2 适配器):     PR#14                              → 6 agents 并行
                         ↓ 完成后
Batch D (W3 上层整合):   PR#15 + PR#16 + PR#17             → 18 agents 并行
                         ↓ 完成后
Batch E (W4 测试+文档):  PR#28 + PR#29 + PR#30             → 18 agents 并行
                         ↓ 完成后
Batch F (Phase 3 集成):  PR#31                              → 4 agents 并行
                         ↓ 完成后
Batch G (Phase 4):       PR#32                              → 6 agents 并行
```

### 已关闭 PR 的 Review 策略

PR#28/#29/#30 已关闭但内容已进入代码库。Review 方式：

1. **以变更文件列表为线索** — 使用 `gh pr view {N} --json files` 获取该 PR 涉及的文件列表
2. **读取当前代码** — 不看 PR diff（已关闭无法获取），直接读取这些文件的最终状态
3. **对比预期** — PR description 中声明的 tasks（T70-T90）是否全部体现
4. **标记差异** — 如果最终代码与 PR 预期不一致（被后续 commit 覆盖/修改），记录为 finding

### 单个 PR Review 的 Agent Prompt 模板

每个 agent 收到的 prompt 包含：

```
你正在 review GoCell 项目的 PR#{N}: "{title}"。

**变更文件列表**: {files}
**review 视角**: {role_description}

请逐文件审查，输出格式：
## 发现 (Findings)
| 严重级别 | 文件:行号 | 问题描述 | 建议修复 |
|---------|----------|---------|---------|
| P0/P1/P2 | path:line | ... | ... |

## 亮点 (Highlights)
- 值得肯定的设计决策

## 风险评估
- 整体风险: LOW / MEDIUM / HIGH
- 阻塞合并: YES / NO
```

### 汇总输出

每个 Batch 完成后，汇总所有 agent 输出到：
- `specs/feat/003-phase4-examples-docs/review-plan1-batch-{A..E}-findings.md`

最终汇总到：
- `specs/feat/003-phase4-examples-docs/review-plan1-summary.md`

## 执行命令参考

```bash
# Batch A 示例 — 同时启动 3 个 PR 的 review，每个 PR 6 个 agent
# PR#7: 6 agents (architect, reviewer×3, kernel-guardian, product-manager)
# PR#8: 6 agents
# PR#9: 6 agents (DevOps PR 用 devops agent 替换 kernel-guardian)
```

## 预期产出

| 产出 | 格式 |
|------|------|
| 每 PR review 报告（标准 PR 6 份，集成 PR 4 份） | Markdown table |
| 每 Batch 汇总 | 按 P0/P1/P2 去重合并 |
| 最终 summary | 全量 findings + 统计 + 建议 action items |
| 预计总 agent 数 | **94** (13 标准 PRs × 6 agents + 1 集成 PR × 4 agents + 汇总 12) |

### Agent 数量明细

| Batch | PR 数 | Agents/PR | 小计 |
|-------|--------|-----------|------|
| A (W0) | 3 | 6 | 18 |
| B (W1) | 3 | 6 | 18 |
| C (W2) | 1 | 6 | 6 |
| D (W3) | 3 | 6 | 18 |
| E (W4) | 3 | 6 | 18 |
| F (集成) | 1 | 4 | 4 |
| G (Phase 4) | 1 | 6 | 6 |
| 汇总 | — | — | 7 (每 Batch 1 个汇总 + 最终 1 个) |
| **Total** | **15** | — | **95** |
