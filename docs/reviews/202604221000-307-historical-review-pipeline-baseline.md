# 历史审查流水线 Phase A 基线报告（v1）

> 日期: 2026-04-22
> 范围: 六席位历史审查提取流水线（步骤0/1起步）
> 方法: 先做输入源盘点与结构化可解析性评估，再启动六席位并行抽取。

---

## 1. 输入源覆盖统计

| 来源 | 文件数 | 备注 |
|---|---:|---|
| docs/reviews | 110 | 主审查数据源 |
| docs/reviews/archive | 74 | 历史归档子集 |
| bak/docs/reviews | 51 | 备份审查资料 |
| specs | 126 | 包含 research/review-findings/tech-debt 等补充证据 |
| VS Code Copilot debug-logs | 3 | 可选增强数据源 |

补充统计:
- docs/backlog.md + docs/tech-debt-registry.md 的 markdown 表格行（以 `|` 开头）估算: 245 行。

---

## 2. 可解析性分层

### 高可解析（优先）

1. journeys/status-board.yaml（结构化 YAML）
2. docs/backlog.md、docs/tech-debt-registry.md（稳定表格结构）
3. docs/reviews 中结构化表格较完整的六席位报告

### 中可解析

1. docs/reviews 一般审查报告（含结构化段落 + 混合叙述）
2. specs 下 review-findings/research 文档

### 低可解析（需要人工复核）

1. bak/docs/reviews 历史叙述型文档
2. debug-logs（格式不稳定，适合作为佐证不适合作为主判据）

---

## 3. 统一模型与门禁

本轮采用模板:
- templates/review-finding-normalized-schema.yaml

强制门禁:
1. 六席位覆盖率 = 100%
2. 证据可追溯率 = 100%
3. 涉及“最佳实践”结论必须具备每主题 >= 3 个开源项目证据，否则标记 `insufficient`

---

## 4. 首批执行策略（步骤1起跑）

### 抽取顺序

1. docs/reviews 最近 30 天报告（高信号）
2. docs/backlog.md + docs/tech-debt-registry.md（状态与频次锚点）
3. specs/*/review-findings.md（专题补强）
4. archive 与 bak 作为复发模式补证

### 抽取单位

- 以“根因主题批次”作为并行单元，不按单文件切分。

### 席位输出

每席位必须输出:
1. Raw Findings
2. Seat Digest
3. benchmarkNeeded 标记

---

## 5. 已落地资产

1. docs/workflow/202604220930-032-six-seat-historical-review-pipeline-sop.md
2. templates/review-finding-normalized-schema.yaml
3. templates/six-seat-stage-report.md
4. templates/six-seat-subagent-prompts.md

---

## 6. 风险与应对

1. 风险: 文档格式异构导致漏提取。
   - 应对: 高可解析源先跑，低可解析源进入人工复核池。
2. 风险: 席位间重复问题过多。
   - 应对: 使用 rootCauseCluster 作为唯一归并键。
3. 风险: 对标证据不足。
   - 应对: 建立主题级对标任务，未达 3 项目不下最佳实践结论。

---

## 7. 下一步（Implementation Step 1）

1. 选择首批主题（建议: auth boundary, refresh rotation, runtime lifecycle, contract governance）。
2. 按 templates/six-seat-subagent-prompts.md 启动六席位并行抽取。
3. 用 templates/six-seat-stage-report.md 产出步骤1的常见问题报告 V1。
