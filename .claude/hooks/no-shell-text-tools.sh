#!/usr/bin/env bash
# PreToolUse(Bash) hook: 拦 awk/sed/echo/cat（全位置，含管道内），强制改用
# Read/Edit/Write 处理文件。grep 不拦截。harness 直接执行本脚本、不经 Bash 工具，
# 故脚本自身用 grep/jq 不被拦也不递归。
set -euo pipefail

command -v jq >/dev/null 2>&1 || exit 0   # jq 缺失则放行

input=$(cat)
cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // ""')

deny=0
# awk/sed/echo/cat：任何位置都拦（含管道内）。
if printf '%s' "$cmd" | grep -Eq '(^|[[:space:]|&;(`]|\$\()[[:space:]]*(awk|sed|echo|cat)([^A-Za-z0-9_./-]|$)'; then
  deny=1
fi

if [ "$deny" -eq 1 ]; then
  jq -nc '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: "读文件用 Read、改文件用 Edit/Write；禁止 awk/sed/cat/echo 处理文件。"
    }
  }'
fi
exit 0
