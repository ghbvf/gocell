# ADR: replayable event payload 的客户端 IP 经 keyed-HMAC 单源双侧 funnel 去明文

- 日期：2026-06-05
- 状态：Accepted
- Issue：#1488（明文 clientIp 经 outbox/broker/DLX 扩散）
- 触发来源：PR #1467 多方安全审查 簇 C4 / Finding F5（P1·Cx3·安全）

## 背景

`event.auth.bootstrap-failed.v1` 的 payload 携带**明文 `clientIp`**（PII）。事件由 accesscore 发布 → outbox
（PG `outbox_entries` BYTEA 持久化）→ broker（RabbitMQ）→ auditcore 消费 → audit ledger 落账，失败进 DLX。
明文 IP 因此跨越多个信任边界，并在 **replayable 存储**中长期驻留；且 auditquery HTTP 出口的
`pkg/redaction.RedactPayload` 敏感键集不含 `ip`，明文还会经审计查询 API 出站。

源头 `cellmodules/accesscore/module.go` 的 bootstrap 观察者此前注释明写「slog hashed for observability;
**ledger payload keeps plaintext IP for compliance**」——本 ADR 推翻该决定（下方 §推翻 C3）。

## 决策

replayable event payload 中的客户端 IP 一律以 **keyed、非可逆的 HMAC 哈希**承载，明文永不进入
outbox/broker/DLX 或 audit ledger。落地为 `pkg/redaction` 单源 typed hash 的**双侧 funnel**：

### Sink 侧（Hard）— sealed `redaction.IPHash`

- `type IPHash struct { v string }`：unexported 字段，包外不可结构字面量构造（sealed construction）。
  唯一构造器 `HashIP(salt []byte, ip string) IPHash` = HMAC-SHA256，hex 编码；`ip==""` → 零值（marshal `""`）。
- 生产 DTO `dto.BootstrapAuthFailedEvent.ClientIPHash` 字段类型 = `redaction.IPHash`（非 string）。明文 string
  赋给 sealed 字段是**编译错误** → 生产者物理上不能 emit 明文。
- `RecordBootstrapAuthFail(ctx, reason string, clientIPHash redaction.IPHash)` 全链（cell → setup service）收
  sealed 类型；composition root 在观察者闭包内 `HashIP(salt, ip)` 后传入——cell 永不见明文，secret 留在 root。
- 故意**无 `UnmarshalJSON`**：消费者（auditcore / runtime-audit）把 `clientIpHash` 当普通 string 读取，
  唯一产出 IPHash 的路径只剩 `HashIP`，明文无法经 json.Unmarshal 洗进 IPHash。
- schema `contracts/event/auth/bootstrap-failed/v1/payload.schema.json`：`clientIp` → `clientIpHash`
  （pre-v1.0 直改 v1，无 v2、无弃用期，ADR 202605211200）。无 DB 迁移（payload 为 schema-free BYTEA，无生产数据）。

### Source 侧（Medium）— `ctxkeys.RealIPFrom` caller-allowlist

`ctxkeys.RealIPFrom`（明文 IP 的唯一读取入口）的生产引用点收口为有界 allowlist（5 个合法读取者：rate-limit
keying ×2 / 操作访问日志 / accesscore + ssobff 两个 observer）。新读取者必须显式入列 → 评审 checkpoint，防未来
新事件静默把明文 IP 喂进新 sink。这是 sink 侧 sealed 类型的源侧补全，二者合成对 IP 的双侧 funnel。

### slog 统一

bootstrap 观察者的 slog `client_ip_hash` 与 wire payload 共用**同一个 keyed HMAC**（替换原先可逆的 32-bit
`HashIPForLog`，仅此调用点）。`HashIPForLog` 全仓删除，ssobff 的 slog 迁到 keyed `HashIP` → 全仓**单一** IP-hash
原语，无「弱 API foot-gun」。

### key 管理

salt 走现有 `cellmodules/cellsecrets` 机制，dev 默认值登记进 `wellKnownDemoKeys`（防公开 salt 误抄进真部署使哈希可逆）。
IP-hash salt 与 audit HMAC chain key **独立**（key separation）。两个 composition root 不对称、刻意如此：

- **accesscore（生产路径，`cmd/corebundle`）**：env `GOCELL_ACCESSCORE_IP_HASH_SALT` 经 `cellsecrets.BuildHMACKey`——real
  模式缺失 / demo-key fail-fast；composition root 另强制 **≥32 字节**（`redaction.MinIPHashSaltBytes`），短 salt 启动即拒
  （短 salt 静默使 IPv4 空间可暴力还原）。
- **ssobff（demo-only 示例）**：env `GOCELL_SSOBFF_IP_HASH_SALT` 为 env-or-default，**无 real-mode fail-fast**（ssobff
  无 real/demo adapter mode，永非生产）；default 仍登记进 `wellKnownDemoKeys`。

### slog `client_ip_hash` 值变更（运维感知）

bootstrap observer 的 slog `client_ip_hash` 值从 **8-hex**（旧 `HashIPForLog` 的 32-bit 截断 SHA）变为 **64-hex**
（keyed HMAC-SHA256）。值本身始终随 IP 变化、dashboard 不解析其内容，但任何**按长度/正则**硬匹配 `client_ip_hash`
的日志解析器 / grep 需更新。这是 slog 与 wire 哈希统一（单一 keyed 原语）的预期副作用。

## 为何 hash 而非加密 / 保留明文

| 方案 | 同 IP 关联 | 明文恢复 | 新基建 | 选用 |
|------|-----------|---------|--------|------|
| keyed hash（本 ADR） | ✅ 确定性 | ❌ 不可逆 | 无 | ✅ |
| 加密 + TTL/DLX 清理 | ✅ | ✅ 可解密 | 密钥管理 + 保留策略 | ❌ |
| 保留明文 + 仅文档化 | ✅ | ✅ | 无 | ❌ |

- 已**用数据证实**无任何特性读明文 IP：唯一消费者 `auditappendbootstrap` 只存储，auditquery 无 IP filter/display。
  旧「for compliance 保留明文」是设想、非 load-bearing → 推翻安全（见下）。
- keyed hash 保留「同 IP 关联分析」（rate-limit 滥用检测足够），去除 PII liability，无解密/TTL 复杂度。
- **明文不可恢复是刻意接受的代价**：若将来确需 IP 取证恢复，另开加密 ADR；不在本 ADR 范围内重新引入明文。
- 对标 HashiCorp Vault audit device 默认（HMAC-SHA256 敏感字段，除非 `log_raw`）。

## 推翻 C3「ledger 保留明文 for compliance」

原 `cellmodules/accesscore/module.go` 注释将 slog（hashed）与 ledger（plaintext）刻意分裂，理由是「compliance
需要明文 IP」。本 ADR 经激进自审 L3 逐条核对后推翻：

- **已证实无 load-bearing 消费方**：全仓无任何特性按明文 IP 查询/展示/取证（grep 证实）。该「compliance 需求」
  是假设，非实现。
- 关联性由 keyed hash 的确定性保留，足以支撑滥用检测。
- 同 PR 内重写该注释，不保留两套真理源（charter「ADR amendment 与原文矛盾段落同 PR 重写」）。

## AI-robust 评级（charter §Funnel 双向锁评级）

| 轴 | 评级 | 依据 |
|----|------|------|
| Sink 上游 | **Hard** | sealed `IPHash`（unexported 字段）+ 冻结 DTO 字段类型；明文 string 赋值编译不可表达 |
| Sink 下游 | **Hard** | `CLIENT-IP-HASH-FUNNEL-01` 经 go/types 冻结 IPHash 字段集 + 唯一构造器集 + DTO 字段类型；任何 drift CI 红 |
| Source 上游 | **Medium**（Go 永久天花板） | `ctxkeys.RealIPFrom` 须导出供跨包调用，Go 可见性无法表达「仅这 N 文件可调」；同 #1282/#851/#893 won't-do 族 |
| Source 下游 | **Hard** | `CTXKEYS-REALIP-READ-CALLER-01` 经 go/types caller-allowlist（alias/函数值不可绕） |

- 范围 = IP 一个字段（已证实是今日全部 11 个 replayable payload 中唯一 PII 字段）。
- 「按字段名扫描所有 replayable payload 拦 PII」= **Soft**（字符串锚点），宪章**拒绝立项**。
- 未来 PII 字段同范式接入 sealed 类型 + `HashX` 构造器 + 冻结 archtest；generic Hard 机制（codegen 从 schema
  `format` 派生 typed 字段）的演进追踪于 **gh #1605**。

## 契约扇出（5 载体）

| 载体 | 同步内容 |
|------|---------|
| schema | `payload.schema.json` clientIp → clientIpHash |
| 生成物 | `generated/.../bootstrap-failed/v1/types_gen.go`（`gocell generate contract` 重生成） |
| 实现（3 DTO 副本） | accesscore `dto.BootstrapAuthFailedEvent`（sealed IPHash）/ auditcore local（string）/ runtime-audit local（string） |
| 测试 | setup service/contract + auditappendbootstrap service/contract + runtime/audit append + cmd/corebundle 集成 + pkg/redaction TestHashIP + 2 archtest |
| docs | 本 ADR + `.claude/rules/gocell/observability.md` 导航 |

## 参考

- `pkg/redaction/redaction.go`（IPHash + HashIP 单源）
- archtest `CLIENT-IP-HASH-FUNNEL-01`（`tools/archtest/client_ip_hash_funnel_test.go`）
- archtest `CTXKEYS-REALIP-READ-CALLER-01`（`tools/archtest/ctxkeys_realip_read_caller_test.go`）
- ADR 202605211200 pre-v1.0 direct v1 evolution
- 对标：HashiCorp Vault audit device（HMAC log_raw=false 默认）
