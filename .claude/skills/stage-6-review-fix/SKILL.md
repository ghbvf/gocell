---
name: stage-6-review-fix
description: "Review-Fix循环: 6席位审查+最多3轮修复+tech-debt"
argument-hint: "[branch-name]"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent]
---

# 阶段 6: 集成审查 + Tech Debt 整理

S5 per-PR 循环中每个 PR 已通过独立 review。本阶段对集成后的完整代码做最终审查，聚焦跨 PR 交互和集成风险。

**执行者**: 总负责人派发 Reviewer + 开发者修复

**入口条件**: S5 出口通过（pr-plan.md 所有 PR merged + 全量 build/test 绿）

---

## 6 个命名 Review Bench 席位

| 席位 | 审查焦点 |
|------|---------|
| **席位 1: 架构一致性** | DDD 分层、聚合边界、模块耦合 |
| **席位 2: 安全/权限** | 认证鉴权、数据暴露、攻击面 |
| **席位 3: 测试/回归** | 测试覆盖、回归风险、边界用例 |
| **席位 4: 运维/部署** | Docker/CI、migration 安全 |
| **席位 5: DX/可维护性** | 可读性、命名、复杂度 |
| **席位 6: 产品/用户体验** | 交互流程、错误提示、空状态 |

> 各席位的详细审查标准和阈值定义在 `.claude/agents/reviewer.md` 中（唯一真相源）。

---

## 上下文获取（Reviewer 自行完成）

每个 Reviewer Agent 启动后必须自行获取以下 4 项上下文（不由总负责人注入）：

1. **kernel-constraints.md** — 内核约束基准，读取 `specs/{branch}/kernel-constraints.md`
2. **git diff stat** -- 自行运行 `git diff develop...HEAD --stat` 获取变更范围概览
3. **spec.md** — 需求对照基准，读取 `specs/{branch}/spec.md`
4. **当前 commit hash** — 自行运行 `git rev-parse HEAD` 记录审查基准版本

## Reasoning Blindness 指令

所有 Reviewer 的 prompt 必须包含以下指令：

```
审查纪律: 直接审查代码变更和测试覆盖。不参考 Agent 对自身工作的描述。
不因"Agent 说它已测试"就跳过验证。自行确认事实。
```

---

## 操作步骤

### Round 1（全量）

**6.1** 派发 6 个命名席位 review Agent：

每个 Reviewer prompt 模板：
```
角色: {席位名称} Reviewer
审查焦点: {上表对应焦点}

启动后自行获取上下文（不依赖总负责人注入）:
1. 读取 specs/{branch}/kernel-constraints.md
2. 运行 git diff develop...HEAD --stat 获取变更范围
3. 读取 specs/{branch}/spec.md
4. 运行 git rev-parse HEAD 记录审查基准版本

集成焦点: 关注跨 PR 的交互问题 -- 接口不匹配、重复定义、遗漏的集成测试。
各 PR 内部逻辑已在 S5 审查过，不需要重复。

审查纪律: 直接审查代码变更和测试覆盖。不参考 Agent 对自身工作的描述。
不因"Agent 说它已测试"就跳过验证。自行确认事实。

增加检查维度: 实现是否违反 Kernel Guardian 定义的内核约束？

产出格式:
每条 finding 必须包含:
- 严重级别: P0(阻塞) / P1(重要) / P2(建议)
- 发现席位: {席位名称}
- 受影响文件: {文件路径}
- 问题描述: {具体问题}
- 建议修复: {修复方案}
```

**6.2** 收集 findings → 产出 `specs/{branch}/review-findings.md`

review-findings.md 格式：
```markdown
# Review Findings — Phase {N}

## 审查基准版本
Commit: {hash}
Branch: {branch}
变更范围: {文件数} files changed

## P0（阻塞）
| # | 席位 | 文件 | 问题 | 建议修复 |
|---|------|------|------|---------|

## P1（重要）
| # | 席位 | 文件 | 问题 | 建议修复 |
|---|------|------|------|---------|

## P2（建议）
| # | 席位 | 文件 | 问题 | 建议修复 |
|---|------|------|------|---------|
```

**6.3** 总负责人裁决: 哪些修/哪些延迟

**6.4** 派发开发者修复 P0 + 选定的 P1

**6.5** 验证: build + test

### Round 2（聚焦）

**6.6** 派发聚焦 review（只检查修复区域 + 回归风险）

**6.7** VERIFIED → 进入阶段 7; ISSUE → 修复 → Round 3

### Round 3（最终）

**6.8** 只检查 P0

**6.9** 总负责人: 将 P1+ 记录到 `specs/{branch}/tech-debt.md`

### tech-debt.md 格式

```markdown
# Tech Debt — Phase {N}

## 分类说明
- [TECH]: 技术债务（代码质量、架构退化、测试缺失）
- [PRODUCT]: 产品债务（降级体验、缺失功能、临时方案）

## 延迟项
| # | 标签 | 来源席位 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|---------|------|---------|-------------|
| 1 | [TECH] | 架构一致性 | ... | ... | Phase N+1 |
| 2 | [PRODUCT] | 产品/UX | ... | ... | Phase N+2 |

## 统计
- [TECH] 新增: {N} 条
- [PRODUCT] 新增: {N} 条
- 上一 Phase 遗留已解决: {N} 条
```

**6.10** 仍有 P0 → 升级到架构师裁决

### 阶段门检查

```bash
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S6 --branch {branch} --check exit
```

---

## 硬性产出物

| 文件 | 路径 | 责任角色 |
|------|------|---------|
| review-findings.md | `specs/{branch}/review-findings.md` | 6 个 Reviewer |
| tech-debt.md | `specs/{branch}/tech-debt.md` | 总负责人 |
| 修复后的代码 | `src/` | 开发者 |

## 出口条件

```
[ ] 无 P0 遗留
[ ] review-findings.md 已写且含审查基准版本（commit hash）
[ ] tech-debt.md 已写且每条含 [TECH] 或 [PRODUCT] 标签
[ ] build + test 绿
[ ] phase-gate-check.py --stage S6 --branch {branch} --check exit = PASS
```
