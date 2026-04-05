---
name: stage-7-qa
description: "QA+使用者验证: 环境部署+测试执行+四视角验证"
argument-hint: "[branch-name]"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent]
---

# 阶段 7: QA + 使用者验证

**硬阻塞门**: 阶段 6 完成后**自动进入**。不是总负责人决定是否执行。`qa-report.md` 不存在则阶段 8 拒绝进入。

**执行者**: DevOps（环境部署）+ QA Agent（执行测试）+ 使用者（四视角验证）

**入口条件**: S6 出口通过（review-findings.md + tech-debt.md 存在）

---

## 操作步骤

### 步骤 7.0: DevOps 部署测试环境

派发 DevOps Agent:

1. 使用 docker-compose.test.yml 启动 PG + GoCell App 测试环境
2. 确认 Playwright 已安装且配置可用（playwright.config.ts）
3. 确认 playwright.config.ts 包含 `trace: 'on'`, `video: 'on-first-retry'`, `screenshot: 'only-on-failure'`
4. 确认种子数据已加载（seed-test-data.sh 或 Go test helper）
5. 产出: 测试环境就绪确认

### 步骤 7.1: QA Agent 执行测试

测试脚本已在 S5 编写完成。

1. 运行 Go 测试:
   ```bash
   go test ./... -v -count=1
   ```
2. 运行元数据验证（V3 验证规则全集）:
   ```bash
   gocell validate
   ```
3. 运行 Journey 验收测试（验证业务闭环）:
   ```bash
   gocell verify --journeys
   ```
4. 运行 Playwright E2E 测试（**仅 role-roster.md 中 QA自动化=ON 且存在 UI 组件时执行**）:
   ```bash
   npx playwright test
   ```
   如不适用，在 qa-report.md 中标注 `E2E: N/A — SCOPE_IRRELEVANT`。
5. 收集结果，补充回归脚本（如发现 S5 遗漏的场景）
6. **证据采集**（Playwright 适用时）: 确认 `specs/{branch}/evidence/playwright/` 目录包含 trace + screenshots。需配置 playwright.config.ts 的 `outputDir` 指向此目录。
7. 证据路径: `specs/{branch}/evidence/playwright/`（trace zip + screenshot png）

### 步骤 7.2: 使用者四视角验证

按四个 Persona 视角顺序执行:

**视角 A — PM（浏览器 UI 全流程）**:
- A1. 项目列表页加载 → 3 秒内渲染 + 表头含义清晰
- A2. 创建项目 → 成功反馈 + 列表自动刷新
- A3. 点击项目 → 导航到详情页 + URL 含标识
- A4. 整体: PM 能否通过 UI 回答"项���状态如何"

**视角 B — 开发者（UI + API 混合）**:
- B1. API 请求 → 正确响应 + 合理响应时间
- B2. 错误处理 → 标准错误格式（errcode 包）+ 有意义的错误信息
- B3. 文档 → godoc 覆盖导出 API + examples 可运行

**视角 C — Vibe Coder（纯 API）**:
- C1. GET /health → 200
- C2. 标准响应格式 `{"data":{...}}`
- C3. 分页格式一致 data/total/page/pageSize
- C4. 整体: API 能否支撑脚本化自动化

**视角 D — 框架集成者（Go 开发者首次接入）**:
- D1. `go get` 安装 → 无 replace/vendor 异常，依赖干净
- D2. godoc 可读性 → 导出类型/函数有清晰注释，package doc.go 存在
- D3. examples/ 可运行 → `go run` 或 `docker-compose up` 一键启动，README 步骤完整
- D4. Cell/Slice 脚手架 → `gocell scaffold cell`/`gocell scaffold slice` 产出可编译骨架
- D5. 错误信息可定位 → errcode 错误码能帮助开发者定位问题，非裸 "internal error"
- D6. 整体: 新开发者能否在 30 分钟内跑通一个 example 并理解 Cell 模型

每视角评分 1-5:
- 1=不可用 2=有明显摩擦 3=可接受 4=流畅 5=优秀

产出: `specs/{branch}/user-signoff.md`

判定:
- **APPROVE**: 均>=4 且无 P0 问题
- **CONDITIONAL**: 均>=3
- **REJECT**: 任一<3 或存在 P0 问题

> **注意**: 如 role-roster.md 中前端开发者=OFF，使用者验证的 UI 视角（视角 A）标记为 N/A，仅执行 API + 框架视角（视角 B、C、D）。user-signoff.md 仍需产出，但 UI 部分标注 `N/A — SCOPE_IRRELEVANT`。视角 D（框架集成者）始终执行。

### 步骤 7.3: QA 编写 qa-report.md

产出: `specs/{branch}/qa-report.md`

内容必须包含:
1. Go test / contract test / journey test 范围和结果
2. gocell validate 验证结果
3. Playwright 测试范围和结果���如适用）
4. 覆盖的用户场景
5. 未覆盖的场景（记录原因）
6. 手动验证结论
7. 引用 `product-acceptance-criteria.md` 中的 AC 编号
8. **每条结论必须引用证据路径**（`specs/{branch}/evidence/playwright/` 下的具体文件）

---

## 硬产出物

| 产出物 | 路径 | 说明 |
|--------|------|------|
| qa-report.md | `specs/{branch}/qa-report.md` | 测试结果报告 |
| user-signoff.md | `specs/{branch}/user-signoff.md` | 四视角使用者签收 |
| evidence/ | `specs/{branch}/evidence/playwright/` | Playwright trace + screenshots |

---

## 出口条件

```bash
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S7 --branch {branch} --check exit
```

**绝对禁止跳过此阶段。** 即使没有 UI 变化，也需要运行已有的测��确认无回归。

---

## 出口检查清单

- [ ] qa-report.md 已写入 specs/{branch}/
- [ ] user-signoff.md 已写入 specs/{branch}/（或 N/A 声明已在 phase-charter.md 中记录）
- [ ] qa-report.md 包含 gocell validate 验证结果
- [ ] qa-report.md 每条结论引用证据路径
- [ ] phase-gate-check.sh --stage S7 --branch {branch} --check exit = PASS
