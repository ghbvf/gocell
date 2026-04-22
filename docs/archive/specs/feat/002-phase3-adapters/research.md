# Research — Phase 3: Adapters

> 对标框架参考研究，基于 `docs/references/framework-comparison.md`

---

## 1. Outbox Pattern — Watermill watermill-sql

**参考**: `ThreeDotsLabs/watermill` → `watermill-sql/pkg/sql/`

关键设计提取:
- `SchemaAdapter` 接口抽象 SQL 方言差异（PostgreSQL/MySQL）
- `Publisher` 写入 outbox 表，使用调用方传入的 `*sql.Tx`（context-embedded）
- `Subscriber` 轮询使用 `SELECT ... FOR UPDATE SKIP LOCKED`
- 批量拉取 + 标记已消费，周期清理已处理消息
- 发布端 confirm mode 保证 at-least-once

**GoCell 采纳**: context-embedded tx 模式、`FOR UPDATE SKIP LOCKED` 轮询、batch 处理
**GoCell 偏离**: 不使用 SchemaAdapter 抽象（仅支持 PostgreSQL），简化实现

## 2. RabbitMQ Consumer — Watermill watermill-amqp

**参考**: `ThreeDotsLabs/watermill` → `watermill-amqp/pkg/amqp/`

关键设计提取:
- `Config.Reconnect` 结构体含 backoff 参数
- `Subscriber` 每 topic 一个 goroutine，自动 reconnect
- ACK/NACK 机制：handler 返回 error → NACK（requeue or DLQ）
- 连接级和 channel 级错误分别处理

**GoCell 采纳**: 自动重连 + exponential backoff、ACK/NACK 语义、channel 池
**GoCell 偏离**: GoCell 的 ConsumerBase 内置 idempotency.Checker 调用，Watermill 不内置

## 3. Redis Store — go-micro

**参考**: `micro/go-micro` → `store/redis/`

关键设计提取:
- `Options` struct 含 Nodes, Database, Table 配置
- `Read/Write/Delete` 统一接口
- TTL 在 Write 时指定

**GoCell 采纳**: Config struct 模式、TTL-based 操作
**GoCell 偏离**: 额外提供 DistLock 和 IdempotencyChecker（go-micro 无此功能）

## 4. OIDC Client — coreos/go-oidc

**参考**: `coreos/go-oidc` → `oidc.go`, `verify.go`

关键设计提取:
- `Provider` 通过 Discovery URL 自动加载 metadata
- `IDTokenVerifier` 含 `VerifyConfig`（Audience, Now func）
- JWKS 公钥缓存 + kid rotation 透明处理

**GoCell 采纳**: Discovery 自动加载、JWKS 缓存、kid rotation
**GoCell 偏离**: 自行实现而非直接依赖 go-oidc（减少间接依赖）

## 5. S3 Client — minio/minio-go

**参考**: `minio/minio-go` → `api.go`

关键设计提取:
- `Options` 含 Endpoint, Creds, Secure, Region
- `PutObject/GetObject/RemoveObject` + `PresignedPutObject/PresignedGetObject`
- 自动分片上传大文件

**GoCell 采纳**: 基础 CRUD + Presigned URL
**GoCell 偏离**: Phase 3 不实现分片上传（审计归档为小文件 JSON）

## 6. WebSocket — nhooyr.io/websocket

**参考**: `nhooyr.io/websocket` → `examples/chat/`

关键设计提取:
- `websocket.Accept(w, r, opts)` 升级 HTTP
- `conn.Read/Write` 简洁 API
- 内置 ping/pong + close 处理
- `InsecureSkipVerify` 仅用于测试

**GoCell 采纳**: Accept 升级模式、ping/pong lifecycle
**GoCell 偏离**: signal-first 模式（不推送完整数据，仅推送刷新信号）
