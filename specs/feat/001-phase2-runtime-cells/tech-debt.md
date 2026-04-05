# Tech Debt — Phase 2

## 分类说明
- [TECH]: 技术债务（代码质量、架构退化、测试缺失）
- [PRODUCT]: 产品债务（降级体验、缺失功能、临时方案）

## 延迟项

| # | 标签 | 来源席位 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|---------|------|---------|-------------|
| 1 | [TECH] | 架构一致性 | ARCH-04: BaseSlice 是空壳，与实际 Service/Handler 无关联 | kernel Slice 接口重构影响面大 | Phase 3 |
| 2 | [TECH] | 架构一致性 | ARCH-05: cells/ 直接 import chi，应仅用 RouteMux 抽象 | 需扩展 RouteMux 接口支持子路由 | Phase 3 |
| 3 | [TECH] | 架构一致性 | ARCH-06/D-03: 订阅 goroutine 用 context.Background()，shutdown 时无法取消 | 功能正确（eventbus.Close 间接终止），优雅关闭优化 | Phase 3 |
| 4 | [TECH] | 架构一致性 | ARCH-07: L2 事件发布不在事务中，Phase 3 需改为 outbox.Writer | Phase 2 无 DB，事务语义无意义 | Phase 3 |
| 5 | [TECH] | 安全/权限 | SEC-03: cmd/core-bundle 密钥硬编码（有 "replace-in-prod" 注释） | Phase 2 无生产部署 | Phase 3 |
| 6 | [TECH] | 安全/权限 | SEC-04: JWT 使用 HS256 对称签名，缺少 aud claim | Phase 2 单进程部署可接受 | Phase 3 迁移 RS256 |
| 7 | [TECH] | 安全/权限 | SEC-06: RealIP 无条件信任 XFF，可绕过限流 | 需 trustedProxies 配置机制 | Phase 3 |
| 8 | [TECH] | 安全/权限 | SEC-07: ServiceToken HMAC 无 timestamp，可重放 | 内部 API 风险可控 | Phase 3 |
| 9 | [TECH] | 安全/权限 | SEC-08: Session/User ID 用 UnixNano 可预测 | Phase 2 in-memory 存储风险低 | Phase 3 改 UUID |
| 10 | [TECH] | 安全/权限 | SEC-09: refresh token 验证未显式检查 signing method | jwt/v5 内置防护，defense-in-depth | Phase 3 |
| 11 | [TECH] | 安全/权限 | SEC-10: refresh token 无 rotation reuse detection | ARCH-08 已修复 persist，完善 rotation 需更多逻辑 | Phase 3 |
| 12 | [TECH] | 安全/权限 | SEC-11: API 端点无认证中间件保护 | Phase 2 无外部暴露 | Phase 3 |
| 13 | [TECH] | 测试/回归 | T-01: 10/16 slices handler 层覆盖率 < 80%（Cell 级聚合达标 85-87%） | handler 层 httptest 测试工作量大 | Phase 3 补充 |
| 14 | [TECH] | 测试/回归 | T-02: 无 J-audit-login-trail 端到端集成测试 | Soft Gate 允许 stub 辅助 | Phase 3 adapter 就绪后 |
| 15 | [TECH] | 测试/回归 | T-03: bootstrap.go 覆盖率 51.4%（sandbox 限制 net.Listen） | CI 环境可补充 | Phase 3 |
| 16 | [TECH] | 测试/回归 | T-05: in-memory repo 掩盖集成问题 | Phase 3 adapter 替换后自然解决 | Phase 3 |
| 17 | [TECH] | 测试/回归 | T-06: go vet copylocks warning (time.Time in User struct) | Go 版本特定行为 | Phase 3 |
| 18 | [TECH] | 测试/回归 | T-07: cmd/core-bundle 无冒烟测试 | 功能已在 cell_test.go 覆盖 | Phase 3 |
| 19 | [TECH] | 运维/部署 | D-06: Assembly.Stop 可在 Starting 状态被调用 | 竞态窗口极小 | Phase 3 |
| 20 | [TECH] | 运维/部署 | D-07: config watcher 未集成到 bootstrap 生命周期 | J-config-hot-reload 需要 | Phase 3 |
| 21 | [TECH] | 运维/部署 | D-09: eventbus 无健康状态暴露到 /healthz | 可观测性增强 | Phase 3 |
| 22 | [TECH] | DX | DX-02: 11 个 runtime 包缺少 doc.go | 文档增量补充 | Phase 3-4 |
| 23 | [TECH] | DX | DX-03: TopicConfigChanged 常量定义 3 次 | 抽取到共享 events 包 | Phase 3 |
| 24 | [PRODUCT] | 产品/UX | PM-03: RateLimit Retry-After 硬编码 1 秒 | 需扩展 RateLimiter 接口 | Phase 3 |
| 25 | [PRODUCT] | 产品/UX | 审计查询 time.Parse 错误静默忽略 | 应返回 400 | Phase 3 |
| 26 | [PRODUCT] | 产品/UX | Update user 仅支持 email 字段 | 扩展可更新字段 | Phase 3 |

## PR #3 深度审查追加项（来源: review-031, 8-agent 模块级审查）

> 以下为 review-031 新发现，与上方 S6 条目去重后追加。已在本 PR 修复的不再列入。

### BLOCKING — 需另开分支修复

| # | 标签 | 来源 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| 27 | [TECH] | review-031 V1 | kernel/slice/verify.go 7 处 fmt.Errorf 导出错误，违反 errcode 规则 | Phase 1 kernel 代码，改动影响已有测试 | 新分支 |
| 28 | [TECH] | review-031 V2 | VERIFY-01 只检查 provider 角色的 verify.contract，V3 spec 要求所有角色 | Phase 1 governance 规则，需与 spec 确认是 bug 还是有意设计 | 新分支 |
| 29 | [TECH] | review-031 V3 | Projection replayable 必填字段未在 FMT 规则中校验 | Phase 1 governance 规则，需扩展 FMT-04 | 新分支 |
| 30 | [TECH] | review-031 S5 | ServiceToken HMAC 只签 method+path，不含 query/timestamp，可重放+参数篡改 | 协议变更影响所有 client | 新分支 |
| 31 | [TECH] | review-031 S4 | RealIP trusted proxies — API 签名变化需参数化 | 中间件 API 变更影响使用方 | 新分支 |
| 32 | [TECH] | review-031 V5 | access-core 5 个 slice handler 层覆盖率 < 80%（identitymanage 42%, rbaccheck 41%, sessionlogin 65%, sessionlogout 61%, sessionrefresh 66%） | handler httptest 测试工作量大 | 新分支 |
| 33 | [TECH] | review-031 S8 | 7 处 ID 生成用 UnixNano 可碰撞+可预测（identitymanage, sessionlogin, auditappend, configwrite, configpublish, eventbus） | 需引入 crypto/rand UUID，7 处同步改 | 新分支 |
| 34 | [TECH] | review-031 S9 | access-core 端点无 auth/authz 中间件保护，知道 ID 即可操作 | 需先确认公开/保护端点策略 | 新分支 |
| 35 | [PRODUCT] | review-031 + 用户审查 | /metrics 返回 JSON 非 Prometheus text format，go.mod 无 prometheus/otel 依赖，与 spec/AC 交付口径不一致 | 依赖拉取受网络限制。需修正 spec 措辞为"接口+stub" | 新分支(文档) |

### CONCERNS — Phase 3 处理

| # | 标签 | 来源 | 问题 | 建议修复时机 |
|---|------|------|------|-------------|
| 36 | [TECH] | C-G1 | DFS 只找第一个环，多环需反复修 | Phase 3 |
| 37 | [TECH] | C-G2 | depcheck.go 重复定义 isProviderRole（与 cell.IsProviderRole 重复） | Phase 3 |
| 38 | [TECH] | C-G3 | Map 遍历非确定性，多错误时输出顺序不稳定 | Phase 3 |
| 39 | [TECH] | C-G4 | TOPO-04 检查 ownerCell 而非 provider actor 的一致性级别 | Phase 3 |
| 40 | [TECH] | C-G5 | 缺少 cell.verify.smoke / slice.verify.unit 非空校验 | Phase 3 |
| 41 | [TECH] | C-G6 | 缺少禁用字段名 (cellId/sliceId 等) 检测规则 | Phase 3 |
| 42 | [TECH] | C-G7 | FMT-09/FMT-08 调用顺序导致 invalid-kind 同时报两个错误 | Phase 3 |
| 43 | [TECH] | C-M1 | Registry 线程安全未文档化（build-once-read-many） | Phase 3 |
| 44 | [TECH] | C-M2 | Parser 接受 id: "" 不报错 | Phase 3 |
| 45 | [TECH] | C-M4 | Catalog CellJourneys/ContractJourneys O(n*m) 无索引 | Phase 3 |
| 46 | [TECH] | C-M5 | ContractRegistry.Consumers() 方法名与禁用 YAML 字段名碰撞 | Phase 3 |
| 47 | [TECH] | C-M6 | StatusBoardEntry YAML tag journeyId 需确认命名约定 | Phase 3 |
| 48 | [TECH] | C-L1 | Start 和 StartWithConfig ~95% 重复代码 ~40 行 | Phase 3 |
| 49 | [TECH] | C-L2 | Stop 允许从 stateStopped 重复调用 | Phase 3 |
| 50 | [TECH] | C-L3 | BaseCell 无线程安全（Health/Ready 可从不同 goroutine 调用） | Phase 3 |
| 51 | [TECH] | C-L4 | outbox.Entry.Metadata 未测试 | Phase 3 |
| 52 | [TECH] | C-L6 | contract ID 格式不一致：scaffold 用点分 vs generator 用斜杠 | Phase 3 |
| 53 | [TECH] | C-AC1 | issueToken + TokenPair + TTL 常量在 login/refresh 重复 | Phase 3 |
| 54 | [TECH] | C-AC2 | Session refresh 存在 TOCTOU 竞态（并发 refresh 覆盖） | Phase 3 |
| 55 | [TECH] | C-AC3 | "already revoked is idempotent" 测试实际未测试幂等性 | Phase 3 |
| 56 | [TECH] | C-AC4 | Service 层 Create 返回含 PasswordHash 的 domain.User（handler 已用 DTO 但 service 接口泄露） | Phase 3 |
| 57 | [TECH] | C-AC5 | Session.ExpiresAt 追踪 access token 过期而非 session 过期 | Phase 3 |
| 58 | [TECH] | C-AC6 | UserRepository.Update byName 索引改名时残留旧条目 | Phase 3 |
| 59 | [TECH] | C-AC7 | 无 JWT jti claim，token 不可单独撤销 | Phase 3 |
| 60 | [TECH] | C-DC5 | configsubscribe unmarshal 失败 ACK 而非 dead letter | Phase 3 |
| 61 | [TECH] | C-DC6 | auditappend publish 失败仅 log 不重试（L3 cell 缺 outbox 保证） | Phase 3 |
| 62 | [TECH] | C-DC7 | configpublish.Rollback 不校验 version > 0 | Phase 3 |
| 63 | [TECH] | C-DC8 | config-core handler 直接依赖 chi.URLParam — router 耦合 | Phase 3 |
| 64 | [TECH] | C-DC9 | auditarchive 是纯 stub，ArchiveStore 已定义但未接线 | Phase 3 |
| 65 | [TECH] | C-H1 | statusRecorder 在 3 个包重复，不支持 Flusher/Hijacker | Phase 3 |
| 66 | [TECH] | C-H2 | 默认 middleware chain 缺 RateLimit | Phase 3 |
| 67 | [TECH] | C-H4 | HSTS 缺 includeSubDomains | Phase 3 |
| 68 | [TECH] | C-H5 | access_log_test.go slog.SetDefault 测试隔离 bug | Phase 3 |
| 69 | [TECH] | C-S1 | auth middleware 用 slog.Warn 而非 slog.WarnContext — 无 request_id | Phase 3 |
| 70 | [TECH] | C-S2 | shutdown.Manager 第一个 hook 失败中断剩余 hook | Phase 3 |
| 71 | [TECH] | C-S3 | shutdown.Manager FIFO 顺序而非 LIFO（bootstrap 补偿但 API 误导） | Phase 3 |
| 72 | [TECH] | C-S4 | config watcher 无 debounce | Phase 3 |
| 73 | [TECH] | C-S5 | EventBus "bus is closed" 用 fmt.Errorf 而非 errcode | Phase 3 |
| 74 | [TECH] | C-S6 | Worker.Stop 注释说 reverse order 但实际并发执行 | Phase 3 |
| 75 | [TECH] | C-S7 | PeriodicWorker 正常关闭返回 context.Canceled 被 log 为 Error | Phase 3 |
| 76 | [TECH] | C-P1 | mapCodeToStatus 用 strings.Contains 匹配 — 顺序敏感、歧义 | Phase 3 |
| 77 | [TECH] | C-P2 | WriteJSON 忽略 json.Encode 错误 | Phase 3 |
| 78 | [TECH] | C-P3 | CLI exit code 不区分（usage error / validation error 都是 1） | Phase 3 |

## 统计
- [TECH] 新增: 23 条 (S6) + 49 条 (review-031) = 72 条
- [PRODUCT] 新增: 3 条 (S6) + 1 条 (review-031) = 4 条
- 本 PR 已修复: 路由 404 + logout persist + INVALID_TOKEN 映射 + JWT key 校验 + workerErrCh + ServiceToken nil secret
- 上一 Phase 遗留已解决: 0 条（首次启用工作流）
- **S6 #2 (cells/ import chi)** 已在本 PR 修复（RouteMux 扩展后 Cell 不再直接 import chi）
- **S6 #10 (refresh token 验证未检查 signing method)** 列为本 PR 待修项
