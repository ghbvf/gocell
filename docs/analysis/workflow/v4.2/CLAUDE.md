# 协作说明

## 必须遵守的工作方式

- 文件操作优先使用内置工具（Read/Glob/Grep/Edit/Write），Bash 仅用于 shell 执行（go/git/docker）
- 修改较大内容前先用 `Glob` 确认目标目录存在
- 提交信息遵循 Conventional Commits
- 涉及功能或行为变更时，同步更新对应层级文档，而不是只改一份摘要
- 禁止大范围修改原有文件，如果有超过10行修改，生成新的文件

## 文档命名规则

格式：`yyyyMMddHHmm-编号-实际功能或问题.md`（ date "+%Y%m%d%H%M" 后缀按内容选择，不限 `.md`）

示例：`202603281443-022-compliance-api-review.md`

## Active Technologies

- Go 1.25+
- PostgreSQL (adapters/postgres)
- Vue 3 + Vite + TypeScript (examples/ 示例项目, 条件启用)
- Playwright (E2E testing, examples/ 条件启用)

## 阶段门强制检查

总负责人在进入下一阶段前必须运行 `phase-gate-check.sh --stage SN --check exit`。
阶段门数据定义: `.claude/skills/phase-gate/phase-gates.yaml`
脚本不通过（FAIL）时禁止进入下一阶段。

## 多角色工作流

@see `docs/workflow-detailed.md`（权威源：角色体系、8 阶段详细操作、文档职责矩阵、验收清单）
