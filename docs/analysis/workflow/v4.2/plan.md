# v4.2 优化计划（修订版）

## Context

v4.1 已部署。两份外部审查 + 用户 5 个诉求 + 四角色团队分析已完成。用户在计划审批时提出 8 点修正意见，本计划据此全面修订。

核心原则变更：
- ~~延迟做~~ → **该做就做，不留"以后再说"**
- 结构化验证不能取消，用 `phase-gates.yaml` 做准入准出数据定义
- 渐进式披露按 Claude 官方架构：rules(全局) + skills(按阶段) + agents(按角色)

---

## 用户 8 点反馈的处理

| # | 反馈 | 处理 |
|---|------|------|
| 1 | 该优化的就优化，不要留 | 所有"延迟"项重新评估，能做就做 |
| 2 | 取消 YAML/JSON 后缺结构化验证 | 恢复 `phase-gates.yaml` 作为准入准出数据源，phase-lint 消费它 |
| 3 | Playwright trace/video 要做 | 加入计划 |
| 4 | 契约先行 QA 要做 | 加入计划 |
| 5 | Review 上下文注入 ≠ 双盲验证 | 只做上下文注入（git diff + spec + kernel-constraints），不做双盲 |
| 6 | 渐进式披露位置错误 | 按官方架构：rules=全局规则，skills=阶段工作流，agents=角色定义 |
| 7 | 产出物分级：每阶段都要硬性产出 | 取消"可选产出"，每阶段明确硬性产出清单 |
| 8 | 修正 5 和修正 9 没协调 | 合并为一次 CLAUDE.md 重写 |

---

## 修改清单（10 项）

### 1. 创建 phase-gates.yaml + phase-gate-check.sh

**新建**: `.claude/skills/phase-gate/phase-gates.yaml`

声明式准入准出规则，phase-lint 脚本消费此文件：

```yaml
stages:
  S0:
    exit:
      required_files: [phase-charter.md, role-roster.md, product-context.md]
  S1:
    entry:
      required_files: [phase-charter.md, role-roster.md, product-context.md]
    exit:
      required_files: [spec.md, checklists/requirements.md]
      content_checks:
        - file: spec.md
          pattern: "(文档|DevOps|E2E|Playwright|测试)"
  S2:
    entry:
      required_files: [spec.md]
    exit:
      required_files: [kernel-constraints.md]
  S3:
    entry:
      required_files: [kernel-constraints.md]
    exit:
      required_files: [decisions.md]
  S4:
    entry:
      required_files: [decisions.md]
    exit:
      required_files: [plan.md, tasks.md, product-acceptance-criteria.md]
      content_checks:
        - file: tasks.md
          pattern: "(Playwright|E2E|测试编写)"
        - file: tasks.md
          pattern: "(Docker|docker-compose|部署)"
        - file: tasks.md
          pattern: "(OpenAPI|文档)"
  S5:
    entry:
      required_files: [tasks.md, product-acceptance-criteria.md]
    exit:
      content_checks:
        - file: tasks.md
          check: no_unchecked_tasks
  S6:
    exit:
      required_files: [review-findings.md, tech-debt.md]
  S7:
    exit:
      required_files: [qa-report.md, user-signoff.md]
  S8:
    entry:
      required_files: [qa-report.md, tech-debt.md]
    exit:
      required_files: [kernel-review-report.md, product-review-report.md, phase-report.md]
```

**新建**: `.claude/skills/phase-gate/scripts/bash/phase-gate-check.sh`

- 读取 phase-gates.yaml（用 python3 解析或 grep/sed 提取）
- 检查文件存在性 + 非空 + content_checks
- 输出 PASS/FAIL + 缺失清单
- 写审计日志 `specs/{branch}/gate-audit.log`

### 2. 去除副总负责人 + phase-lint 替代

**修改**: `docs/workflow-detailed.md`

- Core Mandatory: 5 → 4 角色（移除副总负责人）
- S0 执行者: 总负责人 + 产品经理
- S0 步骤 5 "副总负责人复核" → `phase-gate-check.sh --stage S0 --check exit`
- S6.3 "副总负责人复核" → 合并到总负责人裁决
- S8.4 "副总负责人合并前复核" → `phase-gate-check.sh --stage S8 --check exit`
- 每个阶段出口条件统一增加: `phase-gate-check.sh --stage SN --check exit = PASS`

**修改**: `CLAUDE.md` — 同步移除副总负责人，增加 phase-lint 强制指令

### 3. 重新设计 S7.2 三视角使用者验证

**修改**: `docs/workflow-detailed.md` S7 部分

S7.2 改为三视角结构化操作：

```
7.2 使用者验证（按三个 Persona 视角顺序执行）:

    视角 A — PM（浏览器 UI 全流程）:
    A1. 项目列表页加载 → 3 秒内渲染 + 表头含义清晰
    A2. 创建项目 → 成功反馈 + 列表自动刷新
    A3. 点击项目 → 导航到 Runs 页 + URL 含 project_id
    A4. 审批中心 → 过滤器可用 + 空状态有意义
    A5. 导航栏切换 → 三入口可用 + 当前位置高亮
    A6. 整体: PM 能否通过 UI 回答"项目状态如何"

    视角 B — 开发者（UI + API 混合）:
    B1. 提交 Run 表单 → 字段有标记 + 必填项提示
    B2. API POST /api/v1/runs → 201 + run_id + < 1s
    B3. Run 列表 → 新 Run 出现 + SSE 状态实时更新
    B4. 任务详情 → 元数据卡片 + 时间线
    B5. SSE 连接状态指示器 → 颜色/文案一致
    B6. 访问不存在的 task → 404 清晰 + 返回入口

    视角 C — Vibe Coder（纯 API）:
    C1. GET /health → 200
    C2. POST /projects → 信封格式 {"data":{...}}
    C3. GET /projects?page=1&pageSize=5 → data/total/page/pageSize
    C4. GET /events/stream → SSE + text/event-stream
    C5. POST 不存在的审批 → 标准错误格式
    C6. 跨端点分页格式一致性
    C7. 整体: API 能否支撑脚本化自动化

    每视角评分 1-5:
    1=不可用 2=有明显摩擦 3=可接受 4=流畅 5=优秀

    产出: specs/{branch}/user-signoff.md
    判定: APPROVE(均≥4无P0) / CONDITIONAL(均≥3) / REJECT(任一<3或P0)
```

S8.2 产品经理输入增加 `user-signoff.md`，维度 B 引用三视角评分。

### 4. Playwright trace/video 证据采集

**修改**: `docs/workflow-detailed.md` S5 + S7 部分

S5 QA 编写测试时：
- playwright.config.ts 默认开启 `trace: 'on'`, `video: 'on-first-retry'`, `screenshot: 'only-on-failure'`

S7.1 QA 执行测试后：
- 证据存放: `specs/{branch}/evidence/playwright/` (trace + screenshots)
- qa-report.md 每条结论必须引用证据路径

**新增 S7 出口条件**: `evidence/playwright/` 目录非空

### 5. 契约先行 QA

**修改**: `docs/workflow-detailed.md` S5 部分

S5 batch 划分规则增加：
```
对含 API 变更的 batch，强制执行顺序:
1. 后端开发者先产出 OpenAPI 片段（contracts/ 目录更新）
2. QA 自动化基于 contract 编写/更新测试
3. 后端/前端实现业务逻辑
4. batch 末尾集成运行测试
```

S5 出口条件增加: 本 Phase 新增的 API endpoint 在 contracts/ 或 OpenAPI spec 中有对应定义

### 6. S6 Review 上下文注入强化（不是双盲）

**修改**: `docs/workflow-detailed.md` S6 部分

S6.1 Reviewer prompt 强制注入:
- `kernel-constraints.md`（已有）
- `git diff main...{branch} --stat`（新增：变更范围概览）
- `specs/{branch}/spec.md` 最新版（新增：需求对照基准）
- review-findings.md 增加 "审查基准版本" 字段（commit hash）

不做双盲交叉验证——当前阶段 P0 误报率未知，无数据支撑。

### 7. 渐进式披露（按 Claude 官方架构）

按 Claude Code 官方文档，正确的位置分配：

| 内容类型 | 位置 | 加载时机 |
|---------|------|---------|
| 全局规则（工具使用、命名、技术栈） | `CLAUDE.md` ~35 行 | 每次会话 |
| Go 编码规范（DDD、一致性） | `.claude/rules/go-standards.md` (paths: src/**/*.go) | 操作 Go 文件时 |
| 工作流阶段指令 | `.claude/skills/stage-N-*/SKILL.md` (9 个 skill) | 总负责人 `/stage-0` 触发 |
| 角色定义 | `.claude/agents/product-manager.md` 等 (6 个 agent) | 派发子 Agent 时 |
| 文档职责矩阵 + 验收清单 | `.claude/skills/phase-gate/SKILL.md` | `/phase-gate` 触发 |

**CLAUDE.md 精简为 ~35 行**:
```
# 协作说明
## 必须遵守的工作方式（5 条规则）
## 文档命名规则
## Active Technologies
## Recent Changes
## 阶段门强制检查
总负责人在进入下一阶段前必须运行 phase-gate-check.sh。
## 多角色工作流
完整定义: docs/workflow-detailed.md
按阶段调用: /stage-0 ... /stage-8
按角色派发: Agent(name=product-manager) 等
```

**新建 9 个 Stage Skills**:
```
.claude/skills/
  stage-0-init/SKILL.md
  stage-1-specify/SKILL.md
  stage-2-review/SKILL.md
  stage-3-decide/SKILL.md
  stage-4-plan/SKILL.md
  stage-5-implement/SKILL.md
  stage-6-review-fix/SKILL.md
  stage-7-qa/SKILL.md
  stage-8-close/SKILL.md
  phase-gate/SKILL.md
```

每个 Skill 包含该阶段的完整操作步骤、准入准出条件、角色分配。

**新建 6 个 Agent 定义**:
```
.claude/agents/
  product-manager.md
  project-manager.md
  kernel-guardian.md
  reviewer.md
  doc-engineer.md
  devops.md
```

每个 Agent 定义包含 YAML frontmatter（name/description/tools/model/skills）+ 角色系统 prompt。

**移动 Go 规则**:
- 从 CLAUDE.md 移到 `.claude/rules/go-standards.md`
- 添加 `paths: ["src/**/*.go"]` frontmatter
- 只在操作 Go 代码时加载

### 8. 产出物——每阶段硬性产出（无可选）

**修改**: `docs/workflow-detailed.md`

每阶段的硬性产出清单（全部阻塞阶段门）:

| 阶段 | 硬性产出（phase-gate-check 检查） |
|------|--------------------------------|
| S0 | phase-charter.md, role-roster.md, product-context.md |
| S1 | spec.md, checklists/requirements.md |
| S2 | kernel-constraints.md |
| S3 | decisions.md |
| S4 | plan.md, tasks.md, product-acceptance-criteria.md |
| S5 | tasks.md 全 [x], e2e/*.spec.ts |
| S6 | review-findings.md, tech-debt.md |
| S7 | qa-report.md, user-signoff.md, evidence/playwright/ |
| S8 | kernel-review-report.md, product-review-report.md, phase-report.md |

取消"可选产出"概念。如果某阶段确实不需要某文件（如纯后端无 UI），在 phase-charter.md 的 "Scope 声明" 中标注 N/A 理由，phase-gate-check 读取此声明跳过检查。

### 9. CLAUDE.md 重写（合并修正 5 + 修正 9）

**修改**: `CLAUDE.md`

一次性完成：
- 精简到 ~35 行（移出工作流、Go 规则、角色清单、验收清单）
- 修正文档漂移（架构师统一为 Core Mandatory，Roadmap 规划师统一分类）
- 增加 phase-lint 强制指令
- 增加工作流引用 `@see docs/workflow-detailed.md`
- 移除副总负责人

### 10. workflow-detailed.md 全量更新

**修改**: `docs/workflow-detailed.md`

一次性包含所有变更：
- 去除副总负责人（Core Mandatory 4 角色）
- 每阶段出口增加 phase-gate-check.sh
- S5 契约先行 QA 规则
- S6 Review 上下文注入强化
- S7.2 三视角使用者验证
- S7 Playwright trace/video
- S8.2 产品经理输入增加 user-signoff.md
- 产出物全部硬性（无可选）
- 跳过警告枚举化
- CHANGELOG 自动化（文档工程师 prompt 加 git log）
- 修正漂移（架构师/Roadmap 统一分类）
- 历史教训表 + 改进时间线更新

---

## 新建文件清单

| 文件 | 类型 | 内容 |
|------|------|------|
| `.claude/skills/phase-gate/phase-gates.yaml` | 数据 | 准入准出规则声明 |
| `.claude/skills/phase-gate/scripts/bash/phase-gate-check.sh` | 脚本 | 阶段门检查器 |
| `.claude/skills/stage-0-init/SKILL.md` | Skill | S0 完整操作指令 |
| `.claude/skills/stage-1-specify/SKILL.md` | Skill | S1 完整操作指令 |
| `.claude/skills/stage-2-review/SKILL.md` | Skill | S2 完整操作指令 |
| `.claude/skills/stage-3-decide/SKILL.md` | Skill | S3 完整操作指令 |
| `.claude/skills/stage-4-plan/SKILL.md` | Skill | S4 完整操作指令 |
| `.claude/skills/stage-5-implement/SKILL.md` | Skill | S5 完整操作指令 |
| `.claude/skills/stage-6-review-fix/SKILL.md` | Skill | S6 完整操作指令 |
| `.claude/skills/stage-7-qa/SKILL.md` | Skill | S7 完整操作指令 |
| `.claude/skills/stage-8-close/SKILL.md` | Skill | S8 完整操作指令 |
| `.claude/skills/phase-gate/SKILL.md` | Skill | 阶段门检查 Skill |
| `.claude/agents/product-manager.md` | Agent | 产品经理角色定义 |
| `.claude/agents/project-manager.md` | Agent | 项目经理角色定义 |
| `.claude/agents/kernel-guardian.md` | Agent | Kernel Guardian角色定义 |
| `.claude/agents/reviewer.md` | Agent | 6 席位 Reviewer 定义 |
| `.claude/agents/doc-engineer.md` | Agent | 文档工程师角色定义 |
| `.claude/agents/devops.md` | Agent | DevOps 角色定义 |
| `.claude/rules/go-standards.md` | Rule | Go 编码规范（path-scoped） |

## 修改文件清单

| 文件 | 操作 |
|------|------|
| `CLAUDE.md` | 重写（174行→~35行） |
| `docs/workflow-detailed.md` | 全量更新 |

---

## 验证方式

1. `bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S0 --branch test --check exit` 输出 FAIL + 缺失清单
2. `wc -l CLAUDE.md` < 40
3. `grep -c "副总负责人" docs/workflow-detailed.md CLAUDE.md` = 0（仅历史教训表中保留）
4. `ls .claude/skills/stage-*/SKILL.md | wc -l` = 9
5. `ls .claude/agents/*.md | wc -l` = 6
6. `grep "trace:" examples/{project}/ui/playwright.config.ts` 确认 trace 配置
7. `grep "phase-gate-check" docs/workflow-detailed.md` 每阶段出口都有
8. S7.2 包含"视角 A""视角 B""视角 C"三个完整操作序列
9. `cat .claude/skills/phase-gate/phase-gates.yaml` 每阶段都有 required_files
10. `.claude/rules/go-standards.md` 含 `paths:` frontmatter
