# Research Notes — Phase 4: Examples + Documentation

## 1. 示例项目结构对标

### go-zero examples/
- 每个示例独立目录，含 `etc/config.yaml` + `main.go`
- 使用 goctl 生成的标准目录结构（handler/logic/svc/types）
- **采纳**: 独立目录 + main.go 入口
- **偏离**: GoCell 使用 Cell/Slice 目录约定而非 go-zero 的 handler/logic 扁平结构

### Kratos examples/
- `examples/helloworld/` 最简示例 + `examples/blog/` 完整 CRUD
- 梯度复杂度设计（从 hello world 到 DDD 分层）
- **采纳**: 梯度复杂度（todo-order: 中 → sso-bff: 中高 → iot-device: 高）

## 2. README Getting Started 对标

### Uber fx README
- 一句话定义 + 30 行代码 quickstart
- 核心概念用代码示例而非文档解释
- **采纳**: 代码驱动的概念说明
- **偏离**: GoCell 需要 cell.yaml 声明式概念，纯代码不够

### go-micro README
- 安装 → 创建服务 → 运行 → 测试，4 步快速开始
- **采纳**: 步骤化快速开始结构

## 3. testcontainers 集成测试对标

### Watermill _examples/tests
- 使用 docker compose 启动依赖 + go test
- 每个 adapter 有独立的 integration_test.go
- **采纳**: adapter 独立 integration_test.go + build tag 隔离

### pgx pgxpool_test
- 使用环境变量 `PGX_TEST_DATABASE` 连接真实 PG
- **偏离**: GoCell 使用 testcontainers 自动管理容器生命周期

## 4. CI workflow 对标

### Kratos .github/workflows
- lint → test → build 三阶段
- matrix strategy 多 Go 版本
- **采纳**: 三阶段结构
- **偏离**: GoCell 增加 gocell validate + 分层检查 + 覆盖率门控

## 5. L4 DeviceLatent 模式研究

### AWS IoT Device Shadow
- 设备影子模式：cloud 端维护 desired state，设备端上报 reported state
- 命令通过 MQTT topic 下发，回执通过 delta topic 确认
- **采纳**: 命令入队 + 回执确认模式
- **偏离**: GoCell 使用 HTTP API + WebSocket（非 MQTT），更适合 Go 后端场景

### Eclipse Ditto
- 数字孪生模式，REST API + WebSocket 事件通知
- **采纳**: REST API 命令下发 + WebSocket 状态推送

## 6. 项目模板对标

### Kubernetes hack/boilerplate
- ADR 使用 MADR 格式（Markdown Architecture Decision Record）
- **采纳**: MADR 格式 ADR 模板

### Google SRE Runbook 模板
- Alert/Triage/Mitigation/Escalation 四段结构
- **采纳**: Cell 健康检查 + 故障排查 + 回滚步骤结构
