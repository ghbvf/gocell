#!/usr/bin/env bash
# PreToolUse(ExitPlanMode) hook: 首次退出 plan mode 时注入一次计划自检。
# 本会话首次调用 ExitPlanMode 时 deny 并回喂自检问题，迫使重审计划；
# 重新提交（第二次调用）即放行。状态按 session_id 隔离，每会话仅一次。
#
# 本脚本由 harness 直接执行，不经 Bash 工具，故不受 permissions.deny 约束。
set -euo pipefail

input=$(cat)
sid=$(printf '%s' "$input" | jq -r '.session_id // "default"')
state="${TMPDIR:-/tmp}/claude-exitplan-audited-${sid}"

# 本会话已自检过 → 放行（无输出等价 allow）
if [ -f "$state" ]; then
  exit 0
fi

# 首次：打标记 + deny，把自检问题回喂模型，迫使重审后重新提交计划
touch "$state"
jq -nc '{
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    permissionDecision: "deny",
    permissionDecisionReason: "措施符合彻底、不向后兼容 措施优雅简洁、AI HARD的原则吗"
  }
}'
