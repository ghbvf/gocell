#!/usr/bin/env bash
# PreToolUse(Bash) hook: 拦截 awk/sed/grep/echo/cat（含管道/复合命令内），强制改用
# Read/Grep/Glob/Edit/Write。harness 直接执行、不经 Bash 工具，故本脚本自身用 grep/jq
# 不被拦也不递归。
set -euo pipefail

command -v jq >/dev/null 2>&1 || exit 0   # jq 缺失则放行

input=$(cat)
cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // ""')

# 命令名出现在 行首/空白/|/&/;/(/反引号/$( 之后，且其后非标识符字符（避免误伤 catalog/sedna）
if printf '%s' "$cmd" | grep -Eq '(^|[[:space:]|&;(`]|\$\()[[:space:]]*(awk|sed|grep|echo|cat)([^A-Za-z0-9_./-]|$)'; then
  jq -nc '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: "禁止 awk/sed/grep/echo/cat：读文件用 Read，搜索用 Grep/Glob，改文件用 Edit/Write。"
    }
  }'
fi
