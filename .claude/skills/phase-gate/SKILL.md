---
name: phase-gate
description: "阶段门检查: 验证准入准出条件+FAIL处理"
argument-hint: "--stage SN --branch [branch] --check entry|exit"
allowed-tools: [Read, Glob, Grep, Bash]
---

# 阶段门检查（Phase Gate）

**何时调用**: 总负责人在每个阶段转换时调用。

---

## 调用方式

```bash
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S{N} --branch {branch} --check exit
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S{N} --branch {branch} --check entry
```

### 脚本行为（fail-closed）

1. YAML 解析失败 → **FAIL**（不是空集合继续）
2. 阶段/检查类型不存在 → **FAIL**
3. 空检查集 `{}` → **FAIL**（仅 S0 entry 例外，无条件通过）
3. 检查 `required_files` — specs/{branch}/ 下文件存在且非空
4. 检查 `repo_files` — repo root 下文件存在且非空
5. 检查 `content_checks` — 文件内容 pattern 匹配 / special check
6. 检查 `command_checks` — 命令退出码为 0
7. N/A 声明仅接受结构化枚举：`N/A:SCOPE_IRRELEVANT`、`N/A:RESOURCE_UNAVAILABLE`、`N/A:DEFERRED`
8. 输出 PASS/FAIL + 失败清单
9. 写审计日志 `specs/{branch}/gate-audit.log`

**依赖**: python3 + PyYAML（`pip3 install pyyaml`）

---

## phase-gates.yaml 是唯一可执行合同

所有硬性准入准出条件定义在 `.claude/skills/phase-gate/phase-gates.yaml` 中。workflow-detailed.md 和 stage SKILL 中的出口清单仅作为人类参考说明，**不构成独立规则**。如有差异以 YAML 为准。

---

## FAIL 处理

1. 查看缺失清单（脚本输出具体 FAIL 项）
2. 定位责任角色（查文档职责矩阵）
3. 回退补全（禁止跳过检查强行进入下一阶段）
4. N/A 声明：在 phase-charter.md 中用 `N/A:<枚举值> <文件名>` 格式声明

---

## 速查表（与 phase-gates.yaml 对齐）

| 阶段 | 方向 | required_files | content_checks |
|------|------|---------------|----------------|
| S0 | exit | phase-charter.md, role-roster.md, product-context.md | phase-charter: `(目标\|范围\|非目标)`, role-roster: `(ON\|OFF)` |
| S1 | exit | spec.md, checklists/requirements.md | spec: `(文档\|DevOps\|测试\|contract test\|journey test)` |
| S2 | exit | kernel-constraints.md, review-architect.md, review-roadmap.md, review-product-manager.md | kernel-constraints: `(集成风险\|分层隔离\|核心约束\|元数据合规\|契约完整性)` |
| S3 | exit | decisions.md | decisions: `(采纳\|拒绝\|延迟\|accept\|reject\|defer)` |
| S4 | exit | plan.md, tasks.md, product-acceptance-criteria.md | tasks: 3 pattern, product-acceptance-criteria: `(P1\|P2\|P3)` |
| S5 | exit | tasks.md | tasks: no_unchecked_tasks; command: `cd src && go build ./...` |
| S7 | exit | qa-report.md, user-signoff.md | qa-report: `(go test.*PASS\|coverage\|全量测试通过)`, user-signoff: `(APPROVE\|CONDITIONAL\|REJECT)`; command: `cd src && go test ./...` |
| S8 | exit | kernel-review-report.md, product-review-report.md, phase-report.md; repo: CHANGELOG.md | kernel/product-review: `(绿\|黄\|红\|GREEN\|YELLOW\|RED)`, phase-report: `(变更摘要\|关键决策)` |

---

## 使用示例

```bash
# S0 出口检查
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S0 --branch feat/001-session --check exit

# S5 入口检查
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S5 --branch feat/001-session --check entry

# S8 合并前最终检查
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S8 --branch feat/001-session --check exit
```
