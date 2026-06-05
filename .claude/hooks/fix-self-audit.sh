#!/usr/bin/env bash
# PreToolUse(Bash) hook：fix 3.4 决策后、进阶段 4 改代码前，技能发占位命令
#   bash "$CLAUDE_PROJECT_DIR/.claude/hooks/fix-self-audit.sh" emit
# 每次 fix 首次发该信号 → deny 并回喂自检问题，提示模型按四原则复审本次 fix 方案后重发即放行。
# 消费式 toggle：放行（重发）时删标记，故每个 /fix 都重新自检（≠ exitplan-self-audit.sh 的每会话一次）。
#   已知 fail-open 降级：deny 后若中途不重发（/fix 提前中止），残留标记会被下个 /fix 的首次 emit
#   消费、少自检一轮（之后恢复）——预期降级，非 bug。
# 机制参照 exitplan-self-audit.sh（PreToolUse(ExitPlanMode) 同款 deny 回喂）；注册于 committed
#   .claude/settings.json（团队共享，#1599 刻意选择；exitplan 在 settings.local.json 是历史差异）。
# 两层过滤：settings.json 的 hook `if: Bash(bash *fix-self-audit.sh*)` 粗筛——只在 bash 调用本脚本时
#   spawn（go test/ls/cat/chmod 等不触发、无误导 statusMessage）；下面的 case 精筛 emit 实参。
# AI-robust：与 exitplan-self-audit.sh 同类的「自检提醒」hook，本质 Soft（按命令名锚定 + deny 回喂），
#   非约束 enforcement（不 enforce 不变式），不落 ai-robust.md「Soft 严禁立项」范围；属对既有先例的复制。
set -euo pipefail

# 占位命令直跑形态（技能实际执行的就是它）：无副作用、不读 stdin、成功返回
[ "${1:-}" = "emit" ] && exit 0

# jq 缺失则放行（不阻塞工具调用）；输出 stderr 使降级可见，不进 hook 响应
command -v jq >/dev/null 2>&1 || { echo "[fix-self-audit] jq 缺失，自检 hook 跳过" >&2; exit 0; }

input=$(cat)
cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // ""')
case "$cmd" in
  *fix-self-audit.sh*" emit"*) ;;   # 命中本信号（脚本名 + emit 实参）→ 走自检逻辑
  *) exit 0 ;;                       # 非本信号（含对脚本本身的 cat/chmod/grep 等）→ fail-open 放行
esac

sid=$(printf '%s' "$input" | jq -r '.session_id // "default"')
sid=$(printf '%s' "$sid" | tr -cd 'A-Za-z0-9-')   # 消毒：杜绝路径穿越
[ -n "$sid" ] || sid="default"
state="${TMPDIR:-/tmp}/claude-fix-audited-${sid}"

# 待放行的重发（自检后再次发信号）→ 消费标记 + 放行；下个 /fix 又从 deny 开始
[ -f "$state" ] && { rm -f "$state"; exit 0; }

# 本次 fix 首次发信号 → deny 并回喂自检问题（先 deny 后打标记，jq 失败则标记不落盘）
jq -nc '{
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    permissionDecision: "deny",
    permissionDecisionReason: "措施符合彻底、不向后兼容 措施优雅简洁、AI HARD的原则吗"
  }
}'
touch "$state"
