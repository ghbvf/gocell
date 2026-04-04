---
name: stage-5-implement
description: "并行实施: 契约先行QA+项目经理batch划分+指令重注入+开发者自报+QA编写测试+Playwright配置"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Skill, Agent]
---

# 阶段 5: 并行实施

**执行者**: 项目经理规划 batch + 总负责人派发 + 项目经理跟踪

**入口条件**: S4 出口通过（tasks.md + product-acceptance-criteria.md 存在）

---

## 契约先行 QA 规则

对含 API 变更的 batch，强制执行以下顺序：

```
1. 后端开发者先产出 OpenAPI 片段（contracts/ 目录更新）
2. QA 自动化基于 contract 编写/更新测试
3. 后端/前端实现业务逻辑
4. batch 末尾集成运行测试
```

项目经理在 batch 划分时必须识别含 API 变更的 batch，并在 batch 方案中标注契约先行顺序。

## Playwright 配置要求

QA 在 S5 编写测试时，`playwright.config.ts` 必须开启以下配置：

```typescript
// playwright.config.ts 必须包含:
trace: 'on',                    // 始终录制 trace
video: 'on-first-retry',        // 首次重试时录制视频
screenshot: 'only-on-failure',  // 失败时截图
```

---

## 操作步骤（实施循环）

### 5.1 项目经理: Batch 划分

**入口条件**: task-dependency-analysis.md 存在（如不存在，使用 tasks.md 内嵌依赖图）

1. 基于 `specs/{branch}/task-dependency-analysis.md` 分析 tasks.md 依赖关系
2. 划分当前可并行的 batch
3. 对含 API 变更的 batch，标注契约先行顺序
4. 提交 batch 方案给总负责人

**指令重注入**（防止 Agent Drift）:
- 重述 Phase 目标（来自 phase-charter.md）
- 重述 kernel-constraints.md 中的关键约束
- 重述 product-acceptance-criteria.md 中的 P1 AC

### 5.2 总负责人: 审批 + 派发

审批 batch 方案后，派发 `Agent(run_in_background=true)` x N：

```
- 后端开发者: Go 代码任务
- 前端开发者: Vue 任务
- 文档工程师: 文档任务（OpenAPI spec + 用户文档 + 部署文档）
- DevOps: 部署配置任务（Docker/CI + Playwright 安装 + 种子数据）
- QA 自动化: Playwright E2E 测试编写/更新
```

**开发者 Agent 要求**:
- 完成任务后自行运行 `go build` + `go test` 并在返回结果中报告 PASS/FAIL
- 每个 Agent 完成后产出 **handoff note**: 含 build/test 结果 + 变更摘要 + 已知风险

**QA Agent 要求**:
- 基于 `product-acceptance-criteria.md` 中标注 `[E2E 测试]` 的 AC 编写测试
- 对含 API 变更的 batch，先等待 contracts/ 更新后再编写测试
- 确认 playwright.config.ts 含 trace/video/screenshot 配置

### 5.3 等待: batch 中所有 agent 完成

### 5.4 项目经理: 验证与跟踪

1. 读取每个开发者 Agent 返回结果中的 build/test PASS/FAIL 状态（不亲自运行）
2. 标记 `tasks.md` 中已完成任务 `[x]`
3. 报告:
   - 完成数/总数
   - 阻塞项
   - 下一 batch 就绪状态
   - FAIL 的 Agent 需重新派发

### 5.5 判断

- 还有未完成任务 → 回到 5.1
- 全部完成 → 进入阶段 6

### 5.6 阶段门检查

```bash
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S5 --check exit
```

---

## 硬性产出物

| 文件/产出 | 路径 | 责任角色 |
|----------|------|---------|
| 代码实现 | `src/` | 后端/前端开发者 |
| OpenAPI spec | `contracts/` 或 `docs/` | 文档工程师 |
| 部署配置 | `docker-compose*.yml` 等 | DevOps |
| E2E 测试 | `examples/{project}/ui/e2e/*.spec.ts` | QA 自动化 |
| 种子数据 | `seed-test-data.sh` 或 Go test helper | 后端开发者/DevOps |
| tasks.md | 全部标记 `[x]` | 项目经理 |
| Handoff notes | 每 batch 每 Agent | 开发者们 |

## 出口条件

```
[ ] tasks.md 所有任务标记 [x]
[ ] 开发者 Agent 报告 build + test 全绿
[ ] e2e/*.spec.ts 存在（QA 已编写测试）
[ ] 本 Phase 新增的 API endpoint 在 contracts/ 或 OpenAPI spec 中有对应定义
[ ] playwright.config.ts 含 trace: 'on' 配置
[ ] phase-gate-check.sh --stage S5 --check exit = PASS
```

**约束**: 只执行 tasks.md 中的任务，不自行添加。
