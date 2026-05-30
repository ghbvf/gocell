#!/usr/bin/env bash
# PreToolUse(Bash) hook: 禁止用 shell 文本工具替代专用工具。
# 拦截 awk / sed / grep / python / python3 / echo / cat —— 包括出现在
# 管道、&&、;、$()、反引号等复合命令中的用法（prefix deny 规则无法覆盖）。
#
# 替代方案：读文件用 Read；搜索用 Grep / Glob；改文件用 Edit / Write；
# bash 只跑真命令（go / git / gh / rm 等）。
#
# 本脚本由 harness 直接执行，不经 Bash 工具，故不受 permissions.deny 约束。
set -euo pipefail

input=$(cat)
cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // ""')

# 命令调用位置：行首 / 空白 / | / & / ; / ( / 反引号 / $( 之后，
# 后接可选空白与目标命令名，且其后不是标识符字符（避免误伤 catalog / sedna 等）。
if printf '%s' "$cmd" | grep -Eq '(^|[[:space:]|&;(`]|\$\()[[:space:]]*(awk|sed|grep|python3?|echo|cat)([^A-Za-z0-9_./-]|$)'; then
  jq -nc '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: "禁止 awk/sed/grep/python/python3/echo/cat：读文件用 Read，搜索用 Grep/Glob，改文件用 Edit/Write，bash 只跑真命令(go/git/gh/rm 等)。"
    }
  }'
fi
