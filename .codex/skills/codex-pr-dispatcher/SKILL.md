---
name: codex-pr-dispatcher
description: "主动唤醒本机 Codex PR app dispatcher，立即拉取一次 GitHub PR label 信息并按 ledger 去重派发。"
argument-hint: ""
allowed-tools: [Bash, Read]
disable-model-invocation: true
---

# Codex PR Dispatcher

Use this skill when the user asks to immediately poll PR review labels, wake the dispatcher, or manually trigger the Codex PR app dispatcher.

Run:

```bash
bash hack/automation/codex-pr-app-dispatcher/trigger.sh
```

Then confirm from the router log:

```bash
tail -30 "${GOCELL_APP_ROUTER_HOME:-$HOME/.local/gocell-pr-app-router}/logs/router.log"
```

Expected signal:

```text
active poll trigger received
```

This only wakes one `poll_once`. It must not wait for `turn/completed`, inspect Codex session results, or move PR labels directly.

