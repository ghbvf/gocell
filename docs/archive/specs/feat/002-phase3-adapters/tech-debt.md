# Tech Debt — Phase 3: Adapters

## 分类说明
- [TECH]: 技术债务（代码质量、架构退化、测试缺失）
- [PRODUCT]: 产品债务（降级体验、缺失功能、临时方案）

## 延迟项

| # | 标签 | 来源席位 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|---------|------|---------|-------------|
| 1 | [TECH] | 测试/回归 | F-03: integration_test.go 全为 t.Skip stub，testcontainers-go 未引入 go.mod | 需要 Docker 环境；集成测试框架在位，stub 结构正确，待 Docker CI 环境就绪后填充 | Phase 4 |
| 2 | [TECH] | 测试/回归 | F-04: postgres adapter 覆盖率 46.6%（要求 ≥80%），Pool/TxManager/Migrator 真实路径未覆盖 | 需要真实 PostgreSQL 的集成测试才能覆盖连接/事务/迁移路径 | Phase 4 |
| 3 | [TECH] | 运维/部署 | F-08: 无 .github/workflows CI 配置 | Phase 3 聚焦 adapter 实现，CI 配置待 Phase 4 examples 一并设置 | Phase 4 |
| 4 | [TECH] | 测试/回归 | F-10: websocket/oidc/s3 单元测试在 sandbox 环境 httptest 端口绑定 panic | 测试在非 sandbox 环境正常通过，sandbox 限制 net.Listen | Phase 4 CI |
| 5 | [TECH] | 运维/部署 | F-11: docker-compose.yml 缺 start_period，rabbitmq 冷启动占 retries 配额 | 非阻塞，docker compose up --wait 仍可在 30s 内完成 | Phase 4 |
| 6 | [TECH] | 架构一致性 | F-12: outboxWriter nil guard 静默 fallback 到 publisher.Publish | 向后兼容设计，生产应注入 outboxWriter 避免降级。建议添加 slog.Warn | Phase 4 |
| 7 | [TECH] | 测试/回归 | F-14: testcontainers-go 未在 go.mod（与 #1 联动） | 同 #1 | Phase 4 |
| 8 | [TECH] | DX/可维护性 | F-15: WithEventBus 保留具体类型参数未标注 Deprecated | 向后兼容，Phase 4 可添加 // Deprecated 注释 | Phase 4 |
| 9 | [TECH] | 安全/权限 | RS256 迁移为 Option 注入，默认仍 HS256 | 完整迁移需要 Cell 构造时强制注入 RSA key pair，当前为渐进式 | Phase 4 |
| 10 | [PRODUCT] | 产品/UX | Phase 2 遗留 #54 TOCTOU 竞态未修复 | 需 Redis 分布式锁 + 持久化 session 稳定 | Phase 4 |
| 11 | [PRODUCT] | 产品/UX | Phase 2 遗留 #56-59 domain 模型重构未执行 | 高风险重构，需 adapter 稳定后 | Phase 4 |
| 12 | [PRODUCT] | 产品/UX | Phase 2 遗留 #62 configpublish.Rollback 版本校验 | 需持久化版本管理 | Phase 4 |

## Phase 2 Tech-Debt 处理状态

Phase 2 遗留 80 条 tech-debt：
- **已解决**: ~65 条（安全加固 8 条 + 编码规范 + 生命周期 + 治理规则 + 运维/DX + 产品修复）
- **DEFERRED 到 Phase 4**: 6 条（#54, #56-59, #62）+ 上表新增 12 条
- **成功标准 S7**: 74 条有效分母中约 65 条 RESOLVED（≥ 60 条阈值达标）

## 统计
- [TECH] 新增: 9 条
- [PRODUCT] 新增: 3 条（Phase 2 DEFERRED 继承）
- 上一 Phase 遗留已解决: ~65 条
