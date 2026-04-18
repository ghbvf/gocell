# ADR: First-Run Setup 模式 — 内网专属 Listener

- 编号: ADR-AUTH-SETUP-01
- 日期: 2026-04-18
- 状态: Proposed
- 相关: backlog P1-12 / PR#172 review F1 / backlog R4 INTERNAL-LISTENER-01

## 上下文

当前 `examples/sso-bff/main.go` 在启动时：

1. `generateDevPassword()` 随机生成 12 字节 admin 密码；
2. `accesscore.WithSeedAdmin("admin", seedAdminPass)` 灌入 in-memory UserRepository；
3. `logger.Info("sso-bff: seed admin ready ...", slog.String("password", seedAdminPass))` 将明文密码写入 stdout。

PR#172 外部审查标记 F1 — 明文密码落日志是硬问题：容器日志会被 Loki / CloudWatch / Datadog 采集长期保存，stdout 会被 sidecar 抓到。即使只用于 dev，这条代码路径的存在本身就是安全反模式，且 prod 部署根本无从 bootstrap 一个 admin（没有 seed，没有注册页）。

AUTH-SETUP-01 初案：新建 `POST /api/v1/setup/admin` 公开端点，无 admin 时有效、有 admin 后 409。这暴露一个公网无鉴权管理员创建端点，引入多处需要强假设的防抢占逻辑（token、rate limit、localhost 判定等）。

## 威胁模型

假定攻击者：

| 能力 | 是否具备 | 说明 |
|------|---------|------|
| 公网可达 `/api/v1/*` | ✅ | 这是服务存在价值 |
| K8s Pod 内部网络（ClusterIP）可达 | ❌ | 默认网络策略隔离 |
| kubectl / SSH / bastion 接入集群 | ❌ | 运维人员专属 |
| 读取应用容器 stdout / file 日志 | ❌ | SRE 专属（但一次泄漏长期留存） |

**核心观察**：合法 bootstrap 的人 100% 具备"进入集群内部"能力（运维部署是他做的），攻击者**按定义**没有。两者的能力鸿沟就是我们可以利用的安全边界 — 把 setup 端点放到一个攻击者网络上够不到的 listener 上，整个"谁先抢到谁是 admin"的竞态根本不存在。

## 候选方案

| # | 方案 | 公网暴露 | 防抢占机制 | 运维动作 | 密码是否落日志 |
|---|------|---------|-----------|---------|--------------|
| A | 公网端点 + `setupRequired` 状态 | ✅ | 首次写入后 409 | 前端打开 setup 页 | ❌ |
| B | 公网端点 + `X-Setup-Token` 头（token 来自 env / stdout） | ✅ | token 校验 | 运维读 env，curl | ⚠️ token 短时出现在 deploy 日志 |
| C | 公网端点 + localhost 绑定判定（reverse-proxy 剥离） | 取决于代理配置 | IP allowlist | SSH 进容器 curl localhost | ❌ |
| D | **独立内网 listener（ClusterIP-only / 127.0.0.1 默认），公网 router 不挂载 setup 路由** | ❌ | 网络层即隔离 | `kubectl port-forward` + curl | ❌ |
| E | 无 listener，offline CLI：`gocell admin bootstrap --dsn=...` | ❌ | 只能直连 DB | K8s Job / init container | ❌ |

A、B 都把端点暴露到公网，始终需要回答"如果 token / 状态检测有 bug，攻击者能抢到 admin 吗"。C 依赖运维正确配置反向代理，一次错误就失守。D 把**认证问题彻底转成网络隔离问题**，后者是已经通过 K8s NetworkPolicy / Ingress 白名单解决过的老问题。E 最彻底，但需要 CLI 依赖 access-core 的密码哈希与 repo，工程量大且 CLI/server 两条 bootstrap 路径长期并存。

## 决策

**采纳方案 D。**

1. **新增 internal listener**：`runtime/bootstrap` 增加第二个 `http.Server`，默认绑定 `127.0.0.1:9090`，可通过 `GOCELL_INTERNAL_BIND` 覆盖（见下文"绑定地址选择"）。Router 与公网 listener 独立，不共享中间件链（尤其不套 JWT AuthMiddleware — 本 listener 默认全量信任）。
2. **setup slice 只挂到 internal listener**：contract 放 `contracts/http/auth/setup/{status,admin}/v1/`，路径为 `/internal/v1/setup/status` 与 `POST /internal/v1/setup/admin`。`cell.go` 注册时使用新的 `cell.RegisterInternalHTTP(...)` 钩子（kernel/cell 层新增），公网 Router 对这两条路径返回 404。
3. **无 auth middleware，有一次性语义**：setup 端点不加 Bearer gate（加了反而误导——没有 admin 就不可能有 token）。`UserRepository` 新增原子方法 `CreateIfNoAdmin(user) (created bool, err error)`，in-memory 用 mutex、PG 用 `INSERT ... WHERE NOT EXISTS (SELECT 1 FROM user_roles WHERE role_id='admin')` 或 advisory lock。第二次调用返回 409 `ERR_SETUP_ALREADY_COMPLETED`。
4. **删除 seed path**：`accesscore.WithSeedAdmin` 连同 `generateDevPassword` 与日志全删；`examples/sso-bff/walkthrough_test.go` 改成测试开头 `POST /internal/v1/setup/admin` 初始化 admin，真实模拟部署后第一次 bootstrap 的流程。
5. **生产部署样板**（写入 `docs/deployment/`），三种形态各一份：

   **Docker（单容器或 compose）** — 容器内 bind 127.0.0.1:9090，不在 `ports:` 发布；
   ```bash
   docker compose exec app \
     curl -X POST http://127.0.0.1:9090/internal/v1/setup/admin \
          -H 'Content-Type: application/json' \
          -d '{"username":"admin","password":"..."}'
   ```

   **Docker（想从宿主 loopback 访问）** — `GOCELL_INTERNAL_BIND=0.0.0.0:9090` + `-p 127.0.0.1:9090:9090`：
   ```bash
   docker run -e GOCELL_INTERNAL_BIND=0.0.0.0:9090 -p 127.0.0.1:9090:9090 ...
   curl -X POST http://127.0.0.1:9090/internal/v1/setup/admin ...
   ```
   `127.0.0.1:` 前缀是关键，它让宿主 iptables 只允许宿主 loopback 访问；漏掉就等于公网暴露。

   **Kubernetes** — 容器内保持默认 127.0.0.1:9090，Service 只开 8080，运维用 port-forward：
   ```bash
   kubectl port-forward pod/gocell-core-0 9090:9090
   curl -X POST http://127.0.0.1:9090/internal/v1/setup/admin ...
   ```
   如需跨 pod（ops 控制面），`GOCELL_INTERNAL_BIND=0.0.0.0:9090` + 专用 ClusterIP Service + NetworkPolicy 只允许 `ops` namespace 入站。

## 绑定地址选择

选 `127.0.0.1:9090` 不是挑默认值，而是借 Linux network namespace 做**零配置隔离**：socket 只能被"同一 netns 内进程"连上，不依赖任何防火墙/NetworkPolicy 规则是否被正确写入。所谓"同一 netns"在不同部署形态下实际含义不同，运维路径因此也不同：

| 部署形态 | 同 netns 是谁 | 运维 bootstrap 路径 | 默认 127.0.0.1 够用 | 需要覆盖时的配置 |
|---------|-------------|------------------|------------------|-------------|
| 裸机 / systemd | 本机进程 | SSH + `curl localhost:9090` | ✅ | — |
| `docker run`（无 `-p 9090`） | 容器内进程 | `docker exec -it app curl localhost:9090/...` | ✅ | — |
| `docker-compose`（默认无 `ports`） | 同容器进程 | `docker compose exec app curl localhost:9090/...` | ✅ | — |
| `docker run -p 127.0.0.1:9090:9090` | 宿主机 loopback | 宿主 `curl 127.0.0.1:9090` | ❌ docker-proxy 需连容器外部接口 | `GOCELL_INTERNAL_BIND=0.0.0.0:9090`，隔离由 docker publish 的 `127.0.0.1:` 前缀保证 |
| `kubectl port-forward pod/x 9090:9090` | kubelet 进入 pod netns 转发 | 运维机 `curl 127.0.0.1:9090` | ✅（port-forward 本质即进 netns 连 loopback） | — |
| K8s 跨 pod（ops sidecar / admin 另一个 pod） | 不同 netns | `curl <podip>:9090` | ❌ | `GOCELL_INTERNAL_BIND=0.0.0.0:9090` + 单独 ClusterIP Service + NetworkPolicy allowlist |

**关键原则：server 的 bind 只是一半，另一半永远是 deployment 层的端口暴露策略。** 三条不变式：

1. **public listener（8080）** 允许被 Ingress / LoadBalancer / docker `-p` 无前缀发布到公网；
2. **internal listener（9090）** 永远不被公网 Ingress 挂载、永远不被 docker `-p 0.0.0.0:9090:9090` 或纯 `-p 9090:9090` 发布；
3. 当 `GOCELL_INTERNAL_BIND` 不是 loopback 时，**必须**有一层额外网络策略（docker host-loopback 绑定、K8s NetworkPolicy、cloud SG）显式收束 — bootstrap 脚本做 fail-fast 检查并在 slog.Warn 提示"internal listener is bound to non-loopback; ensure network isolation is in place"。

端口号 9090 可 collision Prometheus（其默认端口）；例子里 sso-bff 已占 8081。实际值用 env 可改，ADR 仅建议默认；`cmd/core-bundle` 选 `GOCELL_INTERNAL_BIND=127.0.0.1:9090`，sso-bff 例子选 `127.0.0.1:8091`（与其 public 8081 对齐 +10 的惯例）。

## 安全论证

- **无抢占窗口**：攻击者从公网到 9090 不可达，window 宽度 = 0。
- **无凭据泄漏**：密码由运维自己选择并通过 TLS（kubectl port-forward 本身走 SPDY/加密）传入，不经过 env / 日志。
- **无降级路径**：没有 "dev 便利模式" 自动 seed，也就不存在"忘记关"导致 prod 泄漏的风险。dev 通过 walkthrough_test 或 `make dev-seed` 脚本显式调同一端点，和 prod 100% 同路径。
- **R4 自然前推**：本方案顺手引入"独立 internal listener"这层基础设施。原 `/internal/v1/access/roles/*`（rbacassign）当前还挂在公网 Router 上，可以作为独立 PR 迁移到新 internal listener、去掉 bearer JWT 依赖（内网信任 + 网络策略已经够），彻底兑现 R4 INTERNAL-LISTENER-01。

## 契约草案

`contracts/http/auth/setup/status/v1/contract.yaml`

```yaml
id: c-auth-setup-status-v1
kind: http
lifecycle: active
http:
  method: GET
  path: /internal/v1/setup/status
  listener: internal          # 新增字段，供 bootstrap 决定挂载到哪个 Router
response:
  schema:
    type: object
    properties:
      data:
        type: object
        properties:
          setupRequired: { type: boolean }
        required: [setupRequired]
```

`contracts/http/auth/setup/admin/v1/contract.yaml`

```yaml
id: c-auth-setup-admin-v1
kind: http
lifecycle: active
http:
  method: POST
  path: /internal/v1/setup/admin
  listener: internal
request:
  schema:
    type: object
    required: [username, password]
    properties:
      username: { type: string, minLength: 3, maxLength: 64 }
      password: { type: string, minLength: 12 }
response:
  status: 201
  schema:
    type: object
    properties:
      data:
        type: object
        properties:
          userId: { type: string }
          username: { type: string }
errors:
  - status: 409
    code: ERR_SETUP_ALREADY_COMPLETED
    when: admin already exists
  - status: 400
    code: ERR_VALIDATION_WEAK_PASSWORD
    when: password fails strength policy
```

**不返回 access/refresh token**：bootstrap 和登录是两件事，setup 成功后运维走标准 `POST /api/v1/access/sessions/login` 拿到 token，确保 token 颁发路径只有一条。

## 实施分阶段

| 阶段 | 交付 | 估时 | 依赖 |
|------|------|------|------|
| Phase 1 | `runtime/bootstrap` internal listener 基础设施 + `kernel/cell.RegisterInternalHTTP` + `contract.http.listener` 字段 + metadata validator | 3h | 无 |
| Phase 2 | `setup` slice + contract + `UserRepository.CreateIfNoAdmin` in-memory 实现 + cell 接线 | 3h | Phase 1 |
| Phase 3 | 删除 `WithSeedAdmin` / `generateDevPassword` / seed 日志；`walkthrough_test.go` 改用 setup 端点 | 1h | Phase 2 |
| Phase 4 | K8s 部署样板文档 + NetworkPolicy 示例 | 1h | Phase 3 |
| Phase 5（可选，独立 PR） | R4 迁移 `rbacassign /internal/v1` 到 internal listener，去除 bearer JWT | 4h | Phase 1 |

总计 8h（不含 Phase 5），比原 6h 多 2h 用于 listener 基础设施；换来一个可复用能力且消除整条公网攻击面。

## 未决

1. **PG 实现的 `CreateIfNoAdmin` 原子性**：X1 PG-DOMAIN-REPO 上线时用 advisory lock 还是唯一索引？倾向唯一索引 `WHERE role_id='admin'` partial index，单条 INSERT 竞态由 PG 保证。本 ADR 只定接口语义，PG 落地写进 X1 的 PR。
2. **内网 listener 的 observability**：`/readyz` `/metrics` 是否也迁过去？建议迁（和 setup 一样内部）。但本 PR 只搬 setup，observability 作为 R4 的一部分另评审。
3. **多副本场景**：HA 部署下每个 pod 都会监听 9090，kubectl port-forward 只会连到其中一个。`CreateIfNoAdmin` 的原子性靠 PG 层保证，port-forward 落到哪个 pod 不影响正确性。

## 对标

- **Kubernetes**: `kube-apiserver --secure-port=6443` 公网 + `--bind-address=127.0.0.1` 健康端点；敏感操作走 `kubectl exec`/port-forward。
- **PostgreSQL**: 初始化只通过 `initdb` + Unix socket 本地连接，没有"公网创建超级用户"端点。
- **etcd**: `--listen-client-urls` 公网 + `--listen-peer-urls` 内网，敏感 bootstrap 命令只接 peer listener。
- **Vault**: `vault operator init` 必须本地 CLI 或 API 带 root token；无公网自助初始化。
- **GitLab**: 历史上用 `gitlab-rails runner` 或 `/etc/gitlab/initial_root_password` 文件（不走 HTTP）。

业界共识：**bootstrap 超级用户不经过公网 HTTP**。本 ADR 与此对齐。
