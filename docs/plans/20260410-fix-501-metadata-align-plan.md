# PR-501: Metadata 对齐 + 后续任务路线

> 日期: 2026-04-10
> 分支: fix/501-metadata-align（worktree: worktrees/501-metadata-align）
> 前置: PR#59 (C1), PR#60 (C2), PR#61 (verify 重构) 已合并

---

## PR-501: Metadata 对齐 — 方案 B（测试适配 metadata）

### 已完成

- [x] 修复 `device-cell/smoke` → `smoke.device-cell.startup`
- [x] 修复 `order-cell/smoke` → `smoke.order-cell.startup`
- [x] 推送到 `fix/501-metadata-align` 分支

### Step 1: 删除 verify legacy fallback

**文件:** `kernel/verify/runner.go`
- 删除 VerifyCell 中 legacy `/` fallback 分支（`strings.Contains(ref, "/")`）
- 删除 `integration_test.go` 中 `TestVerifyCell_LegacySmoke`

### Step 2: 补 Cell smoke stub 测试（6 cell）

每个 cell 包补 `TestStartup` stub，匹配 `smoke.{cellID}.startup` → `-run Startup`。

| Cell | 测试文件 |
|------|---------|
| access-core | `cells/access-core/cell_test.go` |
| audit-core | `cells/audit-core/cell_test.go` |
| config-core | `cells/config-core/cell_test.go` |
| demo | `cells/demo/cell_test.go`（新建） |
| device-cell | `cells/device-cell/cell_test.go` |
| order-cell | `cells/order-cell/cell_test.go` |

### Step 3: 补 Slice unit stub 测试（21 slice）

ref 格式: `unit.{sliceID}.service` → `-run Service`。
对没有 `TestService*` 的 slice 补 stub。先 `grep -l "func Test.*Service"` 确认缺哪些。

### Step 4: 补 Slice contract stub 测试（25 unique refs）

ref 格式: `contract.{contractID}.{role}` → `-run {FullyQualifiedCamelCase}`。
例: `contract.http.auth.login.v1.serve` → `TestHttpAuthLoginV1Serve`。

### Step 5: 补 Journey integration test stubs（26 checkRefs）

ref 格式: `journey.{id}.{suffix}` → `-run {CamelCase(suffix)}`。
命名: `TestJourney_{JourneyName}_{Suffix}` 避免重复。
文件: `tests/integration/journey_test.go`（保持 `//go:build integration` tag）。

### Step 6: 验证

```bash
go build ./...
go test ./kernel/verify/... -count=1
go test ./cells/access-core/... -run Startup -v
go test ./cells/access-core/slices/sessionlogin/... -run HttpAuthLoginV1Serve -v
go test -tags=integration ./tests/integration/... -run OidcRedirect -v
```

### 估计: ~35 文件，~250 行

---

## 后续任务排序

PR-501 完成后，按以下顺序执行：

### PR-B5: GOV-5 verify 格式校验 + GOV-6 select-targets L0

**依赖:** PR-501（metadata 格式对齐后才能做格式校验，否则会立刻报格式错误）

| ID | 内容 | 文件 |
|----|------|------|
| GOV-5 | verify/checkRef 标识符格式静态校验（`{prefix}.{scope}.{suffix}` 正则） | `kernel/governance/rules_verify.go` |
| GOV-6 | select-targets 追踪 L0 依赖边（L0 cell 变更 → 选入依赖方） | `kernel/governance/targets.go` |

预估: ~1d

### PR-B6: strict YAML (KnownFields)

**依赖:** PR-501 + PR-B5（metadata 对齐 + 格式校验先就位，避免 strict 解析误报）

| ID | 内容 | 文件 |
|----|------|------|
| F-META-01 | `yaml.Unmarshal` → `yaml.NewDecoder` + `KnownFields(true)` | `kernel/metadata/parser.go` |

**风险:** 可能导致现有 YAML 含未知字段时报错。
**预检:** 先用 strict 模式试解析所有 YAML，统计报错文件。

预估: ~0.5d

### 执行顺序

```
PR-501 (metadata 对齐)
  → PR-B5 (GOV-5/6 格式校验 + targets L0)
    → PR-B6 (strict YAML)
```

---

## Backlog 关联

| Backlog ID | 状态 |
|------------|------|
| CS-F1~F4 | ✅ PR#61 |
| GOV-5 | 待做 (PR-B5) |
| GOV-6 | 待做 (PR-B5) |
| F-META-01 | 待做 (PR-B6) |
