# gocell CLI flag defaults — local vs CI

K#05 W2 改了 `gocell generate cell` / `gocell verify codegen-cell` 默认行为，本地与 CI 体验有差异，本文档说明。

## generate cell

| 调用形式 | 行为 |
|---|---|
| `gocell generate cell` | 等价 `--all`（处理所有 K#04 opt-in cell） |
| `gocell generate cell <cellID>` | 仅处理该 cell；positional id 优先于 --all |
| `gocell generate cell --all=false <cellID>` | 同上（兼容旧脚本） |
| `gocell generate cell --dry-run` | 默认 --all + dry-run |
| `gocell generate cell --verify` | 默认 --all + verify drift |

## verify codegen-cell

| 调用形式 | 行为 |
|---|---|
| `gocell verify codegen-cell` | 等价 `--local=true`（in-place verify against current working tree） |
| `gocell verify codegen-cell --local=false` | sandbox mode（git worktree 隔离 + go run） |

**dirty worktree 注意**：本地 in-place verify 会把 staged/unstaged 改动当成 baseline；如果你刚跑过 `gocell generate cell --all` 但还没 commit，verify 看到的是新生成的版本，可能误报"无 drift"而 CI 报 drift。建议：
1. 先 `git stash`（或 commit）再跑 verify
2. 或者本地直接跑 sandbox 模式 `gocell verify codegen-cell --local=false`

## CI 行为

`hack/verify-codegen-cell.sh` 在 CI 环境无参数时显式注入 `--local=false`，保留 sandbox 隔离行为。这意味着 .github/workflows 不需要更改。CI 调用始终走 sandbox。

## 历史

- 2026-05-04 K#05 PR-A1 ship：CLI default 不变（--all=false / --local=false）
- 2026-05-05 K#05 PR-A2+B ship（本 PR）：CLI default 翻转为 --all=true / --local=true，`hack/verify-codegen-cell.sh` 同步注入 `--local=false` 以保持 CI 兼容
