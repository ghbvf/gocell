# 合并审查报告：Sonar + Manual Review + Fix Verification

> 合并 SonarCloud 静态扫描（388 issues + 43 hotspots）、3 Phase 手工 review（60+ findings）、
> Tier 0 修复验证（+2450 行新代码审查）的统一报告。

- **生成时间**: 2026-04-06
- **Sonar 扫描时间**: 2026-04-05T23:13:07Z
- **Quality Gate**: ERROR（3 条失败）
- **分支**: feat/003-phase4-examples-docs (HEAD: b1779fb)

---

## 1. Quality Gate 失败原因

| 指标 | 实际值 | 阈值 | 状态 |
|------|--------|------|------|
| new_security_rating | 4 (D) | 1 (A) | ERROR |
| new_duplicated_lines_density | 4.3% | 3% | ERROR |
| new_security_hotspots_reviewed | 0% | 100% | ERROR |

---

## 2. Sonar 总览

| 类别 | 数量 | 说明 |
|------|------|------|
| Vulnerabilities | 8 | 1 BLOCKER, 2 CRITICAL, 5 MAJOR |
| Code Smells | 380 | 314 CRITICAL, 51 MAJOR, 10 MINOR, 5 INFO |
| Bugs | 0 | — |
| Security Hotspots | 43 | 全部 LOW（测试文件硬编码 IP） |

---

## 3. 真正需要关注的 Sonar 发现（去噪后）

### 3.1 Vulnerabilities（8 个，需逐条处理）

| # | Sonar 严重度 | 文件 | 问题 | 与已有 Finding 关系 | 行动 |
|---|-------------|------|------|-------------------|------|
| V-1 | **BLOCKER** | `access-core/slices/sessionlogin/handler_test.go:26` | 硬编码 secret，可能泄露 | **新发现** — 测试文件中的 HMAC key | 替换为 `auth.MustGenerateTestKeyPair()` |
| V-2 | CRITICAL | `runtime/auth/jwt_test.go:81` | JWT 签名使用弱算法 | **关联 P0-2** — RS256 迁移不完整 | 测试用例本身在测试 HS256 路径，标记为 `// DEPRECATED test` |
| V-3 | CRITICAL | `adapters/oidc/verifier.go:70` | JWT 验证使用弱算法 | **新发现** — OIDC verifier 接受非 RS256 | 审查 verifier 是否应限制算法 |
| V-4 | MAJOR | `examples/iot-device/docker-compose.yml:7` | 硬编码 PASSWORD | **关联 F-13** | 示例文件，加注释说明仅用于开发 |
| V-5 | MAJOR | `examples/sso-bff/docker-compose.yml:7` | 同上 | **关联 F-13** | 同上 |
| V-6 | MAJOR | `examples/todo-order/docker-compose.yml:7` | 同上 | **关联 F-13** | 同上 |
| V-7 | MAJOR | `docker-compose.yml:6` | 根目录 compose 硬编码 PASSWORD | **关联 F-13** | 同上 |
| V-8 | MAJOR | `adapters/postgres/pool_test.go:127` | 硬编码 URL 含 credential | **新发现** | 使用 env var fallback |

### 3.2 认知复杂度超标（CLAUDE.md 规则: CC ≤ 15）

Sonar 发现 **12 个方法**超过 CC 15 上限。**这是手工 review 完全遗漏的维度**：

| # | 文件:方法 | CC 实际值 | 超标倍数 | 模块层 | 优先级 |
|---|----------|----------|---------|--------|--------|
| CC-1 | `runtime/bootstrap/bootstrap.go:158` Start() | **57** | 3.8x | runtime | **P0** — 必须拆分 |
| CC-2 | `kernel/governance/depcheck.go:74` CheckDependencies() | **36** | 2.4x | kernel | P1 |
| CC-3 | `cells/access-core/cell.go:147` Init() | **28** | 1.9x | cells | P1 |
| CC-4 | `runtime/auth/middleware.go:71` AuthMiddleware() | **28** | 1.9x | runtime | P1 |
| CC-5 | `cells/access-core/slices/sessionlogin/service.go:100` Login() | **26** | 1.7x | cells | P1 |
| CC-6 | `kernel/assembly/generator.go:115` Generate() | **25** | 1.7x | kernel | P1 |
| CC-7 | `runtime/auth/servicetoken.go:28` Validate() | **22** | 1.5x | runtime | P1 |
| CC-8 | `adapters/postgres/outbox_relay.go:133` relay() | **21** | 1.4x | adapters | P2 |
| CC-9 | `kernel/governance/rules_verify.go:15` checkVerify() | **20** | 1.3x | kernel | P2 |
| CC-10 | `kernel/governance/rules_verify.go:61` checkWaivers() | **21** | 1.4x | kernel | P2 |
| CC-11 | `runtime/auth/jwt.go:92` ParseToken() | **19** | 1.3x | runtime | P2 |
| CC-12 | `adapters/rabbitmq/consumer_base.go:102` consume() | **19** | 1.3x | adapters | P2 |

### 3.3 Security Hotspots（43 个 → 可批量 dismiss）

| 类别 | 数量 | 文件 | 行动 |
|------|------|------|------|
| 硬编码 IP（测试） | 38 | `real_ip_test.go`, `rate_limit_test.go` | **Safe** — 测试夹具，标记为 Won't Fix |
| PATH 变量 | 2 | `kernel/slice/verify.go:195`, `verify_test.go:508` | 审查后标记 Safe |
| 硬编码 IP（测试） | 3 | `rate_limit_test.go` | Safe |

### 3.4 Code Smells 分类汇总

| 类别 | 数量 | 说明 | 行动 |
|------|------|------|------|
| 字符串字面量重复 | ~280 | 主要在 `_test.go` 文件中 table-driven test 数据 | **P2/WONTFIX** — Go table-driven test 惯例，常量化反而降低可读性 |
| 空函数体缺注释 | ~60 | `func() {}` 在 test table 中作为 noop handler | **WONTFIX** — Go test 习惯用法 |
| 认知复杂度 | 12 | 见 3.2 节 | P0-P2 分级处理 |
| 其他 | ~28 | 零散 code smell | 逐条评估 |

---

## 4. Tier 0 修复验证结果（Step 2）

### 4.1 原始 15 个 Finding 闭环状态

| Finding | 严重度 | 修复状态 | 验证详情 |
|---------|--------|---------|---------|
| F-01 | **P0** | **FIXED** | 本地 `ErrSessionNotFound` 常量已删除，全部改用 `errcode.ErrSessionNotFound` |
| F-02 | P1 | **FIXED** | 新增 `ErrOrderNotFound`, `ErrDeviceNotFound`, `ErrCommandNotFound` 常量 |
| F-03 | P1 | **FIXED** | sso-bff README curl 路由已修正 `/api/v1/flags` |
| F-04 | P2 | **FIXED** | README 步骤编号 1-12 已修正 |
| F-05 | P1 | **FIXED** | CI 中移除空 validate 循环，改为注释说明 |
| F-06 | P2 | NOT FIXED | List 端点仍缺 `page` 字段（pre-existing） |
| F-07 | P2 | NOT FIXED | POST 201 响应格式不一致（pre-existing） |
| F-08 | P2 | NOT FIXED | `TestSentinelCodes` 仍只覆盖 10/24 个常量 |
| F-09 | P2 | NOT FIXED | iot-device docker-compose 仍缺 rabbitmq |
| F-10 | P1 | **PARTIAL** | 2/5 错误路径有 errcode 断言，3 个仍缺 |
| F-11 | P2 | NOT FIXED | order-cell L2 无 outboxWriter 强制 |
| F-12 | P2 | **FIXED** | docker-compose 已删除 `version` 字段 |
| F-13 | P2 | NOT FIXED | sso-bff 硬编码 HMAC key（Sonar 也发现了 V-4~V-7） |
| F-14 | P2 | NOT FIXED | 内存 repo 无并发测试 |
| F-15 | P1 | **FIXED** | CI 静态 services 已移除，改用 testcontainers |

### 4.2 修复代码引入的新问题

| ID | 严重度 | 文件 | 问题 |
|----|--------|------|------|
| NEW-01 | P1 | `device-cell/internal/mem/repository_test.go:55` | Create duplicate 测试缺 errcode 类型断言（order-cell 已有） |
| NEW-02 | P1 | `pkg/errcode/errcode_test.go:190-209` | `TestSentinelCodes` 未更新，24 个常量只测 10 个（=F-08 升级） |

---

## 5. 交叉去重：Sonar vs 手工 Review

| Sonar 发现 | 手工 Finding | 状态 | 说明 |
|-----------|-------------|------|------|
| V-1 handler_test.go BLOCKER | — | **Sonar 独有** | 测试文件硬编码 secret |
| V-2 jwt_test.go weak cipher | P0-2 RS256 fallback | **重叠** | 手工已发现更深层问题 |
| V-3 oidc/verifier.go weak cipher | — | **Sonar 独有** | OIDC verifier 算法限制 |
| V-4~V-7 docker PASSWORD | F-13 | **重叠** | 扩展到所有 compose 文件 |
| V-8 pool_test.go credential | — | **Sonar 独有** | 测试文件硬编码 DSN |
| CC-1~CC-12 复杂度 | — | **Sonar 独有（重要！）** | 手工 review 完全遗漏此维度 |
| 280+ string duplicates | — | **Sonar 独有但可忽略** | test table 惯例 |
| 60+ empty functions | — | **Sonar 独有但可忽略** | test noop 惯例 |
| 43 hotspots (IP) | — | **Sonar 独有但安全** | 测试夹具 |

---

## 6. 统一 Action Plan（优先级排序）

### P0 — 阻塞合并（3 项）

| # | 来源 | 问题 | 涉及文件 | 预计工作量 |
|---|------|------|---------|----------|
| 1 | Sonar CC-1 | `bootstrap.Start()` CC=57（3.8x 超标） | `runtime/bootstrap/bootstrap.go` | 中 — 拆分为子函数 |
| 2 | Manual P0-2 | access-core RS256 fallback 生成临时密钥 | `cells/access-core/cell.go:159-184` | 小 — fail-fast |
| 3 | Manual P0-3 | WithEventBus 缺 Deprecated 注释 | `runtime/bootstrap/bootstrap.go:86` | 极小 |

### P1 — 强烈建议修复（15 项）

| # | 来源 | 问题 | 涉及文件 |
|---|------|------|---------|
| 4 | Sonar V-1 | handler_test.go 硬编码 secret (BLOCKER) | `sessionlogin/handler_test.go:26` |
| 5 | Sonar V-3 | OIDC verifier 接受弱算法 | `adapters/oidc/verifier.go:70` |
| 6 | Sonar CC-2 | `CheckDependencies()` CC=36 | `kernel/governance/depcheck.go:74` |
| 7 | Sonar CC-3 | `Init()` CC=28 | `cells/access-core/cell.go:147` |
| 8 | Sonar CC-4 | `AuthMiddleware()` CC=28 | `runtime/auth/middleware.go:71` |
| 9 | Sonar CC-5 | `Login()` CC=26 | `sessionlogin/service.go:100` |
| 10 | Sonar CC-6 | `Generate()` CC=25 | `kernel/assembly/generator.go:115` |
| 11 | Sonar CC-7 | `Validate()` CC=22 | `runtime/auth/servicetoken.go:28` |
| 12 | NEW-01 | device-cell 测试缺 errcode 断言 | `repository_test.go:55` |
| 13 | NEW-02 | TestSentinelCodes 只覆盖 10/24 常量 | `errcode_test.go:190-209` |
| 14 | F-10 | device-cell 3 个错误路径缺 errcode 断言 | `device-cell/*_test.go` |
| 15 | Manual P4-TD-04 | order-cell L2 无 outboxWriter 强制 | `order-cell/cell.go` |
| 16 | Manual P4-TD-05 | 缺 outbox full-chain 集成测试 | `tests/integration/` |
| 17 | Manual P4-TD-06 | CI 验证 no-op | (**已修复 F-05**) |
| 18 | Sonar V-8 | pool_test.go 硬编码 credential | `adapters/postgres/pool_test.go:127` |

### P2 — 可后续处理（20+ 项）

| 类别 | 数量 | 说明 |
|------|------|------|
| Sonar CC P2 级 | 5 | CC 17-21 的方法，轻微超标 |
| Sonar docker PASSWORD | 4 | 示例文件，加注释即可 |
| F-06/F-07 响应格式 | 2 | pre-existing，计划 v1.1 |
| F-08 TestSentinelCodes | 1 | 已升级为 NEW-02 (P1) |
| F-09 docker-compose rabbitmq | 1 | iot-device 缺服务 |
| F-11 L2 无强制 | 1 | 同 P4-TD-04 |
| F-13 硬编码 HMAC | 1 | 示例文件 |
| F-14 并发测试 | 1 | 内存 repo |
| Sonar hotspots | 43 | 全部测试文件 IP，Safe |
| Sonar string duplicates | 280 | test table 惯例，WONTFIX |
| Sonar empty functions | 60 | test noop 惯例，WONTFIX |

---

## 7. Sonar 噪音清理建议

388 issues 中真正需要处理的约 **30 项**（8 vulnerabilities + 12 CC 超标 + ~10 有意义的 code smell）。
其余 ~358 项为 Go 测试惯例产生的噪音，建议：

1. **配置 Sonar quality profile** — 对 `*_test.go` 文件禁用 `go:S1192`（字符串重复）和 `go:S1186`（空函数体）
2. **设置排除路径** — `**/examples/**` 排除示例文件的 docker-compose 密码告警
3. **添加 `sonar-project.properties`** — 当前缺失，导致扫描范围和规则未优化

---

## 8. 按 PR 分布

| PR | Issues | Hotspots | Vulns | QG | 关键问题 |
|----|--------|----------|-------|-----|---------|
| #33 (tier0-fix) | 21 | 0 | 0 | ERROR (dup 5.7%) | 新测试字符串重复 |
| #32 (phase4) | 38 | 0 | 3 | ERROR (sec 3, dup 4.4%) | docker PASSWORD, 新 cell 测试重复 |
| #31 (phase3) | 85 | 22 | 5 | ERROR (sec 4, hotspot 0%) | JWT 弱算法, CC 超标, 硬编码 |
| #30 (W4-all) | 28 | 0 | 0 | OK | 测试重复 |
| #28 (W4-tests) | 57 | 0 | 0 | OK | 测试重复 |
| #17 (kernel) | 3 | 0 | 0 | ERROR (dup 4%) | 轻微重复 |
| #16 (security) | 10 | 20 | 1 | ERROR (sec 4, hotspot 0%) | RS256 + IP hotspots |
| #15 (cells) | 3 | 0 | 1 | ERROR (sec 4, dup 4.9%) | sessionlogin 弱算法 |

---

## 9. 总结

| 指标 | 数值 |
|------|------|
| Sonar 原始 issues | 388 |
| Sonar 去噪后有效 issues | ~30 |
| 手工 review 累计 findings | 60+ |
| Tier 0 修复验证：已修复 | 7/15 |
| Tier 0 修复验证：新引入问题 | 2 |
| **Sonar 独有新发现（有价值）** | **14**（1 BLOCKER vuln + 1 CRITICAL vuln + 12 CC 超标） |
| 合并后去重统一 P0 | **3** |
| 合并后去重统一 P1 | **15** |
| 合并后去重统一 P2 | **20+** |
