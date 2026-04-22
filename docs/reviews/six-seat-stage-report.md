# {StageName} 六席位分析报告

> 日期: {YYYY-MM-DD}
> 阶段: {Step1|Step2|Step3-domain|Step4-layer|Step5-top10}
> 输入范围: {sources}
> 基线: {branch/commit/time-window}

## 1. 执行摘要

- 总 findings: {N}
- 严重级别分布: P0={x}, P1={y}, P2={z}
- 复杂度分布: Cx1={a}, Cx2={b}, Cx3={c}, Cx4={d}
- 六席位覆盖率: {percent}%
- 是否存在阻断项: {yes/no}

## 2. 六席位独立结果

### 架构席位
| ID | Severity | 问题 | 证据 | 根因 | 修复方向 |
|---|---|---|---|---|---|
| A-01 | P1 | ... | path:line | ... | ... |

### 安全席位
| ID | Severity | 问题 | 证据 | 根因 | 修复方向 |
|---|---|---|---|---|---|
| S-01 | P0 | ... | path:line | ... | ... |

### 测试回归席位
| ID | Severity | 问题 | 证据 | 根因 | 修复方向 |
|---|---|---|---|---|---|
| T-01 | P1 | ... | path:line | ... | ... |

### 运维部署席位
| ID | Severity | 问题 | 证据 | 根因 | 修复方向 |
|---|---|---|---|---|---|
| O-01 | P1 | ... | path:line | ... | ... |

### 可维护性/DX席位
| ID | Severity | 问题 | 证据 | 根因 | 修复方向 |
|---|---|---|---|---|---|
| M-01 | P2 | ... | path:line | ... | ... |

### 产品体验席位
| ID | Severity | 问题 | 证据 | 根因 | 修复方向 |
|---|---|---|---|---|---|
| P-01 | P1 | ... | path:line | ... | ... |

## 3. 根因簇归并（跨席位）

| RootCauseCluster | 共享根因 | 涉及席位 | 关联问题数 | 影响范围 | 统一修复方向 |
|---|---|---|---|---|---|
| RC-A | ... | 架构/安全/测试 | 5 | runtime+cells | ... |

## 4. 分类与频次

### 按领域
| Domain | Count | P0 | P1 | P2 |
|---|---|---|---|---|
| auth | 0 | 0 | 0 | 0 |

### 按分层
| Layer | Count | P0 | P1 | P2 |
|---|---|---|---|---|
| runtime | 0 | 0 | 0 | 0 |

## 5. 处置方案矩阵

| ID/Cluster | Decision | 优先级 | 方案A(最小修复) | 方案B(彻底修复) | 验证计划 |
|---|---|---|---|---|---|
| RC-A | now | P0 | ... | ... | ... |

## 6. 开源对标证据（仅需外部论证项）

| Theme | Project | Source | Pattern | Applicability | Status |
|---|---|---|---|---|---|
| auth boundary | Kubernetes | ... | ... | adapt | complete |
| auth boundary | Uber fx | ... | ... | adopt | complete |
| auth boundary | Kratos | ... | ... | adapt | complete |

门禁结果:
- 是否满足每主题 >= 3 项目: {yes/no}
- 如否，必须声明: 外部对比不足，不能下最佳实践结论。

## 7. 风险与下一步

1. 阻断项: {list}
2. 本迭代处理项: {list}
3. 延后项: {list}
4. 下一阶段输入清单: {list}
