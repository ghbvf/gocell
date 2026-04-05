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

## 使用示例

```bash
# S0 出口检查
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S0 --branch feat/001-session --check exit

# S5 入口检查
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S5 --branch feat/001-session --check entry

# S8 合并前最终检查
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S8 --branch feat/001-session --check exit
```
