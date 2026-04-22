# Decisions — Phase 4: Examples + Documentation

## 裁决日期
2026-04-06

## 审查来源
- 架构师: review-architect.md (10 条建议)
- Roadmap 规划师: review-roadmap.md (8 条建议)
- Kernel Guardian: kernel-constraints.md (10 条建议)
- 产品经理: review-product-manager.md (10 条建议)

---

## 重要决策

### 决策 1: IoT-device 示例保留 L4 声明，添加 disclaimer（不新增 kernel 原语）

- **决策**: 保留 iot-device 示例的 `consistencyLevel: L4` 声明，但 README 中明确说明 L4 命令队列模式在 v1.0 为应用层实现，框架计划在 v1.1 提供 `kernel/command` 一等支持。
- **理由**: Phase 4 是文档/示例 Phase，不应引入新的 kernel 包。L4 示例的价值在于展示一致性等级概念和目录结构规范，评估者可理解 L4 场景即使无 kernel 原语也可实现。
- **被否决的替代方案**:
  - Option A（新增 kernel/command 包）：超出 Phase 4 范围，引入 kernel 代码变更风险
  - Option B（降级为 L3）：丧失 L4 演示价值，与 product-context 承诺不符

### 决策 2: RS256 迁移采用 Cell-level Option 注入，保留 WithSigningKey deprecated

- **决策**: 新增 `WithRSAKeyPair(priv, pub)` 和 `WithJWTIssuer/WithJWTVerifier` Option。保留 `WithSigningKey([]byte)` 标记 `// Deprecated`。三个 slice 构造函数从 `signingKey []byte` 改为 `auth.JWTIssuer`/`auth.JWTVerifier` 接口。提供 `auth.MustGenerateTestKeyPair()` helper。
- **理由**: 采纳 A-01 + KG-01 建议。kernel/ 零修改，变更限于 cells/ + runtime/auth。
- **被否决的替代方案**: 直接删除 WithSigningKey（过于激进，无迁移路径）

### 决策 3: outboxWriter fail-fast 在 Cell.Init 阶段执行（非 Assembly.Start）

- **决策**: 在各 Cell 的 Init 方法中（BaseCell.Init 之后）检查 L2+ 是否注入 outboxWriter。
- **理由**: 采纳 KG-02 建议。拒绝 A-03 建议（Assembly.Start 集中验证）。理由：Cell 知道自己的一致性等级和依赖，Init 是 Cell 自身初始化逻辑的自然位置。Assembly 不应知道 Cell 的内部依赖细节。
- **被否决的替代方案**: Assembly.Start 集中 DependencyValidator 模式（耦合 Assembly 与 Cell 内部依赖，违反信息隐藏）

### 决策 4: 示例使用根 go.mod，testcontainers 用 build tag 隔离

- **决策**: 保持单 module 结构。testcontainers 全部使用 `//go:build integration` 标签。
- **理由**: 采纳 A-04 + KG-04。独立 module 增加开发者认知成本，违反"示例应简单"原则。build tag 已足够隔离。

### 决策 5: v1.0 Scope Cut 正式声明

- **决策**: 在 spec FR-9 中新增 FR-9.4，更新 master-plan v1.0/v1.1 边界。7 个 kernel 子模块（webhook/reconcile/replay/rollback/consumed/trace/wrapper）+ 4 个 runtime 子模块（scheduler/retry/tls/keymanager）+ VictoriaMetrics adapter 正式记录为 v1.1 延迟。
- **理由**: 采纳 R-01 + R-02。隐性 scope cut 不可接受，必须正式记录。
- **被否决的替代方案**: 在 Phase 4 实现缺失模块（工作量超出 14 天容量）

### 决策 6: S3 env prefix 修复添加 fallback 兼容层

- **决策**: `ConfigFromEnv()` 先读 `GOCELL_S3_*`，fallback 读旧 `S3_*` + `slog.Warn` deprecation 警告。下个版本删除 fallback。
- **理由**: 采纳 PM-07。虽然当前无生产部署，但向后兼容是好习惯。
- **被否决的替代方案**: 直接修改无 fallback（可能影响已有 docker-compose 配置）

### 决策 7: 快速开始路径改为 git clone（非 go get）

- **决策**: FR-4.4 标题改为"快速开始 — git clone + 运行示例"。保留 go get 场景为独立子节（已有项目集成）。
- **理由**: 采纳 PM-03。私有仓库 go get 会失败，git clone 是更诚实的首次体验路径。

### 决策 8: Grafana dashboard 模板使用 Prometheus 兼容接口

- **决策**: FR-5.6 dashboard 模板基于 Prometheus query 语法，数据源标注为 "Prometheus-compatible"（兼容 InMemoryCollector / 未来 VictoriaMetrics）。
- **理由**: 采纳 PM-08。v1.0 无 VictoriaMetrics adapter，dashboard 应基于现有能力。

---

## Kernel Guardian 约束裁决

| 约束项 | 裁决 | 理由 |
|--------|------|------|
| KG-01: RS256 via Cell Option, kernel 零修改 | accept | 决策 2 |
| KG-02: outboxWriter fail-fast in Cell.Init | accept | 决策 3 |
| KG-03: examples/ internal import grep check in CI | accept | 低成本高收益 |
| KG-04: testcontainers //go:build integration | accept | 决策 4 |
| KG-05: sso-bff 真实 postgres outboxWriter | accept | 示例教育价值核心 |
| KG-06: todo-order metadata pass gocell validate | accept | golden path 必须合规 |
| KG-07: iot-device L4 不注入 outboxWriter | accept | L4 ≠ L2+延迟 |
| KG-08: CI validate 单独运行覆盖 examples/ | accept | 方案(2) 不修改 kernel |
| KG-09: S3 env fix 同步 .env.example | accept | 决策 6 |
| KG-10: kernel 覆盖率 CI 自动化门控 | accept | 硬性 Gate 需自动化 |

Kernel Guardian 30 条约束清单（C-01 ~ C-30）: 全部 accept，作为 S5 实施和 S7 验证的检查基线。

---

## 延迟到后续 Phase 的项目

| 项目 | 来源 | 延迟理由 | 计划时机 |
|------|------|---------|---------|
| L4 kernel/command 一等原语 | A-02 | Phase 4 不新增 kernel 包 | v1.1 |
| Assembly.Start DependencyValidator 接口 | A-03 | Cell.Init 方案已足够 | v1.1（如有需求） |
| gocell validate 扫描 examples/ 路径 | KG-08 备选方案(1) | 避免 kernel 修改 | v1.1 |
| 30 分钟 Gate CI 自动化代理指标 | R-06 | 手动验证在 Phase 4 可接受 | v1.1 |
| v1.0.0 tag 策略 / semver 声明 | R-08 | Phase 4 完成后发布决策 | Phase 4 之后 |
| Optional adapter 接口桩 | R-03 | 空接口无评估价值 | v1.1 按需 |
| WinMDM GoCell 集成 POC | R-04 | 外部项目关注点 | 独立计划 |
| in-memory 降级模式形式化 | PM-05 | 范围过大，需要全 Cell 改造 | v1.1 |
| #10 TOCTOU 竞态 | phase-charter | 高风险重构 | post-v1.0 |
| #11 Domain model refactoring | phase-charter | 高风险重构 | post-v1.0 |
| #12 Rollback version 校验 | phase-charter | 需持久化版本管理 | post-v1.0 |

---

## 被拒绝的建议

| 建议 | 来源 | 拒绝理由 |
|------|------|---------|
| A-03: outboxWriter fail-fast at Assembly.Start | review-architect.md | 违反信息隐藏。Assembly 不应知道 Cell 内部依赖细节。Cell.Init 是更自然的校验位置（KG-02 同意） |
| A-02 Option A: 新增 kernel/command 包 | review-architect.md | Phase 4 不新增 kernel 包。采纳 Option C（disclaimer） |
| PM-05: in-memory 降级模式形式化为 FR 子项 | review-product-manager.md | 范围过大，需改造所有 Cell 的 Init 逻辑。Phase 4 示例默认走 Docker 路径，降级为"建议"而非硬性要求 |
| PM-09: type: edge 需 kernel 扩展 | review-product-manager.md | kernel/cell/types.go 已定义 CellTypeEdge = "edge"（line 17），无需扩展。iot-device 使用现有类型即可 |
