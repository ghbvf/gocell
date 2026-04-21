# 核心问题对标报告 03：健康检查预算与生命周期分阶段（A21 + ER-ARCH-01）

## 问题定义

- 对应 backlog：`A21 HEALTH-CHECKER-CTX-BUDGET-01`、`ER-ARCH-01`（later_detail）
- 当前风险：readiness 聚合预算不统一；启动“已启动”与“持续就绪”语义混淆时易出竞态

---

## 上游对标（3 项）

| 项目 | 证据 | 观察到的模式 | 对 GoCell 的启示 |
|---|---|---|---|
| Kubernetes | `pkg/kubelet/prober/*`, apiserver healthz | 探针预算显式化，启动/就绪/存活分离，输出简洁+可诊断并存 | checker 应带 ctx budget；ready/live/startup 语义分离 |
| Watermill | `message/router.go`, `messages-router.md` | 声明期与运行期分离；Running 仅表示启动完成，不等于持续健康 | EventRouter 需要 Setup/Run 双阶段和单独 readiness 信号 |
| go-micro | `service/service.go`, `server/rpc_server.go`, client timeout/retry | 生命周期钩子明确，注册门禁与周期复检结合 | bootstrap 增“对外可用”门控，依赖退化触发摘流/降级 |

---

## 结论（带权衡）

- 可落地的共识模式：
  1. 健康检查统一超时预算，并区分执行平面与聚合平面
  2. 启动完成信号不等同于就绪健康信号
  3. 失败输出遵循“外部简洁、内部可诊断”
- 与上游差异：
  - Kubernetes 复杂度较高；GoCell 可做轻量版本
  - Watermill 重点在消息路由，不覆盖完整 HTTP 健康治理

---

## 建议落地方案

1. `Checker` 签名升级为 `func(ctx context.Context) error`
2. `ReadyzHandler` 注入全局 deadline（如 2s）+ 受限并发
3. 响应中保留简洁状态；日志/verbose 输出 checker 耗时与错误分类
4. EventRouter 增加显式 Setup 阶段，再进入 Run 阶段

---

## 与当前代码映射

- 串行无 budget：`runtime/http/health/health.go:21`, `runtime/http/health/health.go:146`
- 生命周期接口限制：`kernel/lifecycle/managed_resource.go:23`
- 目标：就绪判断可预测、可诊断、可稳定运行
