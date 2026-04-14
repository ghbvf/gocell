# R1D-4 独立产品/语义审查：`adapters/oidc`

## 结论

这个包已经具备了 OIDC 客户端的基本骨架，但“成功返回”还没有达到可直接喂给上层 login flow 的语义完整度。当前实现更像是“协议报文能拉下来并解析”，而不是“返回值已足够可信、可消费、可持久化”。最明显的问题集中在 token exchange、ID token verification 和 discovery 可信边界上。

当前仓库里 `access-core` 的登录链路仍是本地 session/password 模式，没有看到直接消费这个 OIDC adapter 的现成调用点；所以这里的判断主要基于接口语义和返回值 faithful 程度，而不是端到端集成事实。

## 主要问题

1. **[P1] `ExchangeCode` 把“HTTP 200 + JSON 可解析”当成成功，但没有保证返回结果可用于登录。**
   `/Users/shengming/Documents/code/gocell/adapters/oidc/token.go:27` 到 `:83` 里，成功条件只剩下状态码为 200 和 `json.Unmarshal` 成功。`TokenResponse` 里最关键的 `access_token`、`id_token`、`token_type` 都没有做存在性/类型校验。与此同时，`Config.Validate` 只检查了 `issuerURL` 和 `clientID`，`clientSecret`、`redirectURL` 这两个对授权码换 token 必需的字段没有被前置拦住，见 `/Users/shengming/Documents/code/gocell/adapters/oidc/config.go:43` 到 `:51`。结果是构造成功并不代表 token exchange 可用，login flow 很容易拿到一个“看似成功、其实不可消费”的返回值。

2. **[P1] `Verify` 的成功返回不够 faithful：它接受了 OIDC 不完整的 ID token 语义，并把 `aud` 压扁丢失了。**
   `/Users/shengming/Documents/code/gocell/adapters/oidc/verifier.go:65` 到 `:135` 只验证了签名、issuer 和 audience 是否命中客户端，但没有把 `sub`、`exp` 这些 OIDC ID token 的关键语义作为成功门槛。代码只是在 `claimString` / `float64` 类型断言成功时填充返回结构，因此一个缺少 `sub` 或 `exp` 的 token 仍可能走到成功分支，返回 `Subject == ""`、`ExpiresAt == 0` 的结果。更进一步，`IDTokenClaims.Audience` 是单个 `string`（`verifier.go:35-45`），而 `audienceMatch` 已经支持了数组形式的 `aud`；这意味着多受众 token 在验证成功后会丢失真实 audience 列表，返回值不能 faithfully 反映输入 claims。

3. **[P2] Discovery 成功并没有证明它属于配置中的 issuer，返回的 metadata 可信边界过宽。**
   `/Users/shengming/Documents/code/gocell/adapters/oidc/provider.go:55` 到 `:123` 里，`Discover` 只要求 discovery doc 的 `issuer` 非空，然后就缓存并返回。它没有把 discovery document 的 `issuer` 和 `Config.IssuerURL` 做一致性校验。对于 login flow 来说，这意味着“Discover 成功”并不等于“拿到的是当前配置 issuer 的元数据”，上层后续会把 JWKS、userinfo、token endpoint 都建立在一份未经充分确认的 metadata 上。这个返回值在语义上还不够可托付。

4. **[P2] `JWKSCacheTTL` 是死配置，key rotation 语义没有真正落地。**
   `Config` 里明确暴露了 `JWKSCacheTTL`，见 `/Users/shengming/Documents/code/gocell/adapters/oidc/config.go:21` 到 `:24`，但 `Verifier` 里只有 `fetchAt` 字段被写入，后续完全没有按 TTL 触发刷新，见 `/Users/shengming/Documents/code/gocell/adapters/oidc/verifier.go:47` 到 `:54` 和 `:179` 到 `:251`。现在 JWKS 只有在缓存里找不到新的 `kid` 时才会重新拉取，这和“按刷新间隔轮换 key set”的语义不是一回事。对上层 login flow 来说，这会把 key rotation 的时效性风险悄悄推迟到失败时刻才暴露。

## 残余风险

- 单测覆盖了 discovery、exchange、verify、userinfo 的基本 happy path，但没有覆盖 `aud` 数组、缺失 `sub/exp`、缺失 `id_token`、issuer 不一致、JWKS 过期重拉这些成功语义缺口。
- `integration_test.go` 目前还是 `t.Skip` 占位，没有真实 OIDC provider 的端到端证据。

总体上，这个 adapter 已经能“跑通协议形状”，但还没达到“成功返回就可以被上层 login flow 直接信任”的程度。最需要优先修的是 token exchange 和 ID token verification 的成功条件收敛。
