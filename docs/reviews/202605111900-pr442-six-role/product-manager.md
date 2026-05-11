# PR #442 — 产品经理维度审查

> 视角：GoCell 框架消费者（Go 开发者）；基线：develop @ a54d0c77（PR #442 已 MERGED）

## 总体结论

**有条件通过 / 多处声明与实物偏差**。从 develop 实物核验，PR #442 仅落地了 `scaffold cell` 的 bundle 升级（owner.team/role/type/consistencyLevel 一次写入 + struct/module 派生），**未交付 PR body 自陈的 `scaffold assembly` 子命令**，因此对 K#09「吸收 K-08(b)」的关键验收条款未达成。叠加 backlog 三个 carve-out 全部未登记、roadmap 未更新、README 未提及新 flag 三处闭环缺失，按 P1 验收口径（100% PASS）判为 **产品 FAIL**。功能本体（cell 一键骨架）实现优雅，无 breaking 风险，但产品验收闭环不完整。

## Finding 列表

### F1 [Cx3] [验收标准缺失] K#09 关键子命令 `scaffold assembly` 未交付
**位置**：`cmd/gocell/app/scaffold.go:63-74`、`cmd/gocell/app/help.go:172-194`
**问题**：roadmap 第 K#09 项原始验收要求"吸收 K-08(b)：加 `gocell scaffold assembly --id=... --cells=... --deploy=k8s` 一键产出可编译 assembly"（archive/202605011500-029-master-roadmap.md:280；202605101839 残余版 line 27 仍标"待办"）。但 `runScaffoldWithRoot` switch 仅含 cell/slice/contract/journey 四分支，无 assembly case；`printScaffoldHelp` 同步只列 4 类型。`backlog.md:327` 的 `ASSEMBLY-SCAFFOLD-CMD-01` 条目仍标 OPEN 即旁证。
**证据**：`Grep "case \"assembly\"" cmd/gocell/app/scaffold.go` 无匹配；`Glob cmd/gocell/app/scaffold_assembly*.go` 无文件。
**建议**：要么补 `scaffold assembly` 实现并补 P1 验收测试（dry-run + 实写双覆盖 + --deploy 三模板 golden），要么显式将 K-08(b) 从 K#09 范围中剔除并把 K#09 改名（不再声称"吸收 K-08(b)"），同时把 roadmap K#09 标 ✅。
**Backlog 登记**：现存 `ASSEMBLY-SCAFFOLD-CMD-01` 已覆盖该残留；需把 PR body 与 roadmap 同步改口径。

### F2 [Cx2] [范围偏移] PR body 自陈与 develop 实物不符
**位置**：PR #442 description 自陈"ContractMeta.Codegen 默认翻转 false→true"
**问题**：`kernel/metadata/types.go:140` 仍为 `Codegen bool \`yaml:"codegen,omitempty"\``，`parser_test.go:1402-1413` 显式锁定"contract without codegen defaults false"，未翻转。如真翻转，64+ 已带 `codegen: true` 的 contract 会在 reformat 时被 omitempty 抹掉，产生 silent drift。
**证据**：上述源码与测试。
**建议**：以 develop 实物为准 — Codegen 默认未翻转、carve-out `CONTRACT-YAML-CODEGEN-DEFAULT-CLEANUP` 前提不成立，PR body 该段需更正或在六角色复审记录中纠偏。
**Backlog 登记**：N/A（前提不存在）。

### F3 [Cx2] [开发者体验] 文档完全缺失新 scaffold cell 形态
**位置**：`README.md:100-150`、`docs/guides/`
**问题**：README §Step 1 仍教用户"`mkdir -p cells/mycell/slices/myhello` + 手写 cell.yaml + 手写 slice.yaml"（line 103-164），未提到 `gocell scaffold cell --id=mycell --team=... --role=...` 能一条命令做完。新用户按 README 走根本接触不到 K#09 红利；K#09 投入 22h dev 但消费者发现路径为 0。
**证据**：`Grep "gocell scaffold" README.md` 0 命中；`docs/` 0 文件涉及新 flag。
**建议**：README §Step 1 改为脚手架优先路径 + 手写 fallback；新增 `docs/guides/scaffold-cell.md` 演示完整 bundle 产出 + `go test ./cells/foo/...` 立即通过的验收例。
**Backlog 登记**：可挂在 F1 PR-V1-POSITIONING-AND-LIBS（已记 README 漂移修复）下作为 sub-bullet。

### F4 [Cx2] [范围偏移] 三个 PR 内 carve-out 全部未登记 backlog
**位置**：`docs/backlog.md`
**问题**：PR body 声明三个 carve-out：`EXAMPLES-ASSEMBLY-MINIMAL-CLEANUP` / `SCAFFOLD-PROJECT-INIT-CMD` / `CONTRACT-YAML-CODEGEN-DEFAULT-CLEANUP`。`Grep` 全 backlog 无任何一条登记。直接违反 user memory `feedback_pr_scope_carveouts_must_backlog`「PR 范围切割必须显式 backlog，不能 silent」。
**证据**：`Grep "EXAMPLES-ASSEMBLY-MINIMAL-CLEANUP|SCAFFOLD-PROJECT-INIT-CMD|CONTRACT-YAML-CODEGEN-DEFAULT-CLEANUP" docs/backlog.md` 0 命中。
**建议**：补三行 backlog（feat 类型，P2/Cx2，触发条件分别为"第 2 个 assembly 出现 / 第三方用户接入 / 残余 codegen:true 字面量重构窗口"），引用 PR #442 来源。
**Backlog 登记**：本 finding 直接产出三条 backlog 条目。

### F5 [Cx2] [验收标准缺失] "从 0 启程"用户旅程断裂
**位置**：消费者旅程层面
**问题**：完整 onboard 路径应为 `scaffold project → scaffold cell → scaffold assembly`，PR body 承认 `scaffold project` 在 backlog `SCAFFOLD-PROJECT-INIT-CMD`（且未登记，见 F4），实物又缺 `scaffold assembly`（F1）。K#09 实际只解决了五步中的第 2 步 cell 创建一项，"一键脚手架"的 elevator pitch 未实现。新用户从空目录 `go get` 后无任何 CLI 引导，仍需照 README 手抄。
**证据**：F1 + F3 + F4 复合证据。
**建议**：K#09 验收应明确分层 — 当期交付 cell bundle（OK）；assembly + project init 两段拆 K#09b/K#09c 子里程碑，roadmap 显式列。
**Backlog 登记**：需补 `K#09b SCAFFOLD-ASSEMBLY-SUBCMD` + `K#09c SCAFFOLD-PROJECT-INIT` 两条（或合并 F1 / F4 已建议条目）。

### F6 [Cx1] [开发者体验] CLI help 信息对 cell 必填字段语义不足
**位置**：`cmd/gocell/app/help.go:172-194`
**问题**：scaffold cell help 写 `--team=<team>`，未说明这是 owner team，未提示 `--role` 必填；用户首次 invoke 失败时只看到 `--role is required` 错误（scaffold.go:115）后回头再读 help 才能定位。kubebuilder 对标做了 inline 例子。
**证据**：help.go:173-176 entry 完整文本仅两行 + flag list。
**建议**：help entry desc 追加"--role=<owner-role> e.g. cell-owner（required）"；考虑 `gocell scaffold cell --help` 输出完整最小可用例。
**Backlog 登记**：Cx1 直接修，不必 backlog。

### F7 [Cx2] [验收标准缺失] 7 个 test plan 缺端到端用户路径验证
**位置**：PR #442 test plan checkbox
**问题**：7 项中 4 个 archtest（静态守卫） / 2 个 hack 脚本（drift 检查） / 1 个 golangci。无一条覆盖"新用户跑 `gocell scaffold cell --id=foo --team=t --role=r` 然后 `go test ./cells/foo/...` 立即 PASS"的端到端 journey。该 journey 恰是 K#09 elevator pitch 的核心验收，缺失等于 P1 验收无证据。
**证据**：`cmd/gocell/app/scaffold_golden_test.go` + `scaffold_verify_test.go` 是 golden + dry-run 验证，未跑生成产物的 `go test`。
**建议**：新增 `cmd/gocell/app/scaffold_e2e_test.go`（build tag `integration`），temp dir 跑实写 + `go build ./cells/foo/...` + `go test`，作为 K#09 P1 验收 oracle。
**Backlog 登记**：登记 `SCAFFOLD-CELL-E2E-INTEGRATION-01`（test 类，P1/Cx2，触发条件即时）。

## 评审维度评分

| 维度 | 评级 | 证据 |
|------|------|------|
| A. 验收标准覆盖率 | 红 | F1/F5/F7 — assembly 子命令未交付、端到端旅程未覆盖 |
| B. UI 合规检查（CLI 角度） | 黄 | F6 — help 信息可改进 |
| C. 错误路径覆盖率 | 绿 | dry-run + 必填校验完整 |
| D. 文档链路完整性 | 红 | F3 — README/guides 0 同步 |
| E. 功能完整度 | 红 | F1 — assembly 子命令缺 |
| F. 成功标准达成度 | 黄 | cell bundle 单点完成，整体 onboarding pitch 未兑现 |
| G. 产品 Tech Debt | 黄 | F4 — 三 carve-out silent carry-over |

## 产品验收确认结果

| 检查项 | 状态 |
|--------|------|
| 产品上下文已定义 | PASS |
| 验收标准已分级 | FAIL（roadmap K#09 整体作为单项，未拆 P1/P2/P3） |
| P1 验收 100% PASS | FAIL（F1/F5/F7） |
| P2 无 FAIL | FAIL（F3 文档 / F4 backlog 闭环） |
| 评审无红色维度 | FAIL（A/D/E 红） |

**判定：产品 FAIL**。最小修复闭环：(1) F1 补 `scaffold assembly` 或显式剥离范围；(2) F3 补 README 入口；(3) F4 登记三条 backlog；(4) F7 补端到端测试。完成后可重审。
