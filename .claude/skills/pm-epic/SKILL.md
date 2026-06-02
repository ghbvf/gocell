---
name: pm-epic
description: "Epic 拆解 + wave 实施顺序调度。读 epic → 关键字查找相关 issues 并关联为 sub-issue → 建 blocked-by DAG → wave 拓扑排序 → 写 Project v2 Wave 字段 + 回填 epic body 实施顺序段 + 回评。当用户要整理一个 epic 的子任务、排实施顺序、更新 wave 时使用。"
argument-hint: "<#epic-number>"
allowed-tools: [Read, Grep, Bash, Agent, AskUserQuestion]
---

# pm-epic — Epic 拆解 + Wave 实施顺序

> 真源 = GitHub Issues + Project v2 #3（label/字段见 `.github/PROJECT.md`）。issue/label/评论原子操作的规范见
> `pm-issue`。本技能负责 epic 级「找子任务 → 排 wave → 写 Project Wave 字段 + epic body 实施顺序 + 回评」。
> `gh` 命令用 `dangerouslyDisableSandbox: true`。仓库 `ghbvf/gocell`，Project `--owner ghbvf --number 3`。

---

## 阶段 1：读 epic → 关键字查找相关 issues → 关联 → 汇总子 issues

1. **读 epic**：`gh issue view <epic#> --json number,title,body,labels`（确认 epic label；从 title + 目标/范围提取关键字）。
2. **关键字查找相关 issues**：`gh issue list --search "<关键字>" --state open --json number,title,labels`，挑出属于本 epic 的候选；用 AskUserQuestion 确认候选集（不擅自全关联）。
3. **关联到 epic**（建 sub-issue 关系，已是 sub-issue 的跳过）：
   ```bash
   id=$(gh api repos/ghbvf/gocell/issues/<child#> --jq .id)   # 取整数 id（非 number）
   gh api --method POST repos/ghbvf/gocell/issues/<epic#>/sub_issues -F sub_issue_id=$id
   ```
4. **汇总子 issues**：`gh api repos/ghbvf/gocell/issues/<epic#>/sub_issues --jq '.[]|{number,title,state}'`（含新关联）；对每个 OPEN 子任务读 label（area/type/pri）+ body 的 `Blocked-by: #NNN`（多行/逗号分隔，无声明=无前置）。

> 子任务跨 3+ 包或描述模糊时，用 `Agent(Explore)` 并行核实归属/依赖再汇总。

## 阶段 2：建 blocked-by DAG + wave 拓扑排序

**算法**（拓扑分层 / longest-path layering，作用域 = epic 子任务）：

1. 节点 = OPEN 子任务；有向边 `blocker → dependent`（来自 `Blocked-by`）。
2. 检测环：若有环，AskUserQuestion 让用户裁定断哪条边（不静默）。
3. **wave 分层**（longest-path layering）：
   - `wave(t) = 1` 若 t 无 blocker；
   - `wave(t) = 1 + max(wave(b) for b in blockers(t))` 否则。
   - 即每个子任务落在「所有前置都在更早 wave」的最早 wave。
4. **wave 内排序**：按 `pri`（p0>p1>p2>p3）→ `Cx`（小先）→ issue 号。
5. 输出每个子任务的 `(wave, 序)`。

呈现给用户的 dry-run 表：

```
Wave 1: #aaa(P1/Cx1) #bbb(P2/Cx2)
Wave 2: #ccc(P1/Cx2, blocked-by #aaa)
Wave 3: #ddd(P2/Cx3, blocked-by #ccc)
```

## 阶段 3：写 Project v2 Wave 字段（单源真值）

> Wave 字段是 epic 子任务排序的**机器单源**；epic body 段是派生视图。写前确认字段 / option / item 合法。

```bash
# 3.1 发现 Wave 字段 id + option id（single-select）
gh project field-list 3 --owner ghbvf --format json \
  | python3 -c "import sys,json;[print(f['id'],f['name'],[ (o['id'],o['name']) for o in f.get('options',[])]) for f in json.load(sys.stdin)['fields'] if f['name']=='Wave']"

# 3.2 找子任务对应的 project item id（按 content issue 号）
gh project item-list 3 --owner ghbvf --format json \
  | python3 -c "import sys,json;[print(i['id'],i['content'].get('number')) for i in json.load(sys.stdin)['items'] if i.get('content')]"

# 3.3 写 Wave（对每个子任务）
gh project item-edit --project-id PVT_kwHOBjsrB84BYQ3m \
  --id <ITEM_ID> --field-id <WAVE_FIELD_ID> --single-select-option-id <WAVE_N_OPTION_ID>
```

**写入门（人工核对，非机器门）**：每条写入前确认 `WAVE_N_OPTION_ID` 在 field-list 输出的 option 集中、
`ITEM_ID` 在 item-list（属本 project）。option 缺某 wave 档时先在 Project UI 加，再写。子任务未入 project
（无 item）时先 `gh project item-add 3 --owner ghbvf --url <issue-url>`。

## 阶段 4：回填 epic body 实施顺序段 + 回评

```bash
# 4.1 epic body 的「实施顺序」段重生成（派生视图）
gh issue edit <epic#> --body "$(...更新 ## 实施顺序 段...)"

# 4.2 回评通知（一行 + 标记）
gh issue comment <epic#> --body "$(cat <<'C'
<!-- pm:epic-wave -->
🌊 wave 实施顺序已更新（共 N wave，M 子任务）。Wave 字段 = 单源，详见 Project #3 / 本 issue body 实施顺序段。
C
)"
```

## 沟通规则

- 环检测命中 / wave option 缺档 / 子任务未入 project：停下 AskUserQuestion。
- DAG 排序结果先 dry-run 呈现，确认后再写 Project（写 live 状态）。
- 不改子任务代码 / 不关 issue / 不改 area-type-pri label（那是 `pm-issue` / `fix` 职责）。
