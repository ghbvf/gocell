---
name: codex-pr-dispatcher
description: "管理本机 Codex PR app dispatcher 监控：start/stop/status/restart/logs/trigger，或主动唤醒立即拉取一次 GitHub PR label。"
argument-hint: "[start|stop|restart|status|logs|trigger]"
allowed-tools: [Bash, Read]
disable-model-invocation: true
---

# Codex PR Dispatcher

Use this skill when the user asks to start, stop, restart, inspect, tail logs, or immediately poll the Codex PR app dispatcher.

Default action is `trigger` when the user only asks to wake or poll once.

Commands:

```bash
bash hack/automation/codex-pr-app-dispatcher/monitor.sh start
bash hack/automation/codex-pr-app-dispatcher/monitor.sh stop
bash hack/automation/codex-pr-app-dispatcher/monitor.sh restart
bash hack/automation/codex-pr-app-dispatcher/monitor.sh status
bash hack/automation/codex-pr-app-dispatcher/monitor.sh logs
bash hack/automation/codex-pr-app-dispatcher/monitor.sh trigger
```

For backward compatibility, this also works:

```bash
bash hack/automation/codex-pr-app-dispatcher/trigger.sh
```

Confirm from the router log when the dispatcher runs under LaunchAgent or another supervisor that redirects stdout:

```bash
tail -30 "${GOCELL_APP_ROUTER_HOME:-$HOME/.local/gocell-pr-app-router}/logs/router.log"
```

Expected signal:

```text
active poll trigger received
```

When running `router.py` directly, confirm from that terminal's stdout instead.

This only wakes one `poll_once`. It must not wait for `turn/completed`, inspect Codex session results, or move PR labels directly.
