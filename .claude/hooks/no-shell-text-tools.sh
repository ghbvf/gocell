#!/usr/bin/env bash
# PreToolUse(Bash) hook: 拦 awk/sed/echo/cat（全位置，含管道内）与「搜文件式」的 grep，
# 强制改用 Read/Grep/Glob/Edit/Write。**仅 grep 的管道过滤放行**——`cmd | grep` 紧跟单个
# `|` 之后（读 stdin）是 grep 不可替代的合法用途（Grep 工具喂不进命令 stdout）；awk/sed
# 不开此口子。harness 直接执行本脚本、不经 Bash 工具，故脚本自身用 grep/jq 不被拦也不递归。
set -euo pipefail

command -v jq >/dev/null 2>&1 || exit 0   # jq 缺失则放行

input=$(cat)
cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // ""')

deny=0
# awk/sed/echo/cat：任何位置都拦（含管道内）。
if printf '%s' "$cmd" | grep -Eq '(^|[[:space:]|&;(`]|\$\()[[:space:]]*(awk|sed|echo|cat)([^A-Za-z0-9_./-]|$)'; then
  deny=1
fi
# grep：仅当出现在「非单管道」的命令位置才拦——行首 / ; / && / || / ( / 反引号 / $( /
# 另一命令词后（如 xargs grep）。紧跟单个 `|` 之后（读 stdin 的管道过滤）放行。
# 末尾 ([^A-Za-z0-9_./-]|$) 避免误伤 catalog 等子串。
if printf '%s' "$cmd" | grep -Eq '(^|[;&(`]|\$\(|\|\||[^|[:space:]][[:space:]])[[:space:]]*grep([^A-Za-z0-9_./-]|$)'; then
  deny=1
fi

if [ "$deny" -eq 1 ]; then
  jq -nc '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: "搜文件用 Grep/Glob、读文件用 Read、改文件用 Edit/Write；禁止 grep 搜文件、awk/sed/cat/echo 处理文件。（仅 grep 管道过滤 cmd | grep 已放行）"
    }
  }'
fi
exit 0
