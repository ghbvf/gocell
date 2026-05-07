# [DEPRECATED] Backlog2 — 2026-04-29 4 份归档审查的新增问题清单

> 本文件已合并到 [`docs/backlog.md`](backlog.md)（capability framework，2026-05-07 起生效）。  
> 原内容（431 行，K/R/C/A/W/X/T 物理域 + §12 PR#376 follow-up + §13 PR-V1-SEC-SETUP-CLOSURE follow-up）已完整备份到 [`docs/backlog/archive/backlog2.md`](backlog/archive/backlog2.md)（develop @ 18a06ab7 快照）。
>
> 状态：~67 项 OPEN（含 P0 高危项 2 条）+ 已闭口 ~25 项（✅ 标注详见原档逐项）+ §10 索引段 dedup ~10 项已在原 backlog.md 不重复登记。
>
> 各 OPEN item 已按 capability + cross-domain 决策规则迁入 `docs/backlog.md`。映射规则（按原 §序）：
>
> | 原 § | 主轴归属 | 备注 |
> |---|---|---|
> | §1 P0 高危项 | B2-C-01 → cap-13 (hashchain restart) / B2-C-02 → cap-05 (setup admin public) | 🔴 标记 |
> | §2 kernel/治理 | B2-K-02/05/06/07/08 → cap-01/02/02/02/14 (B2-K-01/03/04 已 ✅) | — |
> | §3 runtime/bootstrap/health/observability | B2-R-01/02/05/06/07/08/09 → cap-12/12/13×5 (B2-R-03 dedup STARTUP-ROLLBACK / B2-R-04 ✅ / B2-R-10 ✅ / B2-R-11 dedup PR405) | — |
> | §4 cells | B2-C-03/05/06/07/08/09/10/11/12/13/14 → cap-01/13/06/04/03/13/08/09/10/x-cross/13 (B2-C-04 dedup AUDITAPPEND-L2-FAILURE-PROOF) | — |
> | §5.1 PostgreSQL | B2-A-08/09/10/11/13 → cap-05/05/12/10/14 (B2-A-12 ✅ via PR#401) | — |
> | §5.2 RabbitMQ | B2-A-14/15/16/17 → cap-08×4 (B2-A-18 dedup ADAPTER-CONNECT-BUDGET) | — |
> | §5.3 OTel/Prometheus/Redis/S3 | B2-A-19~34 → cap-13×6 / cap-14×3 / cap-08×2 / cap-09 / cap-10×3 / cap-11×2 (B2-A-32 dedup S3-FAILURE-INJECTION / B2-A-23 整合 cap-13) | — |
> | §6 WebSocket | B2-W-03/05 → cap-13/12 (B2-W-01/02/04/06 ✅) | — |
> | §7 cmd/装配/启动 | B2-X-01/02/03/04/05/06/07/08 → cap-14/14/12/04/14/14/14/14 | — |
> | §8 contracts | B2-T-01/02/04/05/08 → cap-03/06/03/06/04 + B2-T-07-FU-1/2/3/4 → cap-06/06/02/x-cross (B2-T-03/06/07 ✅ via PR#353/362) | A5 follow-ups |
> | §9 工时 / §11 后续动作 | 不入 backlog（被 029/030 master-roadmap 吸收） | — |
> | §10 dedup 索引 | 不重复迁移（参考索引已无效）| — |
> | §12 PR#376 follow-up | ARCHTEST-PROJECTMETA-HANDLERS-INDEX-01 / PR-V1-EVENT-TYPED-PAYLOAD-CODEGEN | 已被后续 PR#403 typed envelope + Codegen 工作链路吸收，不重复登记 |
> | §13 PR-V1-SEC-SETUP-CLOSURE follow-up | B2-R-B-13-FU-01/02 → cap-08（RMQ doc/test） / B2-PROVISIONER-MUTEX-REVIEW → cap-01 / 其余 3 项 dedup（BOOTSTRAP-AUDIT-CHAIN-WIRING / RATELIMIT-DISTRIBUTED / ACCESSCORE-PG-USERS-MIGRATION 已在 main backlog） | — |
