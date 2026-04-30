# GoCell golangci-lint Tier 1/2 补全优先级与效果预测

> 日期：2026-04-30
> 任务：基于 19 项目工业基线频次矩阵，整理 GoCell 缺失规则的优先级、落地批次切片，以及 Batch 1+2 完成后的效果画像
> 关联文件：
> - `202604290945-ci-baseline-raw-extraction.md`（19 项目 × 90 规则频次矩阵 + Tier 1/2/3 分级）
> - `202604290945-ci-raw-dump-A1.md`（K8s/CRDB/TiDB/Temporal/Vault/Kratos/fx/Watermill 8 项目 raw）
> - `202604290945-ci-raw-dump-B1.md`（Caddy/Pulumi/MinIO/Grafana/NATS/OPA）
> - `202604290945-ci-raw-dump-B2.md`（Cilium/Crossplane/SPIRE/Teleport/Prometheus/Traefik）
> - `202604290900-go-static-analysis-toolchain.md`（推荐工具矩阵 + 完整 .golangci.yml 草案）
>
> **本文为决策文档**：把研究结论收敛为可落地的 4 个 batch + 每个 batch 的预期效果，便于后续 PR 切片实施。

---

## §1 当前 GoCell 已启用清单

`.golangci.yml`（v2 配置）：

```yaml
linters:
  enable:
    - gocognit       # ≤15（CLAUDE.md）
    - nilerr
    - nilnesserr
    - nilnil
  # 默认启用：govet, errcheck, staticcheck, unused, ineffassign, gosimple（含于 staticcheck）
formatters:
  enable:
    - gofmt
```

合计 9 个 linter + gofmt 一个 formatter。

排除规则：
- `_test\.go` 豁免 `errcheck` + `gocognit`
- `^examples/` 豁免 `errcheck`
- `websocket` 豁免 `staticcheck`（`nhooyr.io/websocket` → `coder/websocket` 迁移待办）

---

## §2 规则缺失项分级（按 19 项目频次降序）

### A. 高频缺失（Tier 1 工业共识，**优先**）

| 规则 | 业界频次 | 落地成本 | GoCell 价值 | 落地批次 |
|---|---|---|---|---|
| `goimports` | 18/19 | 零成本 | 替代/补充 gofmt（import 排序），业界事实标准 | **Batch 1** |
| `misspell` | 15/19 | 极低（纯字面拼写） | 注释/标识符拼写，存量预计 < 50 条 | **Batch 1** |
| `unconvert` | 10/19 | 极低 | 删多余类型转换，纯清洁度 | **Batch 1** |
| `revive` | 14/19 | 中（默认会爆量） | 综合规则集；需要先选 ruleset 子集 | **Batch 3** |
| `gosec` | 10/19 | 高（百条级 finding） | JWT/OIDC/加密代码必跑，但要分批修 | **Batch 3** |

### B. 中频缺失（Tier 2 推荐，**结构性强需求**）

| 规则 | 业界频次 | 落地成本 | GoCell 价值 | 落地批次 |
|---|---|---|---|---|
| `depguard` | 9/19 | 中（要写规则） | **GoCell 严格分层（kernel ⊄ runtime/adapters）当前只人工 review + 部分 archtest，业界 9 个项目都靠 depguard 静态守护** | **Batch 2（最有价值）** |
| `errorlint` | 6/19 | 中（清存量） | GoCell 强制 `%w` wrap + errcode，errorlint 保证 `errors.Is/As` 链 | **Batch 2** |
| `forbidigo` | 6/19 | 中（要写 pattern） | 直接落 CLAUDE.md 已有约束：禁 `fmt.Println`/`log.Printf`、禁 `errors.New` 对外、禁 `time.Sleep` 在测试 | **Batch 2** |
| `bodyclose` | 8/19 | 中 | 防 OIDC/S3/webhook HTTP body 泄漏 | **Batch 2** |
| `durationcheck` | 8/19 | 低 | adapters/redis、runtime/worker 时间运算 | **Batch 2** |
| `gofumpt` | 8/19 | 低（全仓 reformat 一次） | 比 gofmt 严，纯格式 | **Batch 4** |
| `gocritic` | 9/19 | 中 | 综合，建议关 `enable-all`，按需启 checker | **Batch 4** |

### C. 低频但 GoCell 已启用（**保留**，构成特色基线）

| 规则 | 业界频次 | GoCell 保留理由 |
|---|---|---|
| `nilnil` | 0/19 | 防接口 nil 歧义，gocell errcode 设计需要 |
| `gocognit` | 0/19 | CLAUDE.md ≤15 |
| `nilerr` | 1/19 | 错误链前瞻 |
| `nilnesserr` | 2/19 | 错误链前瞻 |

### D. 长尾（**暂不动**）

`whitespace`（7）/`prealloc`（7）/`wastedassign`（6）/`copyloopvar`（6, Go 1.22 已自动）/`gci`（6）/`nolintlint`（5）/`testifylint`（5）/`exhaustive`（4）/`govet:nilness`（4）/`sloglint`（4）/`goheader`（4）/`lll`（4）—— 单条价值小、收益分散，按 PR 顺手带，不专门做。

---

## §3 落地批次切片

### Batch 1：零风险开局（1 个 PR）

**新增**：`goimports + misspell + unconvert`

- 预期 finding：< 100 条，全部本 PR 修
- 风险：极低（全是清洁度规则）
- 配置改动：`.golangci.yml` 增 3 条 enable + 1 条 formatter（`goimports`）

### Batch 2：GoCell 结构性收益最大（1-2 个 PR）

**新增**：`depguard + forbidigo + errorlint + bodyclose + durationcheck`

切片建议：
- **PR 2.1**：`depguard + forbidigo`（结构守护双子）
  - depguard 直接编码 CLAUDE.md「依赖规则」章节（kernel/cells/runtime/adapters/pkg 五层），消除人工 review 漂移
  - forbidigo 把 CLAUDE.md 文字约束变成静态门：`fmt.Println`、`log.Printf`、`errors.New` 对外、`_ = func()`、`time.Sleep` 在测试
- **PR 2.2**：`errorlint + bodyclose + durationcheck`（行为收口）
  - errorlint 收口 GoCell 错误链规范（`%w` wrap、`errors.Is/As`）
  - bodyclose 防 HTTP 资源泄漏
  - durationcheck 防时间量纲错误

### Batch 3：存量清理（多 PR 切片）

**新增**：`gosec`（按 G-rule 分批）+ `revive`（按 ruleset 选）

切片建议：
- **PR 3.1**：`gosec` 仅启 G401/G404（弱加密）+ G104（错误未处理）
- **PR 3.2**：`gosec` 扩 G101/G203（hardcoded credential / HTML escape）
- **PR 3.3**：`revive` 启 5-10 条 ruleset：`context-as-argument` / `error-naming` / `unused-receiver` / `var-naming` / `package-comments`
- **PR 3.4**：`revive` 扩规则到 15-20 条

### Batch 4：美化（可延后）

**新增**：`gofumpt`（一次性全仓 reformat）+ `gocritic`（按 checker 选）

---

## §4 Batch 1+2 完成后的效果画像

### 4.1 配置形态对比

| 维度 | 当前 | Batch 1+2 后 |
|---|---|---|
| 启用 linter 数 | 5（默认）+ 4（自定）= 9 | 9 + 8 = **17** |
| Formatter | `gofmt` | `gofmt + goimports` |
| 静态化的 CLAUDE.md 规则 | 0 | 6（分层 / `fmt.Println` / `log.Printf` / `errors.New` / `_ = func()` / `%w` 链） |
| 业界 Tier 1 覆盖率 | 6/11（55%） | 9/11（82%） |
| 业界 Tier 2 覆盖率 | 0/12（0%） | 5/12（42%） |

新增 8 条：`goimports / misspell / unconvert / depguard / forbidigo / errorlint / bodyclose / durationcheck`

### 4.2 静态拦截的具体问题（按 GoCell 场景列）

**通用清洁度（Batch 1）：**
- 注释/标识符拼写错误（`recieve`→`receive` 等英文母语错误）→ misspell
- 多余 `string(s)`/`int(i)` 类型转换 → unconvert
- import 顺序漂移 / 未排序 → goimports

**分层守护（Batch 2 depguard，最大结构性收益）：**
- `kernel/**` 误 import `runtime/`、`adapters/`、`cells/` → 直接 fail（当前要靠人工 review 抓）
- `cells/{cell}/**` 误 import 兄弟 `cells/{other-cell}/internal/`（违反「cell 之间只通过 contract 通信」）→ fail
- `runtime/**` 误 import `cells/`、`adapters/` → fail
- `adapters/**` 误 import `cells/` → fail
- `pkg/**` 误 import `kernel/`、`runtime/`、`cells/`、`adapters/`（pkg 是叶子）→ fail
- 已废弃包：`io/ioutil`、`pkg/errors`、`golang.org/x/exp/*`（已并 stdlib 部分）→ fail

**行为规范（Batch 2 forbidigo，把 CLAUDE.md 文字变静态门）：**
- `fmt.Println` / `fmt.Print` / `log.Printf` 出现在非 cmd/、非 examples/ 路径 → fail
- `errors.New(...)` 出现在导出错误返回路径 → 提示用 `pkg/errcode`
- `_ = someFunc()`（裸忽略错误）→ fail
- `time.Sleep` 出现在 `*_test.go` → fail（用 fake clock）
- `panic(...)` 出现在 cells/、runtime/、adapters/ 业务路径 → fail（kernel 内白名单）

**错误处理（Batch 2 errorlint）：**
- `if err == sql.ErrNoRows`（直接比较）→ 强制 `errors.Is`
- `switch e := err.(type)`（type assertion）→ 强制 `errors.As`
- `fmt.Errorf("ctx: %s", err)`（用 %s 不是 %w）→ fail
- `errors.New(fmt.Sprintf(...))` 反模式 → fail

**资源/时间（Batch 2 bodyclose + durationcheck）：**
- `http.Get(...)` 后未 `defer resp.Body.Close()`（OIDC client / webhook / S3 polling）→ fail
- `time.Duration(60) * time.Second`（误把 second 当 nanosecond）→ fail
- `5 * time.Millisecond * time.Second`（量纲错误）→ fail

### 4.3 业界基线相对位置

完成 Batch 1+2 后，GoCell 在 19 项目矩阵里的相对位置：

```
最简（NATS 6 linter）── GoCell 当前 9 ── GoCell B1+B2 后 17 ── OPA/MinIO 13-15 ── 工业中位 ~18 ── Caddy 29 / K8s+插件 14+脚本 / Crossplane 87 全集
                                            ↑
                              进入工业中位区间（17 vs 中位 18）
```

具体对标（同等量级）：
- **完全同档**：Linkerd 14 / Teleport 14 / TiDB 15 / Kratos 17 / SPIRE 21
- **领先于**：NATS 6 / Watermill 0（无配置）/ Vault 1（仅 depguard）
- **落后于**：Caddy 29 / Pulumi 21 / Cilium 17（含 sloglint）/ Prometheus 21 / Grafana 20 / Crossplane 87 全集

**Tier 1 还差**：`revive`（综合规则集）、`gosec`（安全）—— 都因存量 finding 风险放到 Batch 3。

### 4.4 与 archtest / governance 的职责切分

GoCell 已有 `tools/archtest/` 和 `kernel/governance/` 在做部分守护，引入 depguard 后职责重整：

| 守护层 | 负责范围 | 触发时机 | 与 depguard 的关系 |
|---|---|---|---|
| **depguard（lint 期）** | 包级 import 黑/白名单（kernel ⊄ runtime 等纯静态规则） | 每次 push（lint job） | 接管包级静态规则 |
| **archtest（go test）** | typed go/types 流分析（OBS-01 errcode→metric label taint、CI pinning 等需要类型信息的） | CI hard gate | 留 typed 部分，不再做包级黑名单 |
| **governance（runtime）** | metadata 解析期校验（slice.yaml ↔ contract.yaml 双向、cell.yaml 必填字段） | 启动期 / `gocell validate` | 与 lint 无重叠 |

**避免重复**：depguard 不去做 OBS-01 之类需要 typed flow 的事；archtest 不再做包级 import 黑名单（如有冗余规则迁到 depguard）。引入 depguard 时同步审查现有 archtest 是否能简化。

### 4.5 遗留缺口（Batch 1+2 不覆盖）

- **安全扫描全部缺位**（gosec 押到 Batch 3）—— JWT/OIDC/AES/HMAC 代码无静态扫描
- **综合 lint 覆盖薄弱**（revive 押到 Batch 3）—— 命名/注释/未用参数等漏检
- **格式严格度仅到 gofmt 级别**（gofumpt 押到 Batch 4）
- **测试规范未守护**（testifylint / paralleltest 在 Batch 4 之后）
- **nolint 滥用未守护**（nolintlint 长尾，不进 batch）

---

## §5 每个 Batch PR 必须自带的三件套

按 user feedback「引入新约束必须同 PR 闭环」要求，每 batch PR 必须包含：

1. **`.golangci.yml` 启用规则**（静态守卫）
2. **修光本 PR 触发的所有 finding**（不允许 baseline-only / `--new-from-rev` 仅检查新增代码）
3. **对应的契约同步**：
   - depguard 规则同步进 CLAUDE.md「依赖规则」章节，让人和工具说同一种话
   - forbidigo 规则同步进对应 `.claude/rules/gocell/*.md`（error-handling / observability / eventbus）
   - 必要时新增 archtest 锁定 lint 配置不被静默削弱（防 nolint 滥用）

---

## §6 Batch 1 落地前置：存量盘点

下一步建议：

```bash
# 在 worktree 跑一遍，量真实 finding 数
golangci-lint run --no-config \
  --enable goimports,misspell,unconvert \
  --disable-all \
  ./...
```

预期 finding 量级：
- `goimports` 0-20 条（多数文件已 gofmt，主要是 import 分组顺序差异）
- `misspell` 0-50 条（注释拼写错误）
- `unconvert` 0-30 条（多余类型转换）

如果总数 < 150 条，Batch 1 可以一个 PR 修完；超过则按规则切两个 PR。

---

## §7 决策点（待确认）

- [ ] **是否同意按 Batch 1→2→3→4 顺序推进？**
- [ ] **Batch 1 是否立即开 worktree 跑存量？**
- [ ] **depguard 规则是否同步更新 CLAUDE.md「依赖规则」章节为权威源？**（建议同 PR）
- [ ] **forbidigo 规则源头是否锁定在 `.claude/rules/gocell/*.md` + CLAUDE.md，由这两份文档驱动 .golangci.yml 配置？**

---

## 附录 A：依据数据

频次矩阵直接来源：`202604290945-ci-baseline-raw-extraction.md` §2（19 项目 × 90 规则）+ §3（Tier 1/2/3 分级）+ §4（GoCell 缺失对照）。

8 项目 raw 完整内容：`202604290945-ci-raw-dump-A1.md`（含 K8s 14 linter / CRDB 67 条禁令 / Vault semgrep 8 文件 / Kratos / fx / Watermill / TiDB / Temporal）。

12 项目 raw 完整内容：`202604290945-ci-raw-dump-B1.md` + `B2.md`。

工具版本与推荐 .golangci.yml 草案：`202604290900-go-static-analysis-toolchain.md` §2。
