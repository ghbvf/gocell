# GoCell v1.0 重排实施计划（架构优先版）

生成时间：2026-04-21 15:00  
输入依据：
- docs/reviews/202604211430-031-three-plans-six-seat-review.md
- docs/backlog.md
- docs/plans/202604191515-auth-federated-whistle.md
- docs/plans/202604200313-v1.0-pre-release-plan.md
- docs/plans/202604201800-pg-pilot-layering-refactor-plan.md

目标：
1. 先完成架构/设计/分层护栏升级，避免继续“修一处漏一处”。
2. 再收口安全与正确性问题。
3. 最后实现新功能与体验增强。

原则：
1. backlog 为唯一状态源，计划仅描述实施路径。
2. 阻塞项必须有“可验证门禁”。
3. 每批次结束都要有 build/test/governance 证据。

## 0. 前置说明（现状）

已完成基线：
1. pg-pilot 主体（R1a-R1e + R2 + L0）已完成。
2. Vault envelope/readiness/renewal/keyID 关键链路已完成。
3. BuildApp + CellModule 分层已完成。

仍需完成的核心项：
1. P0：L1
2. P1：L2、S-nonce、S4b+A14、L11、L7-ex、F10
3. P2改进：L10、L7(FMT15)、L8、A21、S10/S11

## 1. Batch A（先架构/设计/分层）

目标：建立“默认安全 + 默认可验证”的护栏。

### A1 路由策略与分支门禁（阻塞）

任务：
1. L2 ROUTE-POLICY-REGISTRY-01
2. L11 GOVERNANCE-CI-MAINBRANCH-01
3. L7-ex EXAMPLES-STARTUP-SMOKE-01（先最小阻塞集）

验收：
1. 新增路由若无策略，启动失败。
2. governance workflow 覆盖 develop/main/release。
3. examples smoke 纳入 required checks。

建议工时：1.5~2 天

### A2 计划治理收口（阻塞）

任务：
1. 清理三计划与 backlog 状态漂移（仅状态口径，不改功能）。
2. 建立“发布阻塞清单”字段：`releaseGate: true/false`。
3. 编号唯一性检查（修复 L7 重号历史问题）。

验收：
1. backlog 与计划状态一致。
2. 不再出现同一任务多编号/同编号多任务。

建议工时：0.5 天

## 2. Batch B（再处理问题）

目标：关闭 v1.0 安全与正确性风险。

### B1 安全主链（阻塞）

任务顺序：
1. L1 AUDIT-ROUTE-POLICY-01（P0）
2. S-nonce SERVICE-TOKEN-NONCE-STORE-01（P1）
3. S4b + A14 VAULT-AUTH wave（P1/P2，绑定实施）

实施要求：
1. L1 必须与对应 401/403/200 回归测试同批。
2. S-nonce 在 real 模式下未配置 NonceStore 必须启动失败。
3. Vault wave 必须覆盖 auth mode、real-mode static-token reject、续期降级可观测。

验收：
1. 安全阻塞项在 backlog 标记关闭。
2. `go test ./... -tags integration` 通过。

建议工时：2~3 天

### B2 测试与就绪性补强（阻塞->改进）

任务顺序：
1. F10 JOURNEY HARNESS 分阶段恢复
2. A21 HEALTH-CHECKER-CTX-BUDGET-01

分阶段策略：
1. 阶段1（阻塞）：auth/config/audit 核心 journey 全绿
2. 阶段2（改进）：其余 journey 恢复 + flaky 报表

验收：
1. 发布候选分支必须通过阶段1。
2. readyness 检查具备统一 deadline 与错误归因。

建议工时：2~3 天

## 3. Batch C（最后做新功能）

目标：在护栏稳定后推进增量能力。

优先顺序：
1. P1-8 DEVICE-LIST-API
2. F2 SYSTEM-TOPOLOGY-API
3. 其余体验项（S2-follow、L8、L7/FMT15、S10/S11）

实施规则：
1. 每个新功能必须先补 contract 与测试。
2. 不再接受“先功能后门禁”。

建议工时：2~4 天（视范围拆 PR）

## 4. PR 拆分建议

### PR-ARCH-1
1. L2
2. L11
3. L7-ex

### PR-ARCH-2
1. 状态治理（计划/backlog 对齐）
2. 编号唯一性

### PR-FIX-1
1. L1

### PR-FIX-2
1. S-nonce

### PR-FIX-3
1. S4b + A14

### PR-FIX-4
1. F10 阶段1
2. A21

### PR-FEAT-1+
1. P1-8
2. F2
3. 其它增量

## 5. 门禁清单（必须满足）

1. `go build ./...`
2. `go test ./...`
3. `go test ./... -tags integration`
4. `golangci-lint run ./...`
5. `gocell validate --strict`
6. required checks 覆盖 `develop/main/release/**`

## 6. 风险与回滚

风险：
1. 安全波次与测试恢复并行会拉长 CI 时长。
2. Vault auth wave 需要真实环境变量矩阵，容易出现配置抖动。

缓解：
1. 每个批次独立 PR，先小后大。
2. 对条件延后项提供明确触发条件与演练脚本。

回滚：
1. 逐 PR revert，不跨批次回滚。
2. 安全门禁 PR 不与新功能混合，避免回滚连带损失。

## 7. 完成定义（DoD）

v1.0 可发布条件：
1. P0 全清零（至少 L1）。
2. 阻塞 P1 清零（L2、S-nonce、S4b+A14、L11、L7-ex、F10阶段1）。
3. 发布门禁全绿且分支策略生效。
4. 计划与 backlog 状态一致。

---

备注：
- 本计划遵循“先架构、后问题、再功能”顺序。
- 如果资源有限，优先保证 Batch A + Batch B1。