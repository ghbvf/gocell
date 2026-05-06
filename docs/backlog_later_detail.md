# [DEPRECATED] GoCell V1.1+ 长期规划详解

> 本文件已合并到 [`docs/backlog.md`](backlog.md)（capability framework，2026-05-07 起生效）。  
> 原内容（91 行，V1.1+ 长期规划详解）已完整备份到 [`docs/backlog/archive/backlog_later_detail.md`](backlog/archive/backlog_later_detail.md)（develop @ 18a06ab7 快照）。
>
> 各 item 现归属：
>
> | 原段 | 现归属 |
> |---|---|
> | §1 Metadata 校验规则补全 (G-1/G-2/G-4/G-6) | `cap-02-metadata-governance` |
> | §2 Kernel 子模块补全 — kernel/webhook | `cap-x-cross` (also: cap-04, cap-08) |
> | §2 Kernel 子模块补全 — kernel/reconcile / replay | `cap-08-subscriber-claimer` |
> | §2 Kernel 子模块补全 — runtime/scheduler | `cap-x-cross` (also: cap-11, cap-12) |
> | §2 Kernel 子模块补全 — kernel/rollback | `cap-x-cross` (also: cap-07, cap-08) |
> | §2 已 ✅ 项（kernel/wrapper / kernel/command / PR-A12-ACK-ATOMIC / PR-A12-SWEEPER-WIRE） | 不入 backlog（已闭口） |
> | §3 Adapters 分层重整 (AL-01/02/04/RMQ-STATUS-01) | 全 ✅ 或 won't-do，不入 backlog |
> | §4 架构风险 — Cell 接口拆分 | 已上 029 #13 PR-A22；与 `cap-14 PR245-F10` 同源 |
> | §4 架构风险 — Adapter 集成覆盖 | 大部已收口，剩 2 处基础设施依赖 t.Skip 留 v1.2 |
> | §4 架构风险 — ER-ARCH-01 | ✅ done |
> | §4 架构风险 — L3 示例缺口 | `cap-x-cross` `L3-EXAMPLE-PROJECTION-01` |
> | §5 契约增强 (CONTRACT-BREAKING-01 / CODEGEN-01 / STUB-01) | `cap-14-codegen-tooling` |
> | §6 技术债务 (C-AC7) | `cap-05-authn` |
> | §6 技术债务 (C-L6) | `cap-14-codegen-tooling` |
> | §6 技术债务 (C-DC9) | `cap-x-cross` (also: cap-08) |
> | §6 技术债务 (DURABLE-TYPE-01) | `cap-02-metadata-governance` |
> | §6 已 ✅ (CONTRACT-META-01) | 不入 backlog（PR#239 已合） |
> | §7 WinMDM Defer V1.1 — WM-32 mTLS | `cap-04-http-inbound` |
> | §7 WinMDM Defer V1.1 — WM-18 延迟消息 | `cap-08-subscriber-claimer` |
> | §7 WinMDM Defer V1.1 — WM-4 Webhook | 与 `cap-x-cross KERNEL-WEBHOOK-01` 同源 |
> | §7 WinMDM 永久封存 (WM-3 / WM-14 / WM-21 / WM-24 / WM-26 / WM-28 / WM-29 / GAP-1 / GAP-8) | 不入 backlog（六席位审议拒绝，详见 commit `18a06ab7` 历史） |
