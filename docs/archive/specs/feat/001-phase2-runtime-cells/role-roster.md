# Role Roster — Phase 2

## Core Mandatory
| 角色 | 状态 | 备注 |
|------|------|------|
| 总负责人 | ON | 调度+裁决+合并 |
| 架构师 | ON | runtime 分层设计 + Cell 接口审查 |
| 产品经理 | ON | 验收标准: 3 cell 运行 + 8 journey 通过 |
| 项目经理 | ON | 3 大块并行调度 (runtime / access / audit+config) |
| Kernel Guardian | ON | kernel 依赖规则守护，runtime/ 不得反向依赖 cells/ |

## Conditional Delivery
| 角色 | 状态 | 理由 |
|------|------|------|
| 后端开发者 | ON | 核心实施角色，纯 Go 后端 Phase |
| 前端开发者 | OFF | SCOPE_IRRELEVANT: Phase 2 无前端代码 |
| 文档工程师 | ON | runtime API 文档 + Cell 使用说明 |
| DevOps | OFF | SCOPE_IRRELEVANT: 无部署环节，Phase 3 才涉及 docker-compose |
| QA 自动化 | ON | 覆盖率要求 kernel>=90% runtime/cells>=80% |

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
| 前端开发者 | SCOPE_IRRELEVANT | 1 (首次) | 无 |
| DevOps | SCOPE_IRRELEVANT | 1 (首次) | 无 |
