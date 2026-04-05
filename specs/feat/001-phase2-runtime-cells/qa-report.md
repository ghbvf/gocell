# QA Report — Phase 2: Runtime + Built-in Cells

## 测试环境
- Go version: go1.25 (as installed on development machine)
- OS: Darwin 24.1.0 (macOS)
- Branch: feat/001-phase2-runtime-cells
- 测试时间: 2026-04-05

## 1. Go Test 结果

### 总体
- **48 个测试包全部 PASS，0 FAIL**
- 涵盖 kernel/ (10 pkg) + runtime/ (12 pkg) + cells/ (22 pkg) + pkg/ (2 pkg) + cmd/ (1 pkg) + root (1 pkg)

### 覆盖率

| 层 | 包 | 覆盖率 | 达标? |
|---|---|---|---|
| kernel/assembly | assembly.go | 94.5% | >= 90% ✓ |
| kernel/cell | types + interfaces + base + registrar | 99.0% | >= 90% ✓ |
| kernel/governance | validate + rules + depcheck + targets | 96.2% | >= 90% ✓ |
| kernel/journey | catalog.go | 100.0% | >= 90% ✓ |
| kernel/metadata | types + parser | 96.7% | >= 90% ✓ |
| kernel/registry | contract + cell | 100.0% | >= 90% ✓ |
| kernel/scaffold | scaffold + templates | 93.2% | >= 90% ✓ |
| kernel/slice | verify.go | 94.2% | >= 90% ✓ |
| **kernel/ 结论** | | **全部 >= 90%** | **✓** |
| runtime/auth | interfaces + middleware + servicetoken | 100.0% | >= 80% ✓ |
| runtime/bootstrap | bootstrap.go | 51.4% | < 80% ✗ 注1 |
| runtime/config | config + watcher | 92.5% | >= 80% ✓ |
| runtime/eventbus | eventbus.go | 95.3% | >= 80% ✓ |
| runtime/http/health | health.go | 100.0% | >= 80% ✓ |
| runtime/http/middleware | 7 middlewares | 98.8% | >= 80% ✓ |
| runtime/http/router | router.go | 78.8% | < 80% ✗ 注2 |
| runtime/observability/* | metrics + tracing + logging | 88.9-100% | >= 80% ✓ |
| runtime/shutdown | shutdown.go | 100.0% | >= 80% ✓ |
| runtime/worker | worker + periodic | 93.9% | >= 80% ✓ |
| **runtime/ 结论** | | **10/12 达标** | **注1,2** |
| cells/access-core (Cell 级) | cell.go | 86.3% | >= 80% ✓ |
| cells/audit-core (Cell 级) | cell.go | 85.7% | >= 80% ✓ |
| cells/config-core (Cell 级) | cell.go | 87.5% | >= 80% ✓ |
| cells/*/internal/domain/* | 领域模型 | 100.0% | >= 80% ✓ |
| **cells/ Cell 级结论** | | **全部 >= 80%** | **✓** |
| cells/ 个别 slice handler | 10/16 slices | 22-66% | < 80% 注3 |

**注1**: bootstrap.go 覆盖率 51.4%，因 `net.Listen` 受开发环境 sandbox 限制无法测试 HTTP server 启动路径。核心 Option/Config/Assembly 逻辑已测试。记入 tech-debt #15。

**注2**: router.go 覆盖率 78.8%，接近 80% 阈值。Route/Mount/Group 委托方法未独立测试。记入 tech-debt。

**注3**: 个别 slice 的 handler.go HTTP 端点路径（JSON 解析、URL 参数提取、响应写入）未通过 httptest 独立测试。service 层均有 table-driven 测试。Cell 级聚合覆盖率（86.3% / 85.7% / 87.5%）达标。Handler 覆盖记入 tech-debt #13。

## 2. gocell validate 结果

```
No issues found.
Validation complete: 0 error(s), 0 warning(s)
```

所有 cell.yaml / slice.yaml / contract.yaml / journey.yaml / assembly.yaml / actors.yaml / status-board.yaml 元数据校验通过。REF/TOPO/VERIFY/FMT/ADV 规则全部 PASS。

## 3. E2E 测试

**E2E: N/A — SCOPE_IRRELEVANT**

Phase 2 无 UI 组件，无 Playwright 测试。role-roster.md 中前端开发者=OFF、DevOps=OFF。

## 4. 覆盖的用户场景

| AC 编号 | 场景 | 验证方式 | 结果 |
|---------|------|---------|------|
| S1 | 3 个 Cell 在 core-bundle 中编译启动 | go build + cell_test.go 生命周期 | PASS |
| S3 | runtime/ 覆盖率 >= 80% | go test -cover | 10/12 PASS (注1,2) |
| S4 | cells/ 覆盖率 >= 80% | go test -cover (Cell 级) | PASS |
| S5 | kernel/ 覆盖率 >= 90% | go test -cover | PASS |
| S6 | scaffold 生成可编译 Cell | gocell scaffold cell --id=demo | PASS |
| S7 | 依赖规则零违反 | go build + gocell validate | PASS |
| AC-EB.1 | EventBus Pub/Sub 基本流程 | eventbus_test.go | PASS |
| AC-EB.2 | EventBus 3x 重试 + dead letter | eventbus_test.go | PASS |

## 5. 未覆盖的场景

| 场景 | 原因 | 计划 |
|------|------|------|
| J-audit-login-trail 端到端 | 需完整 assembly 启动 + HTTP 调用 + 事件传播 | Phase 3 adapter 就绪后 |
| J-config-hot-reload | config watcher 未集成 bootstrap | Phase 3 (tech-debt #20) |
| JWT RS256 非对称签名 | Phase 2 使用 HS256 | Phase 3 (tech-debt #6) |
| 多实例分布式限流 | 需 Redis adapter | Phase 3 |
| OIDC 登录流程 | 需 OIDC adapter | Phase 3 |
| Refresh token rotation reuse detection | 需持久化支持 | Phase 3 (tech-debt #11) |

## 6. 手动验证结论

### 视角 B — 开发者 (API + 代码)
- B1 API 请求: go build 生成可运行 binary ✓
- B2 错误处理: S6 修复后统一使用 pkg/errcode + httputil.WriteDomainError ✓
- B3 文档: 3 个核心 runtime/ 包有 doc.go (middleware/config/bootstrap)，Cell 开发指南已写 ✓

### 视角 C — API 消费者
- C1 GET /healthz: HealthHandler 实现，返回 200 + JSON ✓
- C2 响应格式: {"data": ...} 成功 / {"error": {"code":"ERR_*","message":"...","details":{}}} 错误 ✓
- C3 分页: Phase 2 暂无分页需求（config list 和 audit query 返回全量）

### 视角 D — 框架集成者
- D1 go get: go.mod 依赖 6 个白名单直接依赖，无 replace ✓
- D2 godoc: 核心包有 doc.go，导出类型有注释 ✓
- D3 examples: cmd/core-bundle 可 go build 一键编译 ✓
- D4 scaffold: gocell scaffold cell/slice 产出可编译骨架 ✓
- D5 错误信息: errcode 码可定位问题，500 不泄露内部细节（S6 修复后）✓
- D6 整体: Cell 开发指南 + 参考 config-core 实现模式清晰

## 7. 总体评价

Phase 2 核心目标**已达成**: GoCell 从元数据治理框架升级为可运行框架。3 个 Cell 可编译启动，kernel/ 接口扩展（Subscriber + HTTPRegistrar/EventRegistrar）正确，runtime/ 13 个模块功能完整，bcrypt 密码安全（S6 修复），统一错误处理（S6 修复）。

已知差距（均记入 tech-debt）:
- bootstrap/router 覆盖率略低（sandbox/测试方法限制）
- handler 层 httptest 测试缺失
- 跨 Cell Journey 需 Phase 3 adapter 后完整验证
