# Role Roster — Phase 4: Examples + Documentation

## Core Mandatory
| 角色 | 状态 | 备注 |
|------|------|------|
| 总负责人 | ON | 调度+裁决+合并 |
| 架构师 | ON | 示例架构审查 + 分层隔离验证 |
| 产品经理 | ON | 开发者体验验收 + 30 分钟 Gate 验收 |
| 项目经理 | ON | 3 示例 + tech-debt 并行调度 |
| Kernel Guardian | ON | kernel 无退化守护 + 分层隔离验证 |

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
| 前端开发者 | OFF | SCOPE_IRRELEVANT — Phase 4 examples 为纯 Go 后端项目（sso-bff 是 Go BFF HTTP server，非前端 SPA），项目无 UI 组件 |

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
| 前端开发者 | SCOPE_IRRELEVANT | 3（Phase 2 + Phase 3 + Phase 4） | 已在 phase-charter.md 写明处理方案 — GoCell 纯后端框架定位，项目生命周期内无 UI 层，前端角色无适用场景 |

## N/A 声明（前端开发者 OFF）

| N/A 声明 |
|---------|
| N/A:SCOPE_IRRELEVANT evidence/playwright/result.txt |
| N/A:SCOPE_IRRELEVANT playwright.config.ts |
