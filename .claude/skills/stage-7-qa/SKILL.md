---
name: stage-7-qa
description: "QA+使用者验证: 环境部署+测试执行+三视角验证"
argument-hint: "[branch-name]"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent]
---

# 阶段 7: QA + 使用者验证

**硬阻塞门**: 阶段 6 完成后**自动进入**。不是总负责人决定是否执行。`qa-report.md` 不存在则阶段 8 拒绝进入。

**执行者**: DevOps（环境部署）+ QA Agent（执行测试）+ 使用者（三视角验证）

**入口条件**: S6 出口通过（review-findings.md + tech-debt.md 存在）

---

## 操作步骤

### 步骤 7.0: DevOps 部署测试环境

确认工作目录在 feature 分支最新状态：

```bash
git checkout {branch}
```

```bash
git pull origin {branch}
```

派发 DevOps Agent:

1. 使用 docker-compose.test.yml 启动 PG + GoCell App 测试环境
2. 确认 Playwright 已安装且配置可用（playwright.config.ts）
3. 确认 playwright.config.ts 包含 `trace: 'on'`, `video: 'on-first-retry'`, `screenshot: 'only-on-failure'`
4. 确认种子数据已加载（seed-test-data.sh 或 Go test helper）
5. 产出: 测试环境就绪确认

### 步骤 7.1: 执行自动化测试

派发 DevOps Agent（name=devops）执行测试套件：

```bash
bash .claude/skills/stage-7-qa/scripts/run-qa.sh --branch {branch}
```

脚本自动完成：
1. 创建 `specs/{branch}/evidence/` 目录结构
2. 执行 `go test ./... -v -count=1` 并保存到 `evidence/go-test/result.txt`
3. 执行 `gocell validate` 并保存到 `evidence/validate/result.txt`
4. 遍历 `src/journeys/J-*.yaml` 执行 `gocell verify journey` 并保存到 `evidence/journey/`
5. 如 role-roster.md 中 QA自动化=ON 且存在 UI 组件，执行 `npx playwright test`
6. 汇总通过/失败统计

**脚本不存在时**: 在 phase-charter.md 中声明 `N/A:DEFERRED run-qa.sh`，改为手动逐条执行并记录。

**统一证据目录结构**:
```
specs/{branch}/evidence/
├── go-test/          # go test 输出
│   └── result.txt
├── validate/         # gocell validate 输出
│   └── result.txt
├── journey/          # journey 验收测试（每个 journey 一个文件）
│   ├── J-001.txt
│   └── J-002.txt
└── playwright/       # Playwright trace + screenshots（有 UI 时）
    ├── trace.zip
    └── *.png
```

### 步骤 7.2: 使用者三视角验证

派发 Product Manager Agent（name=product-manager）主持三视角验证。按三个 Persona 视角顺序执行:

**视角 A — PM（浏览器 UI 全流程）**:
- A1. 项目列表页加载 → 3 秒内渲染 + 表头含义清晰
- A2. 创建项目 → 成功反馈 + 列表自动刷新
- A3. 点击项目 → 导航到 Runs 页 + URL 含 project_id
- A4. 审批中心 → 过滤器可用 + 空状态有意义
- A5. 导航栏切换 → 三入口可用 + 当前位置高亮
- A6. 整体: PM 能否通过 UI 回答"项目状态如何"

**视角 B — 开发者（UI + API）**:
- B1. 提交 Run 表单 → 字段有标记 + 必填项提示
- B2. API POST /api/v1/runs → 201 + run_id + < 1s
- B3. Run 列表 → 新 Run 出现 + SSE 状态实时更新
- B4. 任务详情 → 元数据卡片 + 时间线
- B5. SSE 连接状态指示器 → 颜色/文案一致
- B6. 访问不存在的 task → 404 清晰 + 返回入口
- B7. GET /health → 200
- B8. POST /projects → 信封格式 {"data":{...}}，分页 GET 返回 data/total/page/pageSize
- B9. POST 不存在的审批 → 标准错误格式 {"error":{"code":...,"message":...}}
- B10. 跨端点分页格式一致性

**视角 C — 框架集成者（Go 开发者首次接入）**:
- C1. `go get` 安装 → 无 replace/vendor 异常，依赖干净
- C2. godoc 可读性 → 导出类型/函数有清晰注释，package doc.go 存在
- C3. examples/ 可运行 → `go run` 或 `docker-compose up` 一键启动，README 步骤完整
- C4. Cell/Slice 脚手架 → `gocell scaffold cell`/`gocell scaffold slice` 产出可编译骨架
- C5. 错误信息可定位 → errcode 错误码能帮助开发者定位问题，非裸 "internal error"
- C6. 整体: 新开发者能否在 30 分钟内跑通一个 example 并理解 Cell 模型

每视角评分 1-5:
- 1=不可用 2=有明显摩擦 3=可接受 4=流畅 5=优秀

产出: `specs/{branch}/user-signoff.md`

判定:
- **APPROVE**: 均>=4 且无 P0 问题
- **CONDITIONAL**: 均>=3
- **REJECT**: 任一<3 或存在 P0 问题

> **注意**: 如 role-roster.md 中前端开发者=OFF，使用者验证的 UI 视角（视角 A）标记为 N/A，仅执行 API + 框架视角（视角 B、C）。user-signoff.md 仍需产出，但 UI 部分标注 `N/A — SCOPE_IRRELEVANT`。视角 C（框架集成者）始终执行。

### 步骤 7.3: 总负责人编写 qa-report.md

产出: `specs/{branch}/qa-report.md`

内容必须包含:
1. Go test / contract test / journey test 范围和结果
2. gocell validate 验证结果
3. Playwright 测试范围和结果（如适用）
4. 覆盖的用户场景
5. 未覆盖的场景（记录原因）
6. 手动验证结论
7. 引用 `product-acceptance-criteria.md` 中的 AC 编号
8. **每条结论必须引用证据路径**（`specs/{branch}/evidence/{go-test|validate|journey|playwright}/` 下的具体文件）

---

## 硬产出物

| 产出物 | 路径 | 说明 |
|--------|------|------|
| qa-report.md | `specs/{branch}/qa-report.md` | 测试结果报告 |
| user-signoff.md | `specs/{branch}/user-signoff.md` | 三视角使用者签收 |
| evidence/go-test/ | `specs/{branch}/evidence/go-test/` | go test 输出 |
| evidence/validate/ | `specs/{branch}/evidence/validate/` | gocell validate 输出 |
| evidence/journey/ | `specs/{branch}/evidence/journey/` | journey 验收输出 |
| evidence/playwright/ | `specs/{branch}/evidence/playwright/` | Playwright trace + screenshots（有 UI 时） |

---

## 出口条件

```bash
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S7 --branch {branch} --check exit
```

**绝对禁止跳过此阶段。** 即使没有 UI 变化，也需要运行已有的测试确认无回归。

---

## 出口检查清单

- [ ] qa-report.md 已写入 specs/{branch}/
- [ ] user-signoff.md 已写入 specs/{branch}/（或 N/A 声明已在 phase-charter.md 中记录）
- [ ] qa-report.md 包含 gocell validate 验证结果
- [ ] qa-report.md 每条结论引用证据路径
- [ ] phase-gate-check.py --stage S7 --branch {branch} --check exit = PASS
