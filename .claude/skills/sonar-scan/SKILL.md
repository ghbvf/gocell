---
name: sonar-scan
description: 从 SonarCloud 获取静态扫描结果（质量门、issue、hotspot），输出到 .claude/tools/findings/static-scan/。
---

# SonarCloud 静态扫描导出

## 行为

### Step 1: 加载 token 并执行脚本

```bash
set -a && source .claude/tools/findings/static-scan/.env && set +a && STATIC_SCAN_INSECURE=true python3 .claude/tools/findings/static-scan/export_static_scan.py
```

### Step 2: 读取结果并汇报

读取 `.claude/tools/findings/static-scan/snapshot.json`，汇报：质量门状态、bugs/vulnerabilities/code_smells/coverage、失败的质量门条件、质量门失败的 PR 列表。
