# 阶段 5 六角色基线审查报告

**审查日期**: 2026-04-05
**审查基线**: develop 分支 commit 2014298
**范围**: `cells/`（3 cell + 16 slices）、`contracts/`（12 contracts）、`journeys/`（8 journeys + status-board）、`assemblies/`（core-bundle）、`actors.yaml`

## Executive Summary

- 总 finding 数: 20（P0: 1, P1: 7, P2: 12）
- 合流阻塞项: 1 个 P0（孤立 contract http.x.v1）
- Signoff: **带条件通过** — 删除孤立 contract 后可通过

## 基线验证（工具+DX 补充）

| 检查项 | 结果 |
|--------|------|
| 禁用 V2 字段名（cellId/sliceId/contractId） | 0 命中 ✅ |
| 所有 slice 有 contractUsages | 16/16 ✅ |
| 所有 event contract 有 replayable/idempotencyKey/deliverySemantics | 8/8 ✅ |
| 所有 slice 有 verify 节 | 16/16 ✅ |
| YAML 格式一致性 | 一致 ✅ |

---

## 架构师 Findings

### F-5A-01: 孤立 contract http.x.v1 — ownerCell=test-cell 不存在
- **P0** | BUG | `contracts/http/x/v1/contract.yaml`
- 描述: ownerCell 为 test-cell，项目中无此 cell。lifecycle: draft 但无 slice 引用。应删除。

### F-5A-02: http.auth.me.v1 已定义但无 slice 实现
- P1 | DESIGN | `contracts/http/auth/me/v1/contract.yaml`
- 描述: 被 boundary.yaml 导出但无 contractUsages 引用。缺 schema 文件。

### F-5A-03: 所有 cell 的 l0Dependencies 为空但实际存在跨 cell 依赖
- P2 | NIT | `cells/*/cell.yaml`
- 描述: access-core 调用 config-core 的 http.config.get.v1，但 l0Dependencies: []。

### F-5A-04: Assembly cells 顺序未反映依赖关系
- P2 | NIT | `assemblies/core-bundle/assembly.yaml`

### F-5A-05: session-refresh 的 contract 100% waivered
- P2 | DESIGN | `cells/access-core/slices/session-refresh/slice.yaml`

---

## 领域专家 Findings

### F-5D-01: session-login 跨 cell 同步调用缺验证覆盖
- P1 | DESIGN | `cells/access-core/slices/session-login/slice.yaml`
- 描述: 对 http.config.get.v1 的 call 角色使用 waiver 而非 verify.contract。

### F-5D-02: J-config-hot-reload 声明 audit-core 参与但 event.config.changed.v1 无 audit-core 订阅
- P1 | DESIGN | `journeys/J-config-hot-reload.yaml` vs `contracts/event/config/changed/v1/contract.yaml`

### F-5D-03: audit-core 无 slice 订阅 event.config.rollback.v1
- P1 | DESIGN | `contracts/event/config/rollback/v1/contract.yaml`
- 描述: subscribers 声明含 audit-core，但无 slice 实现。

### F-5D-04: passCriteria checkRef 实现位置不清晰
- P2 | DESIGN | `journeys/J-session-refresh.yaml`

### F-5D-05: status-board 与 journey 定义基本对齐
- P2 | NIT | `journeys/status-board.yaml`
- 描述: 8 个 journey 全部有对应 status-board 条目，状态合理（J-sso-login=doing，其余=todo）。

---

## 工具工程师 Findings（Grep 补充）

### F-5T-01: 全局引用完整性——基本通过
- P2 | NIT | 全局
- 描述: 16 个 slice 都有 contractUsages；所有 event contract 必填字段完整。需 `gocell validate` 做最终机器验证。

### F-5T-02: contract version 与目录路径无自动校验
- P1 | DESIGN | 所有 contract.yaml
- 描述: 目录中的 v1 与 ID 中的 v1 一致性依赖人工保证，无 governance 规则检查。

---

## DX Findings（Grep 补充）

### F-5X-01: YAML 格式一致性良好
- P2 | NIT | 全局
- 描述: 缩进统一 2 空格，无混用。命名直观（session-login, config-read 等）。

### F-5X-02: 新增 cell/contract 有清晰范式
- P2 | NIT | 全局
- 描述: 3 个 cell × 16 个 slice 提供了充分的参考范式。

---

## 魔鬼代言人 Findings

### F-5S-01: http.auth.me.v1 导出但无实现——对外虚假承诺
- P1 | DESIGN | `contracts/http/auth/me/v1/contract.yaml` + `boundary.yaml`

### F-5S-02: session-login 混用 HTTP call + event publish 但 call 仅 waiver 覆盖
- P1 | DESIGN | `cells/access-core/slices/session-login/slice.yaml`

### F-5S-03: 事件订阅方声明 vs 实现不一致（audit-core + rollback）
- P1 | DESIGN | `contracts/event/config/rollback/v1/contract.yaml`

### F-5S-04: boundary.yaml 导出内部审计事件
- P1 | DESIGN | `assemblies/core-bundle/boundary.yaml`
- 描述: event.audit.appended.v1 和 event.audit.integrity-verified.v1 subscribers 为空，不应导出。

---

## PM Findings

### F-5P-01: "我是谁" 端点缺 journey 覆盖——MVP 验收漏洞
- P1 | DESIGN | `contracts/http/auth/me/v1` + `journeys/`
- 描述: SSO 登录后需查询用户信息，但无 journey 覆盖此链路。

### F-5P-02: 缺账户解锁 journey
- P2 | DESIGN | `journeys/`
- 描述: J-account-lockout 覆盖锁定但无解锁路径。

### F-5P-03: J-config-rollback 审计追踪承诺与实现不匹配
- P2 | DESIGN | `journeys/J-config-rollback.yaml`

### F-5P-04: boundary.yaml 导出清单不清晰——含未实现和内部合约
- P2 | DESIGN | `assemblies/core-bundle/boundary.yaml`

---

## 跨阶段依赖

| Finding | 来源 | 依赖阶段 | 性质 |
|---------|------|---------|------|
| F-5A-01 孤立 contract | 阶段 5 | 阶段 3 (governance REF-13) | governance 应检出 ownerCell 不存在 |
| F-5D-03 audit-core 缺订阅 | 阶段 5 | 阶段 3 (TOPO-03) | 规则应检出 subscriber 声明无实现 |
| F-5S-04 内部事件导出 | 阶段 5 | 阶段 4 (generator) | boundary 生成逻辑应区分内部/外部 |
| F-5T-02 version 校验 | 阶段 5 | 阶段 3 (governance) | 需新增验证规则 |
