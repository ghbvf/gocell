# CI 治理研究 / golangci-lint 升级决策

> 状态：研究 → 矩阵 → 决策三层链路完整，可进入实施
> 日期：2026-04-29 ~ 2026-04-30
> 目标：基于 19 个工业 Go 项目的 CI 配置基线，把 GoCell `.golangci.yml` 从 9 条 lint 升到 17 条（Tier 1+2 覆盖 82%/42%），并把 CLAUDE.md 文字规则静态化

## 链路概览

```
源头                    候选筛出                research（4 视角）          raw dumps（19 项目）       synthesis              decision
──────────────────      ──────────────────     ─────────────────────       ──────────────────────     ──────────────         ──────────────
bak/ 4 份归档审查 →     backlog2 → CI         cncf-ci-rules-research      ci-raw-dump-A1（K8s/        ci-baseline-raw-       golangci-tier12-
docs/backlog2.md        治理候选筛出           distsys-ci-rules-research   CRDB/TiDB/Temporal/         extraction.md          priority-and-
（70 条新增，4 份       （202604290858-        go-static-analysis-         Vault/Kratos/fx/             (19 × 90 频次          projection.md
bak 归档合并去重）      backlog2-ci-           toolchain                   Watermill 8 项目）          矩阵 + Tier 1/2/3      （Batch 1-4 切片
                       governance-           framework-comparison-       ci-raw-dump-B1（Caddy/      分级 + GoCell 缺失    + 完成后效果画像
                       analysis.md）          ci-research                 Pulumi/MinIO/Grafana/       对照）                + 三件套要求）
                                                                          NATS/OPA）
                                                                          ci-raw-dump-B2（Cilium/
                                                                          Crossplane/SPIRE/
                                                                          Teleport/Prom/Traefik）
```

## 文档清单

### 决策层（先读）

- **`202604300430-golangci-tier12-priority-and-projection.md`** —— 决策文档
  - 当前 GoCell 已启用清单
  - 缺失规则 A/B/C/D 四档分级（按 19 项目频次降序）
  - Batch 1-4 PR 切片建议
  - Batch 1+2 完成后的配置形态/拦截清单/相对位置/职责切分
  - 与 archtest/governance 边界
  - 每 batch PR 三件套要求（静态守卫 + 同 PR 修光 + 契约同步）

### 综合层（决策依据）

- **`202604290945-ci-baseline-raw-extraction.md`** —— 19 项目 × 90 规则频次矩阵 + Tier 1/2/3 工业基线分级 + GoCell 当前缺失对照

### 源数据层（raw 完整提取，不归纳）

- **`202604290945-ci-raw-dump-A1.md`** —— K8s/CRDB/TiDB/Temporal/Vault/Kratos/fx/Watermill 8 项目（含 CRDB lint_test.go 67 条禁令、K8s 14 linter + 3 verify 脚本、Vault semgrep 8 文件清单）
- **`202604290945-ci-raw-dump-B1.md`** —— Caddy/Pulumi/MinIO/Grafana/NATS/OPA 6 项目
- **`202604290945-ci-raw-dump-B2.md`** —— Cilium/Crossplane/SPIRE/Teleport/Prometheus/Traefik 6 项目

### 调研层（多视角）

- **`202604290900-cncf-ci-rules-research.md`** —— K8s/Istio/Argo/etcd/containerd/Linkerd/OpenTelemetry-Collector-Contrib 等 CNCF 项目视角
- **`202604290900-distsys-ci-rules-research.md`** —— TiDB/CockroachDB/Temporal/Vault/Consul/Dapr/Etcd 等分布式系统/数据库视角
- **`202604290900-go-static-analysis-toolchain.md`** —— Go 静态分析工具栈矩阵 + 推荐 .golangci.yml 完整草案
- **`202604290915-framework-comparison-ci-research.md`** —— `docs/references/framework-comparison.md` 列出的 6 个对标框架（fx/go-zero/Kratos/go-micro/Watermill/Kubernetes）+ 13 个官方组件库视角

### 候选筛出层（研究起点）

- **`202604290858-backlog2-ci-governance-analysis.md`** —— 从 backlog2 70 条新增问题中识别 31 条 CI 自动拦截候选；按治理类型（archtest / contract validate / waiver / lint / 测试 required job）分组；4 视角调研由此出发

### 源头（研究链路上游）

- **`../../backlog2.md`**（`docs/backlog2.md`）—— 70 条新增 backlog（基线 `develop @ 4e2e00ad`，`bak/` 4 份归档审查合并去重产物）
  - 来源：`bak/20260426-develop-cross-layer-six-role-review/`、`bak/20260426-layered-six-role-review/`、`bak/20260427-module-dataflow-six-role-review/`、`bak/20260427-per-relation-six-role-review/`

## 推荐阅读顺序

1. **快速理解**：先读 `202604300430-golangci-tier12-priority-and-projection.md`（决策文档，自带数据指针）
2. **验证频次**：跳到 `202604290945-ci-baseline-raw-extraction.md` §2 频次矩阵 / §3 Tier 分级
3. **质疑某条规则**：去对应 raw dump 看原始证据（A1/B1/B2 按项目分 §）
4. **理解工具能力**：读 `202604290900-go-static-analysis-toolchain.md` §1 工具矩阵 + §2 推荐配置
5. **理解多视角**：CNCF / 分布式 / 框架三份调研按需读

## 实施状态

| Batch | 内容 | 状态 |
|---|---|---|
| 1 | `goimports + misspell + unconvert`（零风险开局） | 未开始 |
| 2 | `depguard + forbidigo + errorlint + bodyclose + durationcheck`（结构性收益） | 未开始 |
| 3 | `gosec + revive`（存量清理，多 PR 切片） | 未开始 |
| 4 | `gofumpt + gocritic`（美化） | 未开始 |

## 维护规则

- **不要**在 `docs/plans/` 顶层再放 CI 治理相关文档，统一进本目录
- **新增项目调研**：补到 `ci-raw-dump-{batch}.md`，并在 `ci-baseline-raw-extraction.md` 频次矩阵补列
- **Tier 划分变化**：同步更新 `202604300430-golangci-tier12-priority-and-projection.md` §2 表格
- **Batch 实施完成后**：更新本 README §「实施状态」表格 + 在决策文档对应 batch 段加链接到 PR
- **跨文件链接**：本目录内用纯文件名（如 `` `202604290945-ci-raw-dump-A1.md` ``）；引用 `docs/backlog2.md` 等目录外文件用 `../../` 相对路径，不要写绝对仓库路径
