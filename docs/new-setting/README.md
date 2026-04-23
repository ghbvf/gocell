# Rules 拆分方案（渐进式披露）

## 目录结构

```
docs/new-setting/
├── CLAUDE.md                   # 根规则（精简，始终加载）
├── kernel/CLAUDE.md            # kernel 层规则（进入 kernel/ 时加载）
├── runtime/CLAUDE.md           # runtime 层规则
├── adapters/CLAUDE.md          # adapters 层规则
├── cells/CLAUDE.md             # cells 层规则
├── pkg/CLAUDE.md               # pkg 层规则
├── contracts/CLAUDE.md         # contracts 层规则
└── .claude/rules/
    ├── go-standards.md         # 精简版（仅跨层通用命名+安全）
    ├── eventbus.md             # 精简版（仅 Handler/幂等/DLX，EventRouter 移入 runtime/）
    ├── observability.md        # 不变
    ├── error-handling.md       # 不变
    └── api-versioning.md       # 不变（paths 收窄到 http + cells + cmd）
```

**删除**（内容已迁入层 CLAUDE.md）：
- `.claude/rules/gocell/runtime-api.md` → `runtime/CLAUDE.md`
- `.claude/rules/gocell/cell-patterns.md` → `cells/CLAUDE.md` + `contracts/CLAUDE.md`

## 迁移对照表

| 原位置 | 原内容 | 新位置 |
|--------|--------|--------|
| 根 CLAUDE.md | 架构目录 + 层间依赖表 | 根 CLAUDE.md（精简为摘要表） |
| 根 CLAUDE.md | 编码规范 | `go-standards.md`（已有，保留跨层部分） |
| 根 CLAUDE.md | 元数据规范（cell.yaml/slice.yaml） | `kernel/CLAUDE.md` |
| 根 CLAUDE.md | CLI 工具 | `kernel/CLAUDE.md` |
| `go-standards.md` | 层间依赖表 | 各层 `CLAUDE.md` |
| `go-standards.md` | DDD 分层规则 | `cells/CLAUDE.md` |
| `go-standards.md` | L0-L4 表 + 各级测试要求 | `kernel/CLAUDE.md`（定义）+ `cells/CLAUDE.md`（测试要求） |
| `go-standards.md` | 数据库迁移规则 | `adapters/CLAUDE.md` |
| `go-standards.md` | GoCell 元数据文件规范 | `kernel/CLAUDE.md` |
| `go-standards.md` | 命名规范 + 安全检查点 | `go-standards.md`（保留，跨层通用） |
| `runtime-api.md` | Auth 路由声明（F3）+ FinalizeAuth | `runtime/CLAUDE.md` |
| `runtime-api.md` | Composition Root 模式 | `runtime/CLAUDE.md` |
| `cell-patterns.md` | DTO 边界 + Init fail-fast + Contract test | `cells/CLAUDE.md` |
| `cell-patterns.md` | contractUsages.role 对照 | `cells/CLAUDE.md` + `contracts/CLAUDE.md` |
| `eventbus.md` | EventRouter + RegisterSubscriptions | `runtime/CLAUDE.md` |
| `eventbus.md` | Handler 实现 + 幂等 + DLX | `eventbus.md`（保留） |

## 渐进式披露效果

| 工作场景 | 现在加载 | 拆分后加载 |
|---------|---------|----------|
| 改 `kernel/crypto/*.go` | 根 CLAUDE + 7 个 rules | 根 CLAUDE（精简）+ `kernel/CLAUDE.md` + go-standards + observability |
| 改 `runtime/auth/*.go` | 同上全量 | 根 CLAUDE + `runtime/CLAUDE.md` + go-standards + observability + error-handling |
| 改 `cells/accesscore/` | 同上全量 | 根 CLAUDE + `cells/CLAUDE.md` + eventbus + error-handling + api-versioning |
| 改 `adapters/postgres/` | 同上全量 | 根 CLAUDE + `adapters/CLAUDE.md` + go-standards + observability |
| 改 `pkg/errcode/` | 同上全量 | 根 CLAUDE + `pkg/CLAUDE.md` + go-standards |
| 改 `contracts/` | 同上全量 | 根 CLAUDE + `contracts/CLAUDE.md` |

## 应用步骤

1. 将各层 `CLAUDE.md` 复制到对应的真实目录（`kernel/CLAUDE.md`、`runtime/CLAUDE.md` 等）
2. 用 `.claude/rules/` 下的精简版替换现有对应文件
3. 删除 `.claude/rules/gocell/runtime-api.md` 和 `.claude/rules/gocell/cell-patterns.md`
4. 精简根 `CLAUDE.md`
