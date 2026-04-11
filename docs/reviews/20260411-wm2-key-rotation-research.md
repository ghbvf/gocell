# WM-2 密钥轮换 — 开源研究报告

> 日期: 2026-04-11
> 范围: JWT kid 轮换 + HMAC 密钥轮换（`runtime/auth/`）
> 方法: 4 角色并行分析 12+ 开源项目

---

## 一、JWT kid 轮换 — 行业共识

### 研究项目

| 项目 | 类型 | kid 来源 | 轮换模型 | 多密钥 |
|------|------|---------|---------|-------|
| **go-jose/v4** | JOSE 库 | 调用方设置 `JSONWebKey.KeyID` + RFC 7638 Thumbprint | 无（纯库） | `JSONWebKeySet` 任意数量 |
| **Dex** | OIDC Provider | 随机 20-byte hex | 3 态：Active → Verification-only(带 Expiry) → Pruned | 1 签名 + N 验证 |
| **Authelia** | Auth Gateway | 配置文件静态 kid | 无自动轮换，运维手动 | 多算法多 kid，按 token 类型选 |
| **Casdoor** | IAM | cert.Name | 无自动轮换，DB 存储证书 | 每 app 绑定 1 cert |

### 关键发现

| 维度 | 行业共识 | 最佳参考 |
|------|---------|---------|
| **kid 来源** | SHA-256 thumbprint (RFC 7638) 或 UUID | Kubernetes (thumbprint), Hydra (UUID) |
| **签发** | `token.Header["kid"] = activeKey.ID` | Dex, Casdoor, Hydra, Zitadel 全部设置 |
| **验证** | KeyFunc 按 kid 查找密钥，非遍历 | Kratos `jwt.Keyfunc`, Hydra `GetPublicKey(kid)` |
| **轮换状态** | 3 态：Active → Verification-only(带 Expiry) → Pruned | Dex 最简洁 |
| **多密钥** | 1 签名 + N 验证（N 通常 1-2） | Dex, Zitadel |
| **JWKS** | 暴露 `/.well-known/jwks.json` | Hydra, Zitadel, Casdoor 全部暴露 |

### Dex 轮换模型详解（推荐参考）

Dex 的 `Keys` struct 持有 1 个 `SigningKey`（私钥）+ `SigningKeyPub`（公钥）。轮换时：
1. 生成新密钥对，分配随机 20-byte hex 作为 KeyID
2. 旧 `SigningKeyPub` 降级为 `VerificationKeys`，设置 `Expiry = now + idTokenValidFor`
3. 新密钥成为 `SigningKey`，签发所有新 token
4. 过期的 VerificationKey 在下次轮换时被清除

轮换由定时器触发：`now > NextRotation` 时在 `Storage.UpdateKeys()` 内原子执行。

ref: dexidp/dex `server/rotation.go`, `storage/storage.go`

---

## 二、HMAC 密钥轮换 — 行业共识

### 研究项目

| 项目 | 密钥选择 | 签名策略 | 验证策略 | 轮换方式 | Ring 大小 |
|------|---------|---------|---------|---------|----------|
| **gorilla/securecookie** | 位置式 (index 0 = 当前) | 第一个 codec | 依次尝试全部 `DecodeMulti` | 配置 + 重启 | 无限制 |
| **gorilla/sessions** | 委托 securecookie | 第一个 codec | 依次尝试全部 | 配置 + 重启 | 无限制 |
| **golang-jwt** | 标签式 (`kid` header) | 调用方指定 | `Keyfunc` 按 kid 查找 | JWKS 轮询或代码 | 无限制 |
| **Rails ActiveSupport** | 标签式 (`rotate` 调用) | 仅 primary | 先 primary 再 rotations + `on_rotation` 回调 | 代码调用 | 无限制 |
| **Django signing** | 角色式 (`SECRET_KEY` + `FALLBACKS`) | 仅 primary | 依次尝试 `[key, *fallbacks]` | 配置变更 | 无限（有性能警告） |
| **go-zero** | 双密钥窗口 (`secret` + `prevSecret`) | 当前 secret | 尝试两个，按使用频率优先 | 配置变更 | 固定 2 |

### 关键发现

| 维度 | 行业共识 | 最佳参考 |
|------|---------|---------|
| **密钥选择** | 位置式（index 0 = 当前） | gorilla, Django, Rails 全部位置式 |
| **签名** | 始终用 `keys[0]` | 全部一致 |
| **验证** | Try-all-keys 依次尝试 | gorilla `DecodeMulti`, Django `unsign()` |
| **Ring 大小** | 2（current + previous） | go-zero `PrevSecret`, Django 推荐 |
| **轮换触发** | 配置驱动（非定时） | Django `SECRET_KEY_FALLBACKS` |
| **回调** | 旧密钥解码成功时 emit metric | Rails `on_rotation` |

---

## 三、密钥生命周期 — 行业共识

### 研究项目

| 项目 | 版本模型 | 轮换状态机 | 并发控制 | Grace period |
|------|---------|-----------|---------|-------------|
| **Kubernetes apiserver** | SHA-256 thumbprint | 无显式状态机，多密钥验证器 | 不可变集合，原子替换 | `GetCacheAgeMaxSeconds()` = 3600s |
| **Vault Transit** | 递增整数版本 | 即时原子轮换 + Min/Max Version 滑动窗口 | `sync.RWMutex` | MinDecryptionVersion 控制 |
| **cert-manager** | revision 整数 | 条件触发 → 签发 → 原子交换 | K8s 工作队列串行化 | 剩余 1/3 有效期 |
| **Teleport** | UUID (CurrentID) | 5 阶段: Standby→Init→UpdateClients→UpdateServers→Standby | Clone + 后端 CAS | 可配置 GracePeriod |

### Vault Transit 详解（推荐参考）

Vault 使用两个滑动窗口控制密钥生命周期：
- `MinDecryptionVersion`: 允许解密/验证的最旧版本
- `MinEncryptionVersion`: 允许加密/签名的最旧版本（0 = 仅最新版本）
- `AutoRotatePeriod`: 自动轮换间隔（最小 1h）

密文前缀 `vault:v{version}:` 使版本始终可见。旧密钥保留到管理员推进 MinDecryptionVersion。

ref: hashicorp/vault `builtin/logical/transit/policy.go`

### Teleport 5 阶段轮换详解

```
Standby → Init → UpdateClients → UpdateServers → Standby (complete)
              |                  |                 |
              +→ Rollback ←------+→ Rollback ←-----+
                    |
                    +→ Standby (abort)
```

- **Init**: 新 CA 生成为 AdditionalTrustedKeys，旧 CA 继续签名
- **UpdateClients**: 密钥交换 — 新 CA 签名，旧 CA 仍受信
- **UpdateServers**: 新旧 CA 同时受信，旧 CA 逐步淘汰
- **Rollback**: 任何阶段可回退

ref: gravitational/teleport `lib/auth/rotate.go`

---

## 四、Go 框架集成 — 行业共识

### 研究项目

| 项目 | 密钥配置 | kid 支持 | 轮换 | JWKS | DI 集成 |
|------|---------|---------|------|------|---------|
| **go-zero** | 静态 HMAC string + `PrevSecret` | 无 | 双密钥窗口 + 使用频率优先 | 无 | 无 |
| **Kratos** | `jwt.Keyfunc` 回调 | 完全委托用户 | 无内建 | 无 | Middleware 注册 |
| **Casdoor** | DB 存储证书 | cert.Name 作为 kid | 手动（管理员操作） | 有 | ORM 绑定 |
| **Ory Hydra** | SQL DB + AES-GCM 加密 | UUID | 删除 + 自动重建 | 有 | `InternalRegistry` DI |
| **Zitadel** | Event-sourced + 加密存储 | 事件 ID | 自动（基于 expiry） | 有 | 深度 DI + 背景清理 |

### 关键发现

| 维度 | 行业共识 | 最佳参考 |
|------|---------|---------|
| **验证器抽象** | `KeyFunc(token) → key` 而非单密钥 | Kratos, golang-jwt/v5 原生模式 |
| **密钥存储接口** | `GetSigningKey()` + `GetPublicKey(kid)` | Hydra `Manager` |
| **HMAC 双密钥** | `secret` + `prevSecret` 两阶段窗口 | go-zero `TokenParser` |
| **生命周期** | DI 注入 + config watcher 热更新 | Zitadel (background purge goroutine) |
| **JWKS endpoint** | 暴露公钥供跨服务验证 | Hydra, Zitadel, Casdoor |

---

## 五、GoCell 现状差距

### 当前 `src/runtime/auth/` 代码

| 文件 | 现状 | 差距 |
|------|------|------|
| `keys.go` | 单 RSA 密钥对，`LoadKeysFromEnv()` 从 env 加载 | 无 kid、无 key ring、无多密钥 |
| `jwt.go` | `JWTIssuer` 无 kid header；`JWTVerifier` 单密钥 KeyFunc | 签发不带 kid，验证无法按 kid 选密钥 |
| `servicetoken.go` | 单 HMAC secret (`[]byte`) | 无轮换、无 try-all-keys |
| `auth.go` | `TokenVerifier` / `Authorizer` 接口 + `Claims` | Claims 无 kid 字段 |
| `middleware.go` | Bearer → verify → context | 接口层 OK，不需要改 |

### 框架对标确认

`docs/references/framework-comparison.md:98-104`:
- Primary: **go-micro auth** (JWT + Rules + Account)
- Secondary: **go-kratos middleware/auth**
- Goal: **RS256 pinned + kid rotation + Claims context injection**

---

## 六、推荐方案（WM-2 范围限定）

### Layer 1: JWT kid 轮换（采纳 Dex 模型 + Kratos 抽象）

```
签发: JWTIssuer.Issue() → token.Header["kid"] = SHA256Thumbprint(publicKey)
验证: JWTVerifier.Verify() → KeyFunc 从 KeySet 按 kid 查找
数据: KeySet { SigningKey, VerificationKeys []VerificationKey }
状态: Active → VerificationOnly(ExpiresAt=now+tokenTTL) → Pruned
```

关键设计决策：
- kid = RFC 7638 thumbprint（确定性，无需额外存储，K8s 同款）
- `KeyFunc` 模式替代单密钥（Kratos 验证的最小正确抽象）
- WM-2 阶段：静态配置加载，不含自动轮换调度器

### Layer 2: HMAC 密钥轮换（采纳 gorilla + go-zero 模型）

```
签名: 始终用 secrets[0]
验证: 依次尝试 secrets[0], secrets[1]
Ring:  [active, previous] 固定大小 2
触发: 配置变更（与 WM-34 热更新对接）
```

关键设计决策：
- 位置式而非标签式（HMAC 签名值无法嵌入 kid）
- Ring 大小 = 2（go-zero `PrevSecret` 验证的工业实践）
- WM-2 阶段：静态配置，WM-34 后对接热更新

### 范围排除

| 排除项 | 原因 | 留给哪个后续任务 |
|--------|------|----------------|
| 自动轮换调度器 | 需要 worker 基础设施 | WM-34 热更新后 |
| JWKS endpoint | 需要 router 注册 | 独立任务 |
| DB 密钥存储 | 当前单进程足够 | 扩展时 |
| 多算法支持 | RS256 pinned 不变 | 无计划 |
| SecureCookie 集成 | WM-36 专项 | WM-36 |
