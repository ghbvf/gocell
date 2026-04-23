# 核心问题对标报告 01：路由鉴权声明闭环（L1 + L2）

## 问题定义

- 对应 backlog：`L1 AUDIT-ROUTE-POLICY-01`、`L2 ROUTE-POLICY-REGISTRY-01`
- 当前风险：路由已注册但策略未声明时，缺启动期阻断；同类漏洞可复发

---

## 上游对标（3 项）

| 项目 | 证据 | 观察到的模式 | 对 GoCell 的启示 |
|---|---|---|---|
| Kratos | `transport/http/server.go`, `middleware/selector/selector.go`, `middleware/auth/jwt/jwt.go` | 路由注册与鉴权匹配分离，可通过 operation 做匹配；但缺默认“缺失策略阻断” | 采用 operation 作为策略主键；但必须新增 GoCell 自有 fail-fast 对账 |
| go-zero | `rest/engine.go`, `rest/router/patrouter.go`, `tools/goctl/api/gogen/genroutes.go` | 解析期 + 启动期双门禁，重复路由可 fail-fast | 将“重复路由校验”与“策略完备性校验”一起放入启动阶段 |
| Kubernetes | `admissionregistration/validation/validation.go`, `policy_source.go` | 声明与绑定两阶段，运行期编译可执行快照；但对 dangling binding 容忍较高 | 借鉴声明-绑定-编译三段式，但 GoCell 需更严格：提交前阻断未绑定策略 |

---

## 结论（带权衡）

- 可落地的共识模式：
  1. 路由与策略均进入统一注册表
  2. 启动阶段进行完整对账（重复、未绑定、孤儿策略）
  3. 编译出只读 matcher 快照供运行态使用
- 与上游差异：
  - Kratos/go-zero 更偏工程便利
  - Kubernetes 在 dangling 情况更容忍
  - GoCell 需默认 fail-closed（安全优先）

---

## 建议落地方案

1. 新增 `runtime/http/router/policy_registry.go`
2. 注册期记录：`RecordRoute` + `RecordPolicy` + `RecordBinding`
3. `FinalizeAuth` 后调用 `Verify`：
   - route 无策略（非白名单） -> error
   - 策略未绑定任何 route -> error
   - 重复 method/path 或冲突 operation -> error
4. 将审计查询路由改为声明式注册，去除裸 `sub.Handle`

---

## 与当前代码映射

- 问题点：`cells/audit-core/cell.go:241`
- 现有汇总节点：`runtime/bootstrap/bootstrap_phases.go:489`, `runtime/http/router/router.go:628`
- 目标：把“声明完整性”从 review 规则升级为启动不变量
