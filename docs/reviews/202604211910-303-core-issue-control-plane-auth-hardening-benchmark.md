# 核心问题对标报告 02：控制面认证硬约束（S-nonce + S4b/A14）

## 问题定义

- 对应 backlog：`SERVICE-TOKEN-NONCE-STORE-01`、`VAULT-TOKEN-STATIC-REAL-GUARD-01`、`VAULT-AUTH-PLUGGABLE-01`
- 当前风险：控制面安全能力可选化，生产场景可能出现重放与长期静态凭据

---

## 上游对标（3 项）

| 项目 | 证据 | 观察到的模式 | 对 GoCell 的启示 |
|---|---|---|---|
| HashiCorp Vault | `api/lifetime_watcher.go`, AppRole/K8s auth 文档, token 概念文档 | 机器身份登录 + 短 TTL + 自动续期 + 续期失败重认证；不鼓励长期 static token | real 模式默认拒绝 static token；引入 authMode 与续期/重认证状态机 |
| Kubernetes SA Token | `pkg/kubelet/token/token_manager.go`, `pkg/serviceaccount/jwt.go`, TokenRequest API | audience 约束 + 短期 token + 自动轮转 + 在线校验，压缩重放窗口 | service token 强制 audience 校验 + jti/nonce 去重 + 短有效期 |
| SPIRE / SPIFFE | Workload API, `pkg/agent/svid/rotator.go`, JWT/X509-SVID 标准 | 强机器身份（mTLS）+ 短生命周期凭据 + 流式轮换 | internal 高敏接口可演进到 mTLS 身份，nonce 作为请求级新鲜性补强 |

---

## 结论（带权衡）

- 可落地的共识模式：
  1. 凭据默认短期且可轮换
  2. 生产认证失败默认 fail-closed
  3. audience + nonce/jti 双层降低重放
- 与上游差异：
  - K8s/SPIRE 依赖平台能力（TokenReview、mTLS 基础设施）
  - GoCell 可先落地应用层最小闭环，再逐步升级到平台身份

---

## 建议落地方案

1. `adapterMode=real` 时强制 NonceStore，不配置即启动失败
2. 新增 Vault `authMode`：`approle|kubernetes|token`，其中 real 禁止 `token` 静态长凭据
3. 续期失败进入“重认证-失败即拒绝”路径，不允许静默继续
4. 增加可观测指标：
   - `internal_token_replay_rejected_total`
   - `vault_auth_mode`
   - `vault_reauth_failure_total`

---

## 与当前代码映射

- Nonce 可选化：`runtime/auth/authenticator.go:130`, `cmd/core-bundle/main.go:347`
- Vault static token 路径：`adapters/vault/transit_provider.go:470`
- 目标：把安全能力从“建议配置”升级为“生产不变量”
