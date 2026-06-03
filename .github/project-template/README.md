# 项目模版索引（单一入口）

> **切分原则**：
> - **模版**（本文件夹）拥有「issue / PR / 评论里写什么内容、什么结构」（WHAT）。
> - **`PROJECT.md`**（本文件夹）是「label 体系 / Project 字段 / 评级 rubric / PR 流程」的治理单源。
> - **技能** 拥有「跑什么 `gh` 命令、做什么决策、按什么算法排 wave、切什么 label」的编排（HOW/WHEN）。
>
> 引用方向**单向**：技能 / CI → 模版 + PROJECT.md。模版与 PROJECT.md **不反向列出谁引用它们**，也不互抄内容（label 清单只在 PROJECT.md，模版指向它）。
>
> 本仓 issue/PR **全程由 AI 经 `gh` CLI 创建**：`--body-file` 读下列模版填充。

## 清单

| 文件 | 拥有内容 |
|------|---------|
| `PROJECT.md` | label 体系 / Project 字段 / 评级 rubric / PR 流程（治理单源） |
| `backlog.md` | 新建 backlog issue 的 body 骨架（现状 / 修复方向 / Files / Trigger / Source） |
| `epic.md` | epic body 骨架（目标 / 验收 / 实施顺序段） |
| `pull_request_template.md` | PR body 骨架（Summary / Refs / Test plan） |
| `pr-comment.md` | `pm:ship` / `pm:fix` PR 评论格式 |

## 用法

```bash
gh issue create --label backlog --label pri-pX --label area-XX --label type-XX \
  --title "[<ID>] ..." --body-file <填好的 backlog.md>
gh pr create --title "..." --body-file <填好的 pull_request_template.md>
gh pr comment <N> --body-file <填好的 pr-comment.md 模板>
```

> labels / title 由建的一方用 `--label` / `--title` 显式给（取值见 `PROJECT.md` §2/§3）。模版只承载 body 结构，工作流 / 流转 / 约定见 `PROJECT.md`。
