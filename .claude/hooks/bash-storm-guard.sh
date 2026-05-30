#!/usr/bin/env bash
# PreToolUse(Bash) hook: 抑制 bash 风暴。
#   1) 去重：同一条命令在 DUP_WINDOW 秒内重复 → deny。
#   2) 限流：BURST_WINDOW 秒内 bash 调用数 >= BURST_MAX → deny。
# fail-open：任何内部异常一律放行——绝不因 hook 自身故障 brick 掉 bash。
# 阈值按需调。harness 直接执行本脚本，内部 awk/cksum 不经 Bash 工具、不被拦。

DUP_WINDOW=12
BURST_WINDOW=6
BURST_MAX=5

command -v jq >/dev/null 2>&1 || exit 0

input=$(cat) || exit 0
cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // ""' 2>/dev/null) || exit 0
[ -z "$cmd" ] && exit 0

sid=$(printf '%s' "$input" | jq -r '.session_id // "default"' 2>/dev/null)
sid=$(printf '%s' "${sid:-default}" | tr -cd 'A-Za-z0-9-')
[ -z "$sid" ] && sid=default

now=$(date +%s 2>/dev/null) || exit 0
hash=$(printf '%s' "$cmd" | cksum 2>/dev/null | cut -d' ' -f1)
[ -z "$hash" ] && exit 0
log="${TMPDIR:-/tmp}/claude-bashguard-${sid}.log"   # 行: "<epoch> <hash>"

emit_deny() {
  jq -nc --arg r "$1" '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:$r}}' 2>/dev/null
  exit 0
}

maxw=$DUP_WINDOW; [ "$BURST_WINDOW" -gt "$maxw" ] 2>/dev/null && maxw=$BURST_WINDOW
recent=$(awk -v c="$now" -v w="$maxw" '($1+w)>=c' "$log" 2>/dev/null)
dup=$(printf '%s\n' "$recent" | awk -v c="$now" -v w="$DUP_WINDOW" -v h="$hash" '($1+w)>=c && $2==h{n++} END{print n+0}' 2>/dev/null)
cnt=$(printf '%s\n' "$recent" | awk -v c="$now" -v w="$BURST_WINDOW" '($1+w)>=c && NF{n++} END{print n+0}' 2>/dev/null)

[ "${dup:-0}" -ge 1 ] 2>/dev/null && emit_deny "重复命令：近 ${DUP_WINDOW}s 内已发过同一条 bash。停手，等上一条结果回来再决定，别重发。"
[ "${cnt:-0}" -ge "$BURST_MAX" ] 2>/dev/null && emit_deny "bash 过密：近 ${BURST_WINDOW}s 内已 ${cnt} 条 bash（上限 ${BURST_MAX}）。停手——等结果、合并意图，确需多条时逐条来。"

{ printf '%s %s\n' "$now" "$hash" >> "$log"
  t=$(mktemp 2>/dev/null) && awk -v c="$now" -v w="$maxw" '($1+w)>=c' "$log" > "$t" 2>/dev/null && mv "$t" "$log"
} 2>/dev/null || true
exit 0
