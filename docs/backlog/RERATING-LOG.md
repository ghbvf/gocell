# Backlog Re-rating Log

> 评级流水。规则真值源：[`RERATING-RUBRIC.md`](RERATING-RUBRIC.md)。

---

## Phase 0 — Rubric 落定 (2026-05-19)

- 新建 `docs/backlog/RERATING-RUBRIC.md`：三轴评估（完成性 / Cx / P）+ 5 阶段拆分 + 反模式清单
- `docs/backlog.md` 顶部加 rubric 引用
- commit: `99b973223` on branch `629-backlog-rerating`

---

## Phase 1 — cap-01 + cap-03 + cap-07 + cap-09 试评 (2026-05-19)

**扫描条目**：20（cap-01: 9 / cap-03: 3 / cap-07: 2 / cap-09: 6）

### 统计

| 维度 | 数量 |
|---|---|
| DONE 候选 | 4 |
| STALE re-scope | 1 |
| STALE-CLOSE | 1 |
| OPEN（含 P/Cx/Flag 调整）| 14 |

### DONE 候选（4）

| ID | cap | 证据 |
|---|---|---|
| P1-8 | cap-03 | `examples/iotdevice/cells/devicecell/slices/devicelist/` 完整 slice + contract + 4 个 test 全在；路径已从 `cells/` 迁到 `examples/iotdevice/cells/` |
| B2-T-04 | cap-03 | `contracts/event/user/created/v1/payload.schema.json:6-19` 全 camelCase；`grep '"UserID"\|"UserId"' contracts/ examples/*/contracts/` 命中 0 |
| PR320-FU-CONFIGCORE-CI-NOOP | cap-09 | `service_test.go:37-43` 明示 noop 隐式覆盖；newTestService 默认 NoopEmitter |
| PR238-FU8 | cap-09 | `config_repo_test.go:363-367,407-408` 双向 NotContains 锁就位（PR#553 ship `2dfaf50bf`） |

### STALE re-scope（1）

| ID | cap | re-scope |
|---|---|---|
| F-03 | cap-03 | 主前提失效：`pkg/contracts/` 已删 + Hard archtest `CONTRACTTEST-BOUNDARY-01` 守"legacy stay deleted"；re-scope 至 `PKG-CTXKEYS-NO-CELL-MODEL` 子项。P1/Cx2 → P3/Cx1，Flag → 🟠（触发条件：cell-model identifier 误入 pkg/ctxkeys） |

### STALE-CLOSE（1）

| ID | cap | 理由 |
|---|---|---|
| PR-CFG-A-DEFER-2 | cap-09 | config_entries vs feature_flags schema 差异是按 concept 设计（L1 flagwrite / L2 configpublish 有意分级），不是 bug，前提失效无残留 |

### P 升级（2）

| ID | cap | 原 P → 新 P | 命中维度 |
|---|---|---|---|
| C-04 | cap-01 | P2 → P1 | 架构 + 去重 + 抽象 + 触及 ≥ 3 cell + 吸收 C-09 |
| CONFIGREPO-OP-LABEL-TYPED-ENUM-HARD-01 | cap-09 | P3 → P2 | AI-rebust Soft → Hard 升级路径（charter mandate） |

### P 降级（3）

| ID | cap | 原 P → 新 P | 降级信号 |
|---|---|---|---|
| C-06 | cap-01 | P2 → P3 | doc/决策类 + 无 outcome + 无业务推动 |
| PR341-FU-OUTBOXTEST-CLOSE-BUDGET-COVERAGE | cap-07 | P2 → P3 | 纯 test 触发型 + 剩余 3 处是测 Close 语义本身 |
| B2-A-33 | cap-09 | P2 → P3 | 触发型（sentinel 部署）+ 无业务推动 |

### Flag 调整（2）

| ID | cap | 原 → 新 | 理由 |
|---|---|---|---|
| B2-PROVISIONER-MUTEX-REVIEW | cap-01 | 🟠 → 🟡 | trigger 已达成（PR #482 ship + PR #628 in-flight） |
| RBACASSIGN-L2-PG-ATOMICITY-01 | cap-07 | 🟠 → 🟡 | trigger X1 PG accesscore 仓储已落地（role_repo.go + user_repo.go） |

### OPEN 维持原档（11）

- cap-01：G-10 / SWEEPER / PR441-FU-METADATA / SEALED-MARKER-BUNDLE / CONTROL-PLANE-CLOCK（5）
- cap-03：—（全部 DONE/STALE）
- cap-07：（含上面 Flag 调整）
- cap-09：CONFIGCORE-RECEIVE-PLACEHOLDER（1）

### 规则边界发现（不回改 rubric，记录待 P2 末复盘）

1. **bundle 内子条 DONE 处理**：SEALED-MARKER-DEFENSE-EXPANSION-BUNDLE 内子条 A.5 `SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01` 已落地（`pkg/scaffoldid/scaffoldid.go` 存在 + `cmd/gocell/app/scaffoldid_helpers_test.go:14` 引用）；但 bundle 描述编辑受 rubric §6.1 限制。本 PR 不触碰 bundle 描述，待后续 SEALED-MARKER bundle 整批收口 PR 同步减条。
2. **DUP-like merge 形态**：C-09 已声明"并入 C-04"但保留独立行 + 自己的 P/Cx/Flag。rubric §2 DUP 定义不完全匹配此形态（C-09 残留细节未全部进 C-04 描述）。本 PR 维持 C-09 独立，待 C-04 ship 时一并归档。
3. **试评成功率**：20 条全部一次走通，无误判回滚。规则可推广到 P2 60 条。

### Commit

`<待 commit>` — 13 处 row 编辑（不动 ID/描述主体；F-03 描述按 rubric §6.1 STALE re-scope 允许重写）
