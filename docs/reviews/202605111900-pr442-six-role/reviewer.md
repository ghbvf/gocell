# PR #442 — Reviewer 六维度审查

## 总体结论

**需修复**。核心交付（cell/slice/contract/journey scaffold 命令 + path traversal + symlink guard + YAML 类型校验）质量较高，测试覆盖全面，dry-run 机制完整。但有以下问题：

1. `kernel/scaffold` 自由文本字段（`Goal`、`OwnerTeam`）未做 YAML-unsafe 字符过滤，已 backlog（G-11）但本 PR 未修复
2. `cellgen.ScaffoldCell` 全部错误走 `fmt.Errorf` 而非 `pkg/errcode`，与仓库规范不符
3. `gocell scaffold assembly` 子命令属 K#09 原始 scope 但未交付（与 product-manager F1 同向）
4. `hack/verify-scaffold-reject.sh` 未接入 CI（与 devops F1 同向）

---

## Finding 列表

### 安全

#### F1 [P0] [Cx2] `kernel/scaffold` 自由文本字段未做 YAML-unsafe 字符过滤
**位置**：`kernel/scaffold/scaffold.go:233–240` / `kernel/scaffold/templates/journey.yaml.tpl:2,5` / `kernel/scaffold/templates/cell.yaml.tpl:5`
**问题**：`JourneyOpts.Goal`、`CellOpts.OwnerTeam`、`JourneyOpts.OwnerTeam` 在 `validatePathComponent` 中仅校验 path-traversal 和分隔符，未拒绝 `\n \r : # [ {` 等 YAML-unsafe 字符。模板中以裸 scalar 输出（`team: {{.OwnerTeam}}`、`goal: "{{.Goal}}"` 仅双引号），攻击者传入 `platform\ninjected: true` 即可在 YAML 中注入额外键。`kernel/scaffold` 层没有 `cellgen` 的 `ownerTeamPattern` 白名单等价校验。
**证据**：
- `kernel/scaffold/scaffold.go:115` 仅判空
- `kernel/scaffold/templates/cell.yaml.tpl:5` 裸 scalar 无引号
- `docs/backlog.md` G-11 `SCAFFOLD-FREETEXT-YAML-INJECTION`（P1/Cx2 🟡）已记录但未修
**建议**：新增 `validateFreeText(value, field) error` 拒绝 `\n\r":#[]{}|>`；模板改单引号包裹（`team: '{{.OwnerTeam}}'`）；补 `TestCreateJourney_YAMLInjection` 对抗测试。G-11 应升 🔴 发布阻塞。
**AI-rebust 评级**：当前 Soft（无校验）→ Medium（whitelist + 单引号 scalar）。
**Backlog 登记**：G-11 升级。

#### F2 [P1] [Cx1] `cellgen.ScaffoldCell` 错误未走 `pkg/errcode`
**位置**：`tools/codegen/cellgen/scaffold.go:130` 及周边 26 处
**问题**：26 处错误全走 `fmt.Errorf`，违反 CLAUDE.md "错误用 `pkg/errcode` 包"。冲突检测错误直接将绝对路径拼入 message（而非 `WithInternal`）。`kernel/scaffold` 层使用 `errcode.New(ErrScaffoldConflict, ..., WithInternal(...))`，两层不对称。`--id` 传 kebab 时没有 friendly message。
**证据**：
- `tools/codegen/cellgen/scaffold.go:130` vs `kernel/scaffold/scaffold.go:277` 不对称
**建议**：改走 `errcode.New`，路径进 `WithInternal`；kebab 拒绝时给出 `"cell ID contains '-'; use no-dash identifier (e.g. %s)"` 提示。
**Backlog 登记**：可纳入 `SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01`。

#### F3 [P1] [Cx1] `ScaffoldCell` 冲突检查 TOCTOU race
**位置**：`tools/codegen/cellgen/scaffold.go:128–132` / `kernel/scaffold/scaffold.go:271–278`
**问题**：两处都是 `os.Stat` 判存在 → `os.MkdirAll` + `os.WriteFile`，存在 check-then-act race。存档 review `R1B-5-kernel-assembly-scaffold-journey.md:267` 已提到应使用 `os.OpenFile(O_CREATE|O_EXCL|O_WRONLY)` 原子创建，PR #442 未修。
**证据**：`tools/codegen/cellgen/scaffold.go:199` 裸 `os.WriteFile` 无 O_EXCL
**建议**：改 `os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)`。注意 `pkg/pathsafe.WritePlannedFiles` 已正确使用 `O_NOFOLLOW|O_EXCL`——非 funnel 路径需同等改造或纳入 funnel。

---

### 测试

#### F4 [P1] [Cx2] `hack/verify-scaffold-reject.sh` 未接入任何 CI workflow
**位置**：`hack/verify-scaffold-reject.sh` / `.github/workflows/_build-lint.yml`
**问题**：PR 新增了 `hack/verify-scaffold-reject.sh`，但 `ci.yml`、`pr-check.yml`、`_build-lint.yml`、`governance.yml` 均未引用。脚本永不运行，kebab 回归防护形同虚设。对比 `hack/verify-codegen-cell.sh` 在 `_build-lint.yml` verify-codegen job 显式调用。
**证据**：`grep -r "scaffold" .github/workflows/` 0 匹配；devops F1 / F3 同向确认 verify 路径 CI 缺口。
**建议**：在 `_build-lint.yml` 加 `- name: Verify scaffold rejection (kebab guard); run: ./hack/verify-scaffold-reject.sh`；或改写为 Go table test 并入 `cmd/gocell/app` 包测试。

#### F5 [P1] [Cx1] `kernel/scaffold` 部分写入失败测试缺失
**位置**：`kernel/scaffold/scaffold_test.go` / `kernel/scaffold/scaffold.go:161–172`
**问题**：`CreateSlice` 先写 `slice.yaml` 再写 `handler.go`，注释承认 "Failure here would leave slice.yaml on disk"。无测试覆盖第二步失败情形。
**建议**：增 partial-write 测试：预先写 `handler.go` 路径触发 conflict，断言 `slice.yaml` 已写入且函数返回 conflict error；godoc 明确"partial write 行为契约"。

---

### 运维

#### F6 [P2] [Cx1] `cell.yaml` scaffold 默认 `consistencyLevel` 不一致
**位置**：`tools/codegen/cellgen/scaffold.go:118` (L1) vs `kernel/scaffold/scaffold.go:123` (L2) vs `cmd/gocell/app/scaffold.go:100` flag default (L2)
**问题**：CLI 路径最终输出 L2（flag 覆盖），但 `cellgen.ScaffoldCell` 被单独调用时（API 用法）默认 L1，与文档期望不一致；测试 `TestScaffoldCell_TypeAndLevelRendered` "defaults when empty" 锁定 L1，将不一致固化进测试。
**建议**：统一 cellgen 默认为 L2 并更新测试；或在 cellgen 文档明确 "library default L1，CLI flag 注入 L2"。

---

### DX

#### F7 [P1] [Cx2] `gocell scaffold assembly` 在 K#09 scope 内但未交付
**位置**：`cmd/gocell/app/scaffold.go:63–74` / `docs/plans/202605101839-029-master-roadmap.md:27,40`
**问题**：029 roadmap K#09 原始 scope 含 `scaffold assembly` 子命令，PR body 声称 "Closes K#09 absorbing K-08(b)"。但 `cmd/gocell/app/scaffold.go` switch 只有 cell/slice/contract/journey，**没有 assembly**。backlog `ASSEMBLY-SCAFFOLD-CMD-01` 仍 🟠 OPEN，触发条件为"加第 2 个 assembly"。
**证据**：product-manager F1 / F2 / F5 同向独立确认。该 finding 与 PR body 内容（Round-6 提到 `scaffold_assembly.go`、`PlanAssemblyScaffold` 等实现）存在显著不一致——需 cross-check：(a) PR body 声明但 develop HEAD 未含；(b) develop @ a54d0c77 不是 PR 合入后的最新状态。
**建议**：reviewer 直接 `git show <merge-sha>:cmd/gocell/app/scaffold_assembly.go` 复核 merge 时是否真的包含该文件。若 PR body 与代码事实不符，须明示。

#### F8 [P2] [Cx1] `--help` 输出 scaffold cell 未列 `--role` 必填
**位置**：`cmd/gocell/app/help.go:173–175`
**问题**：`printScaffoldHelp` cell 条目只列了 `--id --team`，未列 `--role`（实际是必填）。
**建议**：改为 `--id=<id> --team=<team> --role=<role>`。

---

### 产品

#### F9 [P2] [Cx1] dry-run 不验证 YAML 渲染合法性
**位置**：`kernel/scaffold/scaffold.go:304–305`
**问题**：dry-run 完整执行模板 render 但不解析 YAML。F1 修复（freetext 过滤）前 dry-run 不能发现注入错误——用户期望 dry-run 保证输出可用，实际上不能。
**建议**：dry-run render 后用 `yaml.v3` 解析渲染结果；F1 修复后可天然消除。

---

### 分层合规

#### F10 [P1] [Cx2] `cellgen.ScaffoldCell` 多处 `//nolint:gocognit,cyclop,funlen` 豁免
**位置**：`tools/codegen/cellgen/scaffold.go:108,220`
**问题**：PR body Round-4 声称 "lint cleanup — split high-complexity funcs"，但 merge 后代码仍存 `//nolint:gocognit,cyclop,funlen` 豁免。CLAUDE.md "函数认知复杂度 ≤ 15" 被 nolint 绕过，问题未实质解决。
**建议**：`validateScaffoldSpec` 拆为 `validateIdentFields` + `validateOwnerFields` + `validateEnumFields`；`ScaffoldCell` 拆出 `applySymlinkGuard()` 后可消 funlen。

---

## 复杂度汇总

| 等级 | 数量 | Findings |
|------|------|----------|
| Cx1 | 6 | F2 F3 F5 F6 F8 F9 |
| Cx2 | 4 | F1 F4 F7 F10 |

## 优先级汇总

| 优先级 | 数量 | Findings |
|--------|------|----------|
| P0 | 1 | F1（YAML 注入防护） |
| P1 | 5 | F2 F3 F4 F5 F7 F10 |
| P2 | 3 | F6 F8 F9 |
