# Product Review Report — Phase 2: Runtime + Built-in Cells

> 评审日期: 2026-04-05
> 评审角色: 产品经理 (Stage 8.2, Path D)
> 输入文件: product-context.md, product-acceptance-criteria.md, qa-report.md, tech-debt.md, user-signoff.md

---

## 7 维度评分表

| # | 维度 | 评分 | 依据 |
|---|------|------|------|
| A | 验收标准覆盖率 | **黄** | P1 (64 条): 62 PASS, 2 未达标 (AC-13.1 runtime/ 两包覆盖率不足; AC-8.2 签名为 HS256 非 RS256)。P2 (12 条): 0 FAIL, 全部 PASS 或 SKIP 附理由。P3 (4 条): 允许 SKIP, 核心 3 包 doc.go 已有, 其余记入 tech-debt #22 |
| B | UI 合规检查 | **N/A** | Phase 2 无 UI 组件, role-roster.md 前端=OFF |
| C | 错误路径覆盖率 | **黄** | spec 边界场景 (panic recovery, rate limit, auth fail, body limit, account lockout) 均有单元测试 PASS; 跨 Cell 事件传播错误路径 (J-audit-login-trail 端到端) 未覆盖, 依赖 Phase 3 adapter; handler 层 10/16 slices 覆盖不足 (tech-debt #13) |
| D | 文档链路完整性 | **黄** | 核心 3 包 (middleware/config/bootstrap) 有 doc.go; Cell 开发指南已写但缺 contract test 和错误处理模式说明; 11 个 runtime 包缺 doc.go (tech-debt #22); README.md 需确认是否反映 Phase 2 新增能力 |
| E | 功能完整度 | **绿** | spec 中定义的 13 个 FR 全部有对应实现: runtime/ 13 模块 (http middleware x7 + health + router + config + bootstrap + shutdown + worker + auth + eventbus + observability x3); cells/ 3 个 Cell 16 个 Slice; kernel/ 接口扩展 (Subscriber + HTTPRegistrar + EventRegistrar + RouteMux); core-bundle 启动入口 |
| F | 成功标准达成度 | **黄** | S1 PASS (3 Cell 生命周期测试通过); S2 部分 (Hard Gate 5 条 PASS, Soft Gate 3 条中 J-audit-login-trail 端到端未覆盖, J-config-hot-reload watcher 未集成 bootstrap); S3 黄 (10/12 达标, bootstrap 51.4%, router 78.8%); S4 PASS (Cell 级聚合 85-87%); S5 PASS (kernel/ 全部 >= 90%); S6 PASS (scaffold 可用); S7 PASS (依赖规则零违反); S8 PASS (6 个白名单依赖) |
| G | 产品 Tech Debt | **绿** | [PRODUCT] 标签仅 3 条: PM-03 Retry-After 硬编码 / 审计查询 time.Parse 错误静默忽略 / Update user 仅支持 email。均为非阻塞体验降级, 已记入 tech-debt, Phase 3 修复 |

---

## 评分说明

### A. 验收标准覆盖率 -- 黄

**P1 验收标准分析 (64 条)**:

已通过 (62 条): FR-1 至 FR-13 核心功能 AC、kernel 接口扩展 AC-K.1/K.2、EventBus AC-EB.1/EB.3/EB.4、Journey Hard Gate AC-J.1 至 AC-J.5、YAML 元数据 AC-W0.1/W0.2、core-bundle AC-CB.1/CB.2/CB.3、NFR AC-NFR.1/NFR.2/NFR.3/NFR.4 均 PASS。

未完全达标 (2 条):
1. **AC-13.1 (runtime/ 覆盖率)**: bootstrap.go 51.4%、router.go 78.8%，2/12 包未达 80% 阈值。bootstrap 因 sandbox 限制 net.Listen 无法测试; router 差 1.2% 未达标。Cell 级聚合和 service 层均达标。
2. **AC-8.2 (session-login 签名)**: AC 原文要求 "RS256 签名"，实际实现为 HS256 (决策 8/tech-debt #6 明确延迟 RS256 至 Phase 3)。功能正确但与 AC 字面要求不一致。

**P2 验收标准 (12 条)**: 全部 PASS 或 SKIP 附理由，无 FAIL。

**P3 验收标准 (4 条)**: AC-11.1 核心 3 包 doc.go 已有 (PASS)，其余 11 包延迟; AC-11.2 Cell 开发指南已写 (CONDITIONAL); AC-11.3/AC-12.3 已实现。

### C. 错误路径覆盖率 -- 黄

已覆盖的错误路径:
- panic recovery (AC-1.3): 中间件捕获 panic 返回 500
- rate limit (AC-1.7/1.8): 超限返回 429
- auth fail (AC-7.1): token 无效返回 401
- authorization deny (AC-7.3): 权限不足返回 403
- body limit (AC-1.6): 超限返回 413
- account lockout (AC-8.8): 连续失败自动锁定
- HMAC key missing (AC-9.6): Init 阶段明确报错阻止启动
- EventBus retry + dead letter (AC-EB.3): 3x 重试 + 死信通道

未覆盖:
- 跨 Cell 事件传播失败路径 (需 Phase 3 adapter 集成后端到端测试)
- handler 层 HTTP 解析错误路径 (10/16 slices handler 无 httptest)
- config watcher 文件监听异常路径 (watcher 未集成 bootstrap)

### D. 文档链路完整性 -- 黄

已有:
- runtime/http/middleware/doc.go, runtime/config/doc.go, runtime/bootstrap/doc.go
- docs/guides/cell-development-guide.md (覆盖 Cell 创建全流程)
- 导出类型有注释

缺失:
- 11 个 runtime 包无 doc.go (tech-debt #22)
- Cell 开发指南缺 contract test 编写指引和错误处理模式说明 (user-signoff D6 扣分原因)

### F. 成功标准达成度 -- 黄

| # | 标准 | 状态 | 说明 |
|---|------|------|------|
| S1 | 3 Cell 可启动运行 | PASS | cell_test.go 生命周期测试全部通过 |
| S2 | Journey 验证通过 | 部分 | Hard Gate 5/5 PASS; Soft Gate: J-audit-login-trail 端到端未覆盖, J-config-hot-reload watcher 未集成 |
| S3 | runtime/ 覆盖率 >= 80% | 部分 | 10/12 PASS, bootstrap 51.4% + router 78.8% 未达标 |
| S4 | cells/ 覆盖率 >= 80% | PASS | Cell 级聚合 85-87%, service 层 table-driven 测试 |
| S5 | kernel/ 覆盖率 >= 90% | PASS | 全部 >= 90%, 最低 93.2% |
| S6 | 10 分钟创建首个 Cell | PASS | scaffold 可用, Cell 开发指南已写 |
| S7 | 依赖规则零违反 | PASS | go build + gocell validate TOPO 通过 |
| S8 | 外部依赖可控 | PASS | 6 个白名单依赖, 无额外引入 |

---

## "必须修复" 项

> 以下为交付前必须解决的阻塞项，不超过 3 条。

### 1. AC-8.2 签名算法与 AC 字面要求不一致 -- 文档对齐

**问题**: product-acceptance-criteria.md AC-8.2 写明 "JWT access token + refresh token（RS256 签名）"，实际实现为 HS256。决策 8 和 tech-debt #6 已明确延迟 RS256，但 AC 文档未同步修订，造成验收时字面 FAIL。

**要求动作**: 修订 AC-8.2 描述，将 "RS256 签名" 改为 "HS256 签名（Phase 2），RS256 迁移延迟至 Phase 3 (tech-debt #6)"，使 AC 与决策记录一致。不需要改代码。

### 2. router.go 覆盖率 78.8% -- 补 1-2 个测试用例达标

**问题**: AC-13.1 要求 runtime/ 每个包覆盖率 >= 80%，router.go 差 1.2%。Route/Mount/Group 委托方法缺少独立测试。bootstrap.go 的 51.4% 因 sandbox 限制可豁免（已记入 tech-debt #15），但 router.go 无环境限制理由。

**要求动作**: 为 router.go 的 Route/Mount/Group 方法补充 1-2 个单元测试，将覆盖率推至 >= 80%。

### 3. 审计查询 time.Parse 错误静默忽略 -- 返回 400

**问题**: tech-debt #25 [PRODUCT] 标签。审计查询接收到非法时间格式时静默忽略而非返回 400 错误，违反错误处理规范 (error-handling.md 第 4 条 "handler 层统一转换领域错误为 HTTP 状态码")。这是一个面向 API 消费者的正确性问题。

**要求动作**: audit-query handler 在 time.Parse 失败时返回 `400 + {"error": {"code": "ERR_VALIDATION_INVALID_TIME_FORMAT", ...}}`。

---

## 总体评价

**判定: CONDITIONAL APPROVE**

Phase 2 核心产品目标已达成 -- GoCell 从 "可编译的元数据治理框架" 成功升级为 "可运行的 Cell-native Go 框架"。3 个内建 Cell (access-core / audit-core / config-core) 功能完整、生命周期正常、kernel 接口扩展正确、runtime 层 13 个模块就绪、安全关键修复 (bcrypt + DTO) 已落地。

7 维度中 1 绿 4 黄 1 N/A 1 绿，无红灯。黄灯原因集中在:
- 2 个 runtime 包覆盖率略低 (可快速修复)
- AC 文档与决策记录的签名算法描述不一致 (文档对齐即可)
- 文档完整性差距 (11 包缺 doc.go，已纳入 Phase 3 tech-debt)
- Soft Gate Journey 需 Phase 3 adapter 完整验证

上述 3 条 "必须修复" 项均为小范围改动（预计 < 1 小时），修复后可转为 APPROVE。

[PRODUCT] tech-debt 仅 3 条，均为非阻塞体验降级，风险可控。23 条 [TECH] debt 中安全类 (SEC-03 ~ SEC-11) 在 Phase 2 无生产部署前提下风险可接受，但须确保 Phase 3 优先处理 SEC-03 (密钥硬编码) 和 SEC-04 (HS256 迁移 RS256)。

User signoff 三视角均 >= 3 (CONDITIONAL)，与产品评审结论一致。
