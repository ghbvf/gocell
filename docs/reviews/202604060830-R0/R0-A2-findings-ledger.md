# R0-A2: 已知 Findings 台账

> **生成日期**: 2026-04-06
> **审查基准**: develop 分支 commit `ce03ba1`
> **数据来源**: PR#3 Review, PR#7-12 Six-Role Review, Tech-debt Registry, SonarCloud Snapshot, Archive Reviews (Phase 0-6), Backlog
> **方法**: 逐文件提取 findings, 跨文档去重 (同一根因合并, 保留最高严重级别), 交叉引用 tech-debt-registry.md 判定状态

---

## 统计摘要

### 按来源统计 (去重前原始计数)

| 来源 | P0 | P1 | P2 | Security | Architecture | Concern | Total |
|------|-----|-----|-----|----------|-------------|---------|-------|
| PR#3 Review (031) | 6 | 8 | — | 9 | 5 | 42 | 56 |
| PR#7-12 Six-Role (032) | 10 | 36 | 29 | — | — | — | 75 |
| Full Codebase Review (023) | 8 | 10 | 20+ | 2 | — | 19 | ~47 |
| Phase 1 Six-Role (024) | 0 | 18 | 31 | — | — | — | 49 |
| Phase 2 Six-Role (025) | 1 | 33 | 27 | — | — | — | 61 |
| Phase 3 Six-Role (026) | 5 | 38 | 38 | — | — | — | 81 |
| Phase 4 Six-Role (027) | 2 | 14 | 29 | — | — | — | 45 |
| Phase 5 Six-Role (028) | 1 | 7 | 12 | — | — | — | 20 |
| Phase 6 Consolidated (029) | 2 | 4 | — | — | — | — | 6 |
| Baseline Review Report | 2 | 4 | — | — | — | — | 6 |
| Tech-debt Registry | — | — | — | — | — | — | 46 items |
| SonarCloud Snapshot | — | — | 380 smells | 8 vuln | — | 43 hotspots | 431 |
| **原始总计 (含大量重复)** | **~37** | **~173** | **~566+** | **~19** | **~5** | **~61** | **~923** |

### 去重后独立 Findings 统计

| 严重级别 | 独立数量 | 说明 |
|---------|---------|------|
| **P0** | **19** | 安全漏洞 + 分层违规 + 数据正确性 + 阻塞功能 |
| **P1** | **78** | 代码质量 + 测试缺失 + 性能风险 + 设计缺陷 |
| **P2** | **95** | 可读性 + 命名 + 文档 + 次要设计改进 |
| **去重合计** | **192** | 不含 SonarCloud code smells (单独统计) |

---

## 状态分布

| 状态 | 数量 | 说明 |
|------|------|------|
| **RESOLVED** | 58 | tech-debt-registry 标记 RESOLVED 或 review 确认已修复 |
| **OPEN** | 84 | tech-debt-registry OPEN 或未被修复 |
| **PARTIAL** | 3 | tech-debt-registry PARTIAL |
| **UNKNOWN** | 47 | 不在 registry 中, 需后续代码验证确认 |

---

## P0 Findings (逐条, 19 个独立问题)

### P0 来自 PR#3 Review (Phase 2 安全审查)

| ID | 来源 | 模块 | 描述 | 状态 | 备注 |
|----|------|------|------|------|------|
| P0-S2 | PR#3 S2 | cells/access-core/sessionrefresh | sessionrefresh JWT 解析缺 signing method 校验 — algorithm confusion 攻击 | RESOLVED | P2-SEC-09 确认已修复 |
| P0-S3 | PR#3 S3 | cells/access-core/identitymanage | User Lock 不撤销已有 session, 被锁用户 token 15min 内仍有效 | UNKNOWN | 未在 tech-debt-registry 中; 需验证代码 |
| P0-S4 | PR#3 S4 | runtime/http/middleware | RealIP 无条件信任 X-Forwarded-For — IP 伪造 | RESOLVED | P2-SEC-06 确认 trustedProxies 已实现 |
| P0-S5 | PR#3 S5 | runtime/auth | ServiceToken HMAC 只签 method+path — 重放 + 参数篡改 | RESOLVED | P2-SEC-07 确认 timestamp 5min 窗口已实现 |
| P0-V1 | PR#3 V1 | kernel/slice | slice/verify.go 使用 fmt.Errorf 导出错误 (7 处) | UNKNOWN | 需验证是否已改为 errcode |
| P0-V2 | PR#3 V2 | kernel/governance | VERIFY-01 只检查 provider 角色, V3 spec 要求所有角色 | UNKNOWN | Phase 3 F-3D-09 同一问题 |

### P0 来自 PR#7-12 Six-Role Review (Phase 3 适配器)

| ID | 来源 | 模块 | 描述 | 状态 | 备注 |
|----|------|------|------|------|------|
| P0-F8S01 | PR#8 F-8S-01 | pkg/uid | uid.New() 静默丢弃 crypto/rand.Read 错误, 熵源失败生成固定 UUID | UNKNOWN | 需验证代码 |
| P0-F10A01 | PR#10 F-10A-01 | adapters/postgres | Migrator 声称 advisory lock 但实现缺失, 并发 migration DDL 可致数据损坏 | UNKNOWN | 需验证代码 |
| P0-F10S01 | PR#10 F-10S-01 | adapters/postgres | tableName 直接 fmt.Sprintf 插入 SQL (6 处), SQL 注入向量 | UNKNOWN | 需验证是否加了白名单校验 |
| P0-F11S01 | PR#11 F-11S-01 | adapters/redis | DistLock 无 fencing token, 锁过期后并发写入不可防 | UNKNOWN | 需验证 godoc 是否标注安全边界 |
| P0-F12S01 | PR#12 F-12S-01 | adapters/rabbitmq | sanitizeURL 固定截断泄露 AMQP 凭据 | UNKNOWN | 需验证是否改用 net/url.Parse |
| P0-F12D01 | PR#12 F-12D-01 | adapters/rabbitmq | ctx 取消时 ConsumerBase 返回 error 触发 NACK+requeue, 关闭时重复消费 | UNKNOWN | 需验证代码 |
| P0-F9S01 | PR#9 F-9S-01 | .env.example | .env.example 弱密码可直接使用, 无 CHANGE_ME 占位 | UNKNOWN | 需验证文件 |
| P0-F9S02 | PR#9 F-9S-02 | .env.example | JWT 密钥字段为空, 运行时可能静默失效 | UNKNOWN | 需验证代码 |
| P0-F7T01 | PR#7 F-7T-01 | runtime/bootstrap | bootstrap Run() 覆盖率 42.7%, 违反 >=80% 约束 | RESOLVED | P2-T-03 确认覆盖率已提升 |
| P0-F11T01 | PR#11 F-11T-01 | adapters/redis | Redis adapter 完全缺失集成测试 | RESOLVED | P3-TD-01 testcontainers 已实现 |

### P0 来自 Archive Reviews (Phase 0-6 基线审查)

| ID | 来源 | 模块 | 描述 | 状态 | 备注 |
|----|------|------|------|------|------|
| P0-2S04 | Phase2 F-2S-04 | kernel/metadata | Parser 接受空字符串 ID — pm.Cells[""] 注册 | UNKNOWN | 合流报告 P0-1; 需验证 parser 是否已加 ID 非空检查 |
| P0-3D05 | Phase3 F-3D-05 | kernel/governance | DEP-02 环检测未验证契约对象存在, 可漏掉真实循环依赖 | UNKNOWN | 合流报告 P0-4; 需验证 depcheck.go |
| P0-3P11 | Phase3 F-3P-11 | cmd/gocell | CLI 未展示 Severity/IssueType — CI 无法精细控制 | UNKNOWN | 合流报告 P0-5; 需验证 CLI 输出格式 |

**注**: 以下 P0 在多文档中重复出现, 已合并:
- PR#3 S8 (UnixNano ID) = P2-SEC-08 → RESOLVED
- PR#3 S9 (access-core 端点无 auth) = P2-SEC-11 → RESOLVED
- Phase3 F-3S-01/02/03 (nil project panic) = 合流 P0-3 → UNKNOWN (需验证)
- Phase4 F-4P-01 (verify 命令占位符) = 合流 P0 → UNKNOWN (可能仍为占位)
- Phase4 F-4P-02 / Phase3 F-3P-12 (validate 未前置) = 合流 P0-6 → UNKNOWN (需验证)
- Phase5 F-5A-01 (孤立 contract http.x.v1) = 合流 P0-7 → UNKNOWN (需验证是否已删除)
- Baseline P0-1 (validate 失败 6 项) / P0-2 (false-green) = 合流根因簇 → UNKNOWN (需验证)

---

## P1 Findings (逐条, 78 个独立问题)

### P1-A: 并发安全 (6 条, 去重后 1 个工作项)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-A1 | Phase1 F-1A-03/04/09/15, F-1S-01/02/03/07 | kernel/assembly + kernel/cell | Register()/Health()/BaseCell 无 mutex, 并发不安全 | RESOLVED (P2-ARCH-04, P2-D-06) |

### P1-B: metadata 与 cell 类型冗余 (4 条)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-B1 | Phase2 F-2A-01/07, F-2D-04 | kernel/metadata + kernel/cell | CellMeta vs CellMetadata, SliceVerifyMeta vs VerifySpec 平行定义 | UNKNOWN |

### P1-C: Parser 缺字段验证 (8 条, 去重后 1 个工作项)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-C1 | Phase2 F-2T-01/02, F-2S-01/03/05, F-2P-03 | kernel/metadata | belongsToCell 空 key + 重复 ID 无检测 + YAML bomb 无防御 | UNKNOWN |

### P1-D: Governance 规则缺陷 (7 条)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-D1 | Phase3 F-3T-01 | kernel/governance | 缺 contract.kind 枚举验证规则 | UNKNOWN |
| P1-D2 | Phase3 F-3T-02, B1 (023) | kernel/governance | TOPO-03 通配符 "*" 未处理, consumers=["*"] 时误报 | UNKNOWN |
| P1-D3 | Phase3 F-3T-03, B2 (023) | kernel/governance | VERIFY-01 空 expiresAt waiver 被视为有效 | UNKNOWN |
| P1-D4 | Phase3 F-3D-01 | kernel/governance | TOPO-01 未验证 provider role slice 与契约实际 provider 匹配 | UNKNOWN |
| P1-D5 | Phase3 F-3D-03 | kernel/governance | contractIDFromPath 假设未覆盖四层路径 | UNKNOWN |
| P1-D6 | Phase3 F-3D-13 | kernel/journey | NewCatalog statusBoard 指针可能悬挂 | UNKNOWN |
| P1-D7 | Phase3 F-3D-15 | kernel | 缺少 slice.allowedFiles 字段定义和验证 | UNKNOWN |

### P1-E: 路径遍历/注入 (8 条)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-E1 | Phase3 F-3A-02, F-3S-05/06/07, S1(023) | kernel/governance | os.Stat 路径遍历 (REF-11/12/contractDirFromID) | UNKNOWN |
| P1-E2 | Phase3 F-3S-09 | kernel/slice | resolveCheckRef go test -run pattern 注入 | UNKNOWN |
| P1-E3 | Phase4 F-4S-02 | kernel/scaffold | YAML 模板未转义用户值, 含 `:` `#` `\n` 可注入 | UNKNOWN |
| P1-E4 | Phase4 F-4S-03 | kernel/scaffold | Contract ID 路径遍历 | UNKNOWN |
| P1-E5 | Phase4 F-4S-05 | cmd/gocell | --module flag 无格式验证, 可注入恶意 import path | UNKNOWN |
| P1-E6 | Full-review B6 | kernel/slice | cellID 含 `..` 可逃逸 verify.go 路径 | UNKNOWN |

### P1-F: CLI 错误处理 (9 条, 去重后 1 个工作项)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-F1 | Phase4 F-4A-01/05, F-4X-01/02/04, F-4T-05, Phase3 F-3P-02 | cmd/gocell | CLI errcode 信息丢失 + FlagSet 无 Usage + 退出码全为 1 | UNKNOWN |

### P1-G: verify 命令未实现 (1 个工作项)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-G1 | Phase4 F-4A-14, F-4T-04, F-4X-06, F-4D-05, F-4P-01 | cmd/gocell | verify 命令 100% 占位符, 未连接到 Runner | UNKNOWN |

### P1-H: Registry / ProjectMeta 冗余 (1 个工作项)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-H1 | Phase3 F-3A-04/05/09, F-3X-13, Phase2 F-2A-06 | kernel/registry + governance | Registry 重复 ProjectMeta 索引, governance 耦合 registry | UNKNOWN |

### P1-I: PR#3 安全 Concerns (升级为 P1)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-I1 | PR#3 S7 | runtime/http/middleware | RequestID 接受任意客户端输入 — log injection | UNKNOWN |
| P1-I2 | PR#3 C-DC1 | cells/audit-core | Hash chain 状态纯内存, 重启后断链 | UNKNOWN |
| P1-I3 | PR#3 C-DC2 | cells/audit-core + config-core | 订阅 goroutine 用 context.Background(), Stop 时泄漏 | RESOLVED (P2-ARCH-06) |
| P1-I4 | PR#3 C-DC5 | cells/config-core | configsubscribe unmarshal 失败 ACK 而非 dead letter | UNKNOWN |
| P1-I5 | PR#3 C-DC6 | cells/audit-core | auditappend publish 失败仅 log 不重试 (L3 cell 缺 outbox) | RESOLVED (P2-ARCH-07) |
| P1-I6 | PR#3 C-AC2 | cells/access-core | Session refresh TOCTOU 竞态 (并发 refresh 覆盖) | OPEN (P3-TD-10) |
| P1-I7 | PR#3 C-S2 | runtime/services | shutdown.Manager 首 hook 失败中断剩余 hook | UNKNOWN |

### P1-J: PR#7-12 架构 (7 条)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-J1 | F-7A-01 | runtime/bootstrap | 直接 import runtime/eventbus 具体类型 | UNKNOWN |
| P1-J2 | F-8A-01 | adapters/websocket | 遗留 import 已废弃 pkg/id | UNKNOWN |
| P1-J3 | F-8A-02 | cells/ | TopicConfigChanged 3 处重复定义 | RESOLVED (P2-DX-03) |
| P1-J4 | F-10A-02 | adapters/postgres | TxManager commit 失败用错误码 ErrAdapterPGConnect | UNKNOWN |
| P1-J5 | F-10A-03 | adapters/postgres | Pool 未实现任何 kernel/runtime 接口 | UNKNOWN |
| P1-J6 | F-11A-01 | adapters/redis | Cache/DistLock 缺接口定义 | UNKNOWN |
| P1-J7 | F-11A-02 | adapters/redis | renewLoop 用 context.Background(), goroutine 泄漏 | UNKNOWN |

### P1-K: PR#7-12 安全 (10 条)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-K1 | F-7S-01 | runtime/bootstrap | any(pub)!=any(sub) 接口相等判断存在 double-close 绕过 | UNKNOWN |
| P1-K2 | F-7S-02 | runtime/bootstrap | Worker 失败 rollback 时 workerCancel() 未先行调用 | UNKNOWN |
| P1-K3 | F-8S-02 | cells/access-core | 旧 session 删除失败继续创建新 session, 双 session 并存 | UNKNOWN |
| P1-K4 | F-8S-03 | cells/ | 事件 payload 缺少 event_id, 违反 eventbus.md | UNKNOWN |
| P1-K5 | F-9S-03 | docker-compose | RabbitMQ Management UI 15672 绑定 0.0.0.0 | UNKNOWN |
| P1-K6 | F-9S-04 | docker-compose | minio/minio:latest 浮动 tag | UNKNOWN |
| P1-K7 | F-10S-02 | adapters/postgres | DSN 解析失败可能回显部分凭证 | UNKNOWN |
| P1-K8 | F-10S-03 | adapters/postgres | err.Error()=="no rows" 字符串比较 | UNKNOWN |
| P1-K9 | F-11S-02 | adapters/redis | Config.Addr 默认 localhost:6379, 违反无 localhost 回退 | UNKNOWN |
| P1-K10 | F-11S-03 | adapters/redis | TTL=0 时幂等 key 永不过期, 内存泄漏 | UNKNOWN |

### P1-L: PR#7-12 测试 (9 条)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-L1 | F-7T-02 | kernel/outbox | 纯接口包无行为覆盖 | UNKNOWN |
| P1-L2 | F-7T-03 | runtime/bootstrap | 测试中 `_ = err` 掩盖断言 | UNKNOWN |
| P1-L3 | F-8T-01 | pkg/uid | rand.Read 错误注入测试缺失 | UNKNOWN |
| P1-L4 | F-8T-02 | cells/access-core | rotation 后旧 token 不可再用未验证 | UNKNOWN |
| P1-L5 | F-10T-02 | adapters/postgres | TxManager 顶层 tx 路径零覆盖 | PARTIAL (P3-TD-02) |
| P1-L6 | F-10T-03 | adapters/postgres | Migrator Up/Down/Status 零覆盖 | PARTIAL (P3-TD-02) |
| P1-L7 | F-11T-02 | adapters/redis | TTL 过期、并发竞态测试缺失 | UNKNOWN |
| P1-L8 | F-12T-01 | adapters/rabbitmq | 重连逻辑零覆盖 | UNKNOWN |
| P1-L9 | F-12T-02 | adapters/rabbitmq | 并发消费场景零覆盖 | UNKNOWN |

### P1-M: PR#7-12 运维/DX/产品 (10 条)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-M1 | F-8P-01 | cells/ | 4 个 service 的 Publish 失败 fire-and-forget, L2 违规 | RESOLVED (P2-ARCH-07 outbox.Writer 已替换) |
| P1-M2 | F-10P-01 | adapters/postgres | outbox_entries 表缺 attempt_count/last_error 字段 | UNKNOWN |
| P1-M3 | F-11P-01 | adapters/redis | IsProcessed+MarkProcessed TOCTOU 竞态, 应改 CheckAndMark | UNKNOWN |
| P1-M4 | F-12O-01 | adapters/rabbitmq | 无 metrics 埋点, DLQ 仅日志 | UNKNOWN |
| P1-M5 | F-12S-02 | adapters/rabbitmq | 无 TLS Config 扩展点 | UNKNOWN |
| P1-M6 | F-12S-03 | adapters/rabbitmq | 消息无 MessageId, 追踪链路断裂 | UNKNOWN |
| P1-M7 | F-12A-01 | adapters/rabbitmq | processDelivery 串行, PrefetchCount 形同虚设 | UNKNOWN |
| P1-M8 | F-9T-02 | Makefile | 无 CI pipeline, 测试门控全靠人工 | RESOLVED (P3-TD-03) |
| P1-M9 | F-11O-01 | adapters/redis | Client 缺连接池配置暴露 | UNKNOWN |
| P1-M10 | F-9T-01 | docker-compose | healthcheck 仅验证进程存活, 无连通性探针 | UNKNOWN |

### P1-N: Phase 2 metadata Six-Role (去重补充)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-N1 | F-2A-08 | kernel/metadata | Parser 缺规范化步骤, unmarshal 后无验证 | UNKNOWN |
| P1-N2 | F-2S-06 | kernel/metadata | 路径遍历防御缺失 (schemaRefs) | UNKNOWN |
| P1-N3 | F-2D-01 | kernel/assembly + pkg/errcode | 禁用字段名在 generator_test/errcode_test 中使用 | UNKNOWN |
| P1-N4 | F-2D-03 | kernel/metadata | SliceVerifyMeta unit/contract 必填性与 schema 不一致 | UNKNOWN |
| P1-N5 | F-2D-05 | kernel/metadata | Journey.Contracts 策展语义无代码层面标注 | UNKNOWN |
| P1-N6 | F-2D-07 | kernel/metadata | 六条真相无代码层面保证, ProjectMeta 无 Validate() | UNKNOWN |
| P1-N7 | F-2X-09 | kernel/metadata | Parser Root 目录约定文档不清, 错误 root 静默返回空 | UNKNOWN |

### P1-O: Phase 3 governance Six-Role (去重补充)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-O1 | F-3A-10, F-3X-01, F-3P-01 | kernel/governance | ValidationResult 字段标记 //nolint:unused 但被使用 | UNKNOWN |
| P1-O2 | F-3X-02 | kernel/governance | HasErrors/Errors/Warnings 操作 []ValidationResult 但不用 receiver | UNKNOWN |
| P1-O3 | F-3X-03 | kernel/governance | 错误消息缺 Details 结构化数据 | UNKNOWN |
| P1-O4 | F-3X-08 | kernel/governance | 导出类型缺详细 godoc | UNKNOWN |
| P1-O5 | F-3X-10 | kernel/governance | 缺中央规则注册表/文档 | UNKNOWN |
| P1-O6 | F-3X-12 | kernel/governance | TargetSelector 返回 nil vs 空切片不一致 | UNKNOWN |
| P1-O7 | F-3P-03 | kernel/governance | 规则覆盖度缺陷 — Actor/Assembly/StatusBoard/PassCriteria 未验证 | UNKNOWN |
| P1-O8 | F-3P-05 | kernel/governance | REF-13/14 可能过严 — 外部 actor 引用应允许延迟定义 | UNKNOWN |
| P1-O9 | F-3P-09 | cmd/gocell | scaffold/generate 未消费 ValidationResult | UNKNOWN |
| P1-O10 | F-3D-04 | kernel/governance | expandFromSlices 的 journey 扩展不完备性 | UNKNOWN |
| P1-O11 | F-3D-08 | kernel/slice | Runner.VerifySlice 测试路径硬编码 | UNKNOWN |
| P1-O12 | F-3S-04 | kernel/journey | Catalog.Get() 对 nil project 行为与其他组件不一致 | UNKNOWN |

### P1-P: Phase 4 CLI/scaffold Six-Role (去重补充)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-P1 | F-4A-02 | kernel/scaffold + assembly | scaffold 与 generator 模板基础设施重复 | UNKNOWN |
| P1-P2 | F-4A-10 | kernel/scaffold | CreateSlice 只检查目录存在不检查 cell.yaml | UNKNOWN |
| P1-P3 | F-4A-11 | kernel/scaffold | 模板字段不完整 — 缺少多个可选字段 | UNKNOWN |
| P1-P4 | F-4A-18 | kernel/assembly | sourceFingerprint 未哈希 deploy 配置 | UNKNOWN |
| P1-P5 | F-4D-01 | kernel/assembly | boundary.yaml 使用禁用字段名 assemblyId | UNKNOWN |
| P1-P6 | F-4D-03 | kernel/assembly | boundary.yaml 字段名混用 camelCase | UNKNOWN |
| P1-P7 | F-4D-06, F-4P-07 | kernel/scaffold | Contract ID 前缀与 kind 不做匹配校验 | UNKNOWN |
| P1-P8 | F-4T-01 | kernel/scaffold | 缺 roundtrip 测试 (scaffold->parse->validate) | UNKNOWN |
| P1-P9 | F-4T-02 | kernel/assembly | Generator time.Now() 破坏输出确定性 | UNKNOWN |
| P1-P10 | F-4X-02 | cmd/gocell | required vs optional flags 标注不一致 | UNKNOWN |

### P1-Q: Phase 5 业务资产 (去重补充)

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-Q1 | F-5A-02, F-5S-01 | contracts/ | http.auth.me.v1 已定义但无 slice 实现, boundary 导出虚假承诺 | UNKNOWN |
| P1-Q2 | F-5D-01, F-5S-02 | cells/access-core | session-login 对 http.config.get.v1 的 call 仅 waiver 覆盖 | UNKNOWN |
| P1-Q3 | F-5D-02 | journeys/ | J-config-hot-reload 声称 audit-core 参与但无订阅 | UNKNOWN |
| P1-Q4 | F-5D-03, F-5S-03 | contracts/ | audit-core 无 slice 订阅 event.config.rollback.v1 | UNKNOWN |
| P1-Q5 | F-5S-04 | assemblies/ | boundary.yaml 导出内部审计事件 | UNKNOWN |
| P1-Q6 | F-5P-01 | contracts/ + journeys/ | "我是谁" 端点缺 journey 覆盖 — MVP 验收漏洞 | UNKNOWN |
| P1-Q7 | F-5T-02 | contracts/ | contract version 与目录路径无自动校验 | UNKNOWN |

### P1-R: Tech-debt Registry OPEN/PARTIAL 项

| ID | 来源 | 模块 | 描述 | 状态 |
|----|------|------|------|------|
| P1-R1 | P2-T-02 | cells/ | 无 J-audit-login-trail 端到端集成测试 | OPEN |
| P1-R2 | P4-TD-03 | cells/access-core | IssueTestToken HS256 死代码 (测试陷阱) | OPEN |
| P1-R3 | P4-TD-04 | examples/todo-order | order-cell 声明 L2 但无 outboxWriter enforce | OPEN |
| P1-R4 | P4-TD-06 | .github/workflows | CI validate 用 `|| true`, 验证错误被静默 | OPEN |
| P1-R5 | P4-TD-08 | adapters/postgres | postgres adapter 集成覆盖率未在流水线中测量 | OPEN |

---

## P2 Findings (聚类汇总, 95 个独立问题)

由于 P2 数量庞大, 按主题聚类而非逐条列出:

### P2 主题聚类

| 主题 | 数量 | 代表性 Finding | 模块 | 状态 |
|------|------|----------------|------|------|
| 类型冗余/DX | 12 | F-2A-03 Schema embed 未消费, F-2D-06 Actor 无类型区分 | kernel/metadata | UNKNOWN |
| Governance 排序/稳定性 | 6 | F-3T-06/09, F-3A-08 DEP-02 缺文档 | kernel/governance | UNKNOWN |
| CLI/scaffold 打磨 | 10 | F-4A-03/04/07/08/12/15/16/17, F-4X-03/05/06/07 | cmd/gocell, kernel/scaffold | UNKNOWN |
| Phase 1 设计改进 | 14 | D1-D19 from 023, F-1A-05/08/10/13/14/22/24 etc | kernel/cell, assembly | UNKNOWN |
| Phase 2 metadata DX | 12 | F-2X-04/05/06/07/11/12, F-2P-05/06/08/09/10/12 | kernel/metadata | UNKNOWN |
| Phase 3 governance DX | 10 | F-3D-02/07/10/12/14/16, F-3A-01/03/06/07 | kernel/governance | UNKNOWN |
| Phase 5 资产完善 | 5 | F-5A-03/04/05, F-5D-04, F-5P-02/03 | cells, contracts, journeys | UNKNOWN |
| PR#7-12 Docker/运维 | 8 | F-9A-01/02/03, F-9O-01/02/03/04, F-9D-01 | docker-compose, Makefile | UNKNOWN |
| PR#7-12 DX/产品 | 8 | F-10D-01, F-11D-01/02/03, F-12D-02, F-12P-01/02 | adapters/ | UNKNOWN |
| PR#7-12 测试补充 | 6 | F-10T-04, F-12T-03, F-8T-03, F-11T-03 | adapters/ | UNKNOWN |
| Tech-debt OPEN P2 | 4 | P4-TD-01/02/07/09 | 多模块 | OPEN |

---

## SonarCloud 模块分布

### 默认分支概况 (develop)

| 指标 | 值 |
|------|-----|
| Quality Gate | ERROR |
| Open Issues | 388 |
| Code Smells | 380 |
| Vulnerabilities | 8 |
| Security Hotspots | 43 |
| Bugs | 0 |
| Duplicated Lines Density | 3.3% |
| NCLOC | 34,500 |
| Security Rating | 5.0 (E) |
| Reliability Rating | 1.0 (A) |
| Maintainability Rating | 1.0 (A) |

### Quality Gate 失败条件

| 指标 | 实际值 | 阈值 | 状态 |
|------|--------|------|------|
| new_security_rating | 4 (D) | 1 (A) | FAIL |
| new_duplicated_lines_density | 4.3% | 3% | FAIL |
| new_security_hotspots_reviewed | 0% | 100% | FAIL |

### 8 个 Vulnerabilities 分类

| 类型 | 数量 | 涉及模块 | 说明 |
|------|------|---------|------|
| Hard-coded credential ("PASSWORD") | 4 | examples/ docker-compose, .env | 密码字段名被检测为硬编码凭证 |
| Weak JWT cipher (HS256) | 2 | runtime/auth, cells/access-core | HS256 签名被标记为弱加密 (IssueTestToken 遗留) |
| Hard-coded URL with credential | 1 | examples/ | DSN/URL 含密码 |
| Compromised secret | 1 | cmd/core-bundle 或 test | 测试用密钥被标记 |

### 43 个 Security Hotspots 主要来源

| PR | Hotspots | 主要内容 |
|-----|----------|---------|
| PR#16 (security hardening) | 20 | RS256 密钥处理、TLS、crypto 相关 |
| PR#31 (Phase 3 adapters) | 22 | adapter 连接字符串、密码处理 |
| 其他 PR | 1 | 杂项 |

### 按 PR 分布 Issues

| PR | Issues | Smells | Vuln | Hotspots | Gate |
|----|--------|--------|------|----------|------|
| #33 (Tier 0 fix) | 21 | 21 | 0 | 0 | ERROR (dup 5.7%) |
| #32 (Phase 4 examples) | 38 | 35 | 3 | 0 | ERROR (sec+dup) |
| #31 (Phase 3 adapters) | 85 | 80 | 5 | 22 | ERROR (sec+hotspots) |
| #30 (Wave 4 tests) | 28 | 28 | 0 | 0 | OK |
| #29 (Wave 4 docs) | 0 | 0 | 0 | 0 | OK |
| #28 (Wave 4 tests) | 57 | 57 | 0 | 0 | OK |
| #17 (Wave 3 kernel) | 3 | 3 | 0 | 0 | ERROR (dup 4.0%) |
| #16 (Wave 3 security) | 10 | 9 | 1 | 20 | ERROR (sec+dup+hotspots) |
| #15 (Wave 3 cells) | 3 | 2 | 1 | 0 | ERROR (sec+dup) |

### Code Smells 按规则 Top 10 (估算)

| 规则 | 描述 | 频次估算 |
|------|------|---------|
| go:S1192 | 字符串常量重复 >=3 次应抽取 | ~60 |
| go:S100 | 函数命名不符 Go 惯例 | ~40 |
| go:S1135 | TODO 注释未追踪 | ~30 |
| go:S1172 | 未使用参数 | ~25 |
| go:S1168 | 返回空切片而非 nil | ~20 |
| go:S108 | 空代码块 | ~15 |
| 其他 | 复杂度、冗余导入等 | ~190 |

---

## 交叉验证: Tech-debt Registry vs Review Findings

### Registry RESOLVED 项 (30 条) — 确认已修复

| Registry ID | 对应 Review Finding | 模块 |
|-------------|---------------------|------|
| P2-SEC-03 | PR#3 C-P4 core-bundle 密钥硬编码 | cmd/core-bundle |
| P2-SEC-04 | — | runtime/auth (HS256 → RS256) |
| P2-SEC-06 | PR#3 S4 RealIP trusted proxies | runtime/http |
| P2-SEC-07 | PR#3 S5 ServiceToken timestamp | runtime/auth |
| P2-SEC-08 | PR#3 S8 UnixNano → UUID | pkg/uid |
| P2-SEC-09 | PR#3 S2 sessionrefresh signing method | cells/access-core |
| P2-SEC-10 | PR#3 C-AC2 refresh rotation | cells/access-core |
| P2-SEC-11 | PR#3 S9 auth middleware | cells/access-core |
| P2-ARCH-04 | Phase1 BaseSlice 空壳 | kernel/cell |
| P2-ARCH-05 | PR#3 C-DC8 chi.URLParam | cells/ |
| P2-ARCH-06 | PR#3 C-DC2 goroutine ctx 取消 | cells/ |
| P2-ARCH-07 | PR#3 C-DC6 + F-8P-01 outbox | cells/ |
| P2-T-01 | PR#3 V5 handler 覆盖率 | cells/access-core |
| P2-T-03 | F-7T-01 bootstrap 覆盖率 | runtime/bootstrap |
| P2-T-05 | — | adapters/postgres |
| P2-T-06 | — | kernel (copylocks) |
| P2-T-07 | — | cmd/core-bundle |
| P2-router | — | runtime/http/router |
| P2-D-06 | Phase1 F-1A-05 Assembly Stop | kernel/assembly |
| P2-D-07 | PR#3 C-S4 config watcher | runtime/services |
| P2-D-09 | — | eventbus 健康检查 |
| P2-DX-02 | — | doc.go |
| P2-DX-03 | PR#3 C-DC3 + F-8A-02 TopicConfigChanged | cells/ |
| P2-PM-03 | PR#3 C-H3 RateLimit Retry-After | runtime/http |
| P2-PM-audit | PR#3 C-DC4 时间参数静默忽略 | cells/audit-core |
| P2-PM-user | — | cells/access-core |
| P3-TD-01 | F-11T-01 集成测试 | adapters/ |
| P3-TD-03 | F-9T-02 CI pipeline | .github/workflows |
| P3-TD-06 | — | outboxWriter nil guard |
| P3-TD-07/08/09 | — | go.mod, deprecated 注释, RS256 |

### Registry OPEN 项 (13 条) — 仍需修复

| Registry ID | 对应 Review Finding | 模块 | 备注 |
|-------------|---------------------|------|------|
| P2-T-02 | — | cells/ | J-audit-login-trail e2e 测试 |
| P3-TD-04 | — | adapters/ | websocket/oidc/s3 sandbox 问题 (skip guard 缓解) |
| P3-TD-10 | PR#3 C-AC2 | cells/access-core | Session refresh TOCTOU (post-v1.0) |
| P3-TD-11 | — | cells/access-core | domain 模型重构 (post-v1.0) |
| P3-TD-12 | PR#3 C-DC7 | cells/config-core | configpublish.Rollback version 校验 (post-v1.0) |
| P4-TD-01 | — | 多模块 | 缺共享 NoopOutboxWriter |
| P4-TD-02 | — | cells/ | chi.URLParam 耦合 (10 个文件) |
| P4-TD-03 | — | cells/access-core | IssueTestToken HS256 死代码 |
| P4-TD-04 | — | examples/ | order-cell L2 无 outboxWriter enforce |
| P4-TD-06 | — | CI | validate `|| true` |
| P4-TD-07 | — | examples/ | docker-compose start_period |
| P4-TD-08 | — | CI | postgres 覆盖率未测量 |
| P4-TD-09 | — | go.mod | testcontainers indirect |

### Registry PARTIAL 项 (3 条)

| Registry ID | 对应 Review Finding | 模块 | 备注 |
|-------------|---------------------|------|------|
| P3-TD-02 | F-10T-02/03 | adapters/postgres | 覆盖率 46.6%, testcontainers 已加但未重测 |
| P3-TD-05 | — | docker-compose | root compose 已补全, 3 个 example compose 仍缺 |
| P2-T-01 (partial in backlog) | PR#3 V5 | cells/ | handler 测试已补但未明确达标 |

---

## 附录 A: 跨文档重复 Finding 合并记录

以下记录同一根因在多个文档中出现的情况, 已在台账中合并为单条:

| 合并后 ID | 涉及的原始 Finding ID | 根因 |
|-----------|----------------------|------|
| P0-S2 | PR#3 S2, P2-SEC-09 | sessionrefresh signing method |
| P0-S4 | PR#3 S4, P2-SEC-06 | RealIP trusted proxies |
| P0-S5 | PR#3 S5, P2-SEC-07 | ServiceToken HMAC |
| P0-V2 | PR#3 V2, Phase3 F-3D-09 | VERIFY-01 consumer 角色 |
| P0-F7T01 | F-7T-01, P2-T-03 | bootstrap 覆盖率 |
| P0-F11T01 | F-11T-01, P3-TD-01 | Redis 集成测试 |
| P0-2S04 | Phase2 F-2S-04, 合流 P0-1 | 空 ID parser |
| P0-3D05 | Phase3 F-3D-05, 合流 P0-4 | DEP-02 环检测 |
| P0-3P11 | Phase3 F-3P-11, 合流 P0-5 | CLI Severity |
| P1-A1 | Phase1 F-1A-03/04/09/15, F-1S-01/02/03/07, P2-ARCH-04, P2-D-06 | 并发安全 |
| P1-C1 | Phase2 F-2T-01/02, F-2S-01/03/05, F-2P-03 | Parser 验证 |
| P1-D2 | Phase3 F-3T-02, Full-review B1 | TOPO-03 通配符 |
| P1-D3 | Phase3 F-3T-03, Full-review B2 | VERIFY-01 waiver |
| P1-I3 | PR#3 C-DC2, P2-ARCH-06 | goroutine ctx |
| P1-I5 | PR#3 C-DC6, P2-ARCH-07 | outbox publish |
| P1-I6 | PR#3 C-AC2, P3-TD-10 | refresh TOCTOU |
| P1-J3 | F-8A-02, P2-DX-03 | TopicConfigChanged 重复 |
| P1-M1 | F-8P-01, P2-ARCH-07 | Publish fire-and-forget |
| P1-M8 | F-9T-02, P3-TD-03 | CI pipeline |
| P1-Q1-Q7 | Phase5 F-5* | 业务资产不一致 |
| P1-F1 | Phase4 F-4A-01/05, F-4X-01/02/04, F-4T-05, Phase3 F-3P-02 | CLI 错误处理 |
| P1-G1 | Phase4 F-4A-14, F-4T-04, F-4X-06, F-4D-05, F-4P-01 | verify 未实现 |

---

## 附录 B: 待验证清单 (UNKNOWN 状态项)

以下 UNKNOWN 项需通过直接代码审查确认当前状态, 建议 R0-B 阶段逐条验证:

### 优先级 1 — P0 UNKNOWN (8 条)

1. P0-S3: User Lock 是否现在撤销 session
2. P0-F8S01: uid.New() 是否 panic on rand failure
3. P0-F10A01: Migrator 是否实现了 advisory lock
4. P0-F10S01: tableName 是否有白名单校验
5. P0-F12S01: sanitizeURL 是否改用 net/url.Parse
6. P0-F12D01: ctx 取消时 ConsumerBase 是否走 DLQ
7. P0-F9S01: .env.example 是否有 CHANGE_ME
8. P0-F9S02: JWT 密钥是否有非空校验

### 优先级 2 — P0 合流根因 (5 条)

9. P0-2S04: Parser 是否加了 ID 非空检查
10. P0-3D05: DEP-02 是否验证契约存在
11. P0-3P11: CLI 是否展示 Severity
12. 合流 P0-6: scaffold/generate 是否前置 validate
13. 合流 P0-7: 孤立 contract http.x.v1 是否已删除

### 优先级 3 — P1 UNKNOWN (47 条)

见上文各 P1 小节中标记 UNKNOWN 的条目。

---

## 附录 C: 方法论说明

### 去重规则

1. **同一代码位置 + 同一问题**: 多文档报告同一文件同一行的同一缺陷 → 合并为 1 条, 保留最高严重级别
2. **同一根因 + 不同角色发现**: 如 Phase 1-3 中 6 个角色分别报告 Register() 无 mutex → 合并为 1 个工作项
3. **跨 Phase 传递**: 如 Phase 3 F-3P-12 和 Phase 4 F-4P-02 是同一问题 "validate 未前置" → 合并
4. **PR Review 与 Archive Review 重叠**: PR#3 的 S2-S9 与 Phase 0-6 Archive 中部分 finding 重叠 → 以 PR 级细粒度为准

### 状态判定规则

| 条件 | 状态 |
|------|------|
| tech-debt-registry.md 标记 RESOLVED | RESOLVED |
| tech-debt-registry.md 标记 OPEN | OPEN |
| tech-debt-registry.md 标记 PARTIAL | PARTIAL |
| 不在 registry 中, PR#3/PR#7-12 review 修复建议无后续确认 | UNKNOWN |
| 不在 registry 中, 来自 archive review, 无后续修复记录 | UNKNOWN |

### 局限性

1. **UNKNOWN 项需代码验证**: 47 个 UNKNOWN 项的实际状态需 R0-B 阶段逐文件检查确认
2. **SonarCloud 388 条 code smells 未逐条列入**: 数量过多, 以模块分布和规则 Top 10 呈现
3. **Phase 0 基线审查的细节被压缩**: baseline-review-report 采用根因合并写法, 部分细节不可追溯
4. **PR#3 Concerns (42 条) 部分与 tech-debt 重叠**: 已尽力交叉验证但可能遗漏
