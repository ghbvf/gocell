#!/usr/bin/env bash
# PreToolUse(ExitPlanMode) hook: 本会话首次退出 plan mode 时 deny 并回喂自检问题，
# 迫使重审计划后重新提交即放行。状态按 session_id 隔离，每会话仅一次。
set -euo pipefail

command -v jq >/dev/null 2>&1 || exit 0   # jq 缺失则放行，不阻塞退出 plan mode

input=$(cat)
sid=$(printf '%s' "$input" | jq -r '.session_id // "default"')
sid=$(printf '%s' "$sid" | tr -cd 'A-Za-z0-9-')   # 消毒：杜绝路径穿越
[ -n "$sid" ] || sid="default"
state="${TMPDIR:-/tmp}/claude-exitplan-audited-${sid}"

[ -f "$state" ] && exit 0   # 已自检过 → 放行

# 先 deny 后打标记：jq 失败则标记不落盘，避免 hook 被永久旁路
jq -nc '{
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    permissionDecision: "deny",
    permissionDecisionReason: "措施符合彻底、不向后兼容 措施优雅简洁、AI HARD的原则吗"
  }
}'
touch "$state"
