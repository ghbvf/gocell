# [DEPRECATED] Tech Debt Registry — GoCell

> 本文件已合并到 [`docs/backlog.md`](backlog.md)（capability framework，2026-05-07 起生效）。  
> 原内容截至 commit `18a06ab7`（含 55 项跨 Phase 技术债与产品债）留作历史参考，可在 git 历史中查阅 `git show 18a06ab7:docs/tech-debt-registry.md`。
>
> 状态分布：
> - **32 RESOLVED** — 不入新 backlog（已闭口，详情查 git 历史）
> - **3 PARTIAL + 20 OPEN = 23 活动项** — 已迁入下表对应 cap
> - **1 项隐式解决**：P4-TD-02（chi.URLParam）— PR#367 D8 已删除 chi 依赖，原概念不再适用
>
> 各 item 现归属：
>
> | 原 ID | 现归属 | 备注 |
> |---|---|---|
> | P2-T-02 | `cap-x-cross` | J-auditlogintrail E2E |
> | P3-TD-02 + P4-TD-08 | `cap-10` | postgres 覆盖率合并为 1 条 |
> | P3-TD-04 | `cap-x-cross` | sandbox httptest panic |
> | P3-TD-05 + P4-TD-07 | `cap-x-cross` | 示例 docker-compose start_period 合并 |
> | P3-TD-10 | `cap-05` | TOCTOU 竞态（依赖 distlock + PG session repo）|
> | P3-TD-11 | `cap-05` X5 | accesscore domain 拆分（已存在，dedup） |
> | P3-TD-12 | `cap-09` | configpublish.Rollback 版本校验 |
> | P4-TD-01 | `cap-x-cross` | noop outbox/Claimer 共享包 |
> | P4-TD-02 | — | chi.URLParam 已被 PR#367 隐式解决，不再立项 |
> | P4-TD-03 | `cap-05` | IssueTestToken HS256 dead code |
> | P4-TD-04 | `cap-07` | ordercell L2 事务性 outbox |
> | P4-TD-06 | `cap-x-cross` | CI example validation `\|\| true` |
> | P4-TD-09 | `cap-x-cross` | testcontainers-go indirect 标记 |
> | P4-TD-10 | `cap-13` | metrics path label cardinality |
> | P4-TD-11 | `cap-10` | Migrator.Down() v=0 回归测试 |
> | WS-T-01 / WS-T-02 / WS-OPS-01 / WS-OPS-02 / WS-DX-01 / WS-DX-02 | `cap-13` | WebSocket Hub follow-up 6 项 |
>
> RESOLVED 32 项归档详情（不再单独追踪）：P2-SEC-03/04/06/07/08/09/10/11、P2-ARCH-04/05/06/07、P2-T-01/03/05/06/07/router、P2-D-06/07/09、P2-DX-02/03、P2-PM-03/audit/user、P3-TD-01/03/06/07/08/09、P4-TD-05、WS-ARCH-01。
