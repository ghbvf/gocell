# Role Roster — Phase 3: Adapters

## Core Mandatory
| 角色 | 状态 | 备注 |
|------|------|------|
| 总负责人 | ON | 调度+裁决+合并 |
| 架构师 | ON | adapter 接口设计审查 + 分层隔离验证 |
| 产品经理 | ON | adapter 验收标准 + 集成测试验收 |
| 项目经理 | ON | 6 adapter 并行调度 + tech-debt 分层跟踪 |
| Kernel Guardian | ON | kernel 接口实现合规 + 分层隔离守护 |

## 始终启用
| 角色 | 状态 |
|------|------|
| 后端开发者 | ON |
| 文档工程师 | ON |
| DevOps | ON |
| QA 自动化 | ON |

## Conditional Delivery
| 角色 | 状态 | 理由 |
|------|------|------|
| 前端开发者 | OFF | SCOPE_IRRELEVANT — Phase 3 纯后端 adapter 层实现，项目无 UI 组件 |

## Review Bench
| 席位 | 状态 |
|------|------|
| 架构一致性 | ON |
| 安全/权限 | ON |
| 测试/回归 | ON |
| 运维/部署 | ON |
| DX/可维护性 | ON |
| 产品/用户体验 | ON |

## 其他
| 角色 | 状态 |
|------|------|
| 使用者 | ON |
| Roadmap 规划师 | ON |

## 跳过记录
| 角色 | 跳过原因 | 连续跳过次数 | 警告级别 |
|------|---------|-------------|---------|
| 前端开发者 | SCOPE_IRRELEVANT | 2（Phase 2 + Phase 3） | 无 — 项目定位为纯后端 Go 框架，无 UI 层，连续跳过理由持续成立。若 Phase 4 examples 含前端 BFF 演示则需重新评估 |

## N/A 声明（前端开发者 OFF）

| N/A 声明 |
|---------|
| N/A:SCOPE_IRRELEVANT evidence/playwright/result.txt |
| N/A:SCOPE_IRRELEVANT playwright.config.ts |
