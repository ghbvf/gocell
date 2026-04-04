# metadata-model-v2.md 六角色分析（第三轮）

**分析日期**: 2026-04-04
**分析对象**: `docs/architecture/metadata-templates-v2.md`（871 行）
**约束**: 严格只看本文件，不交叉引用其他文档
**与前轮关系**: R1(85发现) → R2(69发现) → 本轮

---

## R2 修复验证

| R2 发现 | 修复方式 | 验证结果 |
|---------|---------|---------|
| H1: cell.id/slice.id 无目录校验 | B12/B13 新增 | **有效** — 全角色确认 |
| H2: validate-meta 与 select-targets 无执行顺序 | L794 显式声明 | **有效** |
| H7: verify identifier 格式校验空白 | D8 扩展到所有 verify 位置 | **有效**，但缺前缀-位置一致性约束 [T-02a] |
| H8: sourceFingerprint 缺失 | L784 新增 + D15 规则 | **方向正确**，计算规范待精确化 [T-03, S-02] |
| H9: L0 cell trade-off 缺声明 | L298-306 新增 | **有效** |
| H10: journey 多重职责未标注 | L170 trade-off 声明 | **有效** |
| H11: curated 标准不可判定 | L168 改为 "asserted by passCriteria" | **部分有效** — "间接"边界仍模糊 [D-04] |

**总结**: R2 的 7 个高严重度问题中，5 个有效修复，2 个部分修复。

---

## 本轮发现汇总

| 严重度 | 数量 | 主要来源 |
|--------|------|---------|
| 高 | 5 | 魔鬼代言人 3, 领域专家 2, 架构师 1 |
| 中 | 12 | 各角色均有 |
| 低 | 8 | 各角色均有 |

---

## 高严重度发现

### F1: contract.id domain-path 与目录路径无验证规则（2/6 角色）

**发现者**: 架构师 [A-05]、魔鬼代言人 [S-01]

B7 只检查 version 段，D9 只检查 kind 段，D12 交叉校验 kind。但 **domain-path 中间段**与目录路径的对应关系（dots → 目录分隔符）完全没有规则���`contracts/http/auth/login/v1/contract.yaml` 中可以声明 `id: http.user.profile.v1`，validate-meta 无法捕获。

**建议**: 新增 B14: contract.id 的 domain-path 段（dots → 目录分隔符）必须等于 contract.yaml 所在目录的中间路径段。

### F2: C15 与 curated criterion 结构性矛盾 → 系统性 false positive（4/6 角色）

**发现者**: 魔鬼代言人 [S-04]、DX [X-4-01]、领域专家 [D-06]、PM [P-15]

C15 扫描 journey.cells 中每个 cell 的**所有** slice contractUsages，发现不在 journey.contracts 中的 active contract 就 warning。但 L168 的 curated criterion 说 journey.contracts **只列** passCriteria 直接/间接断言的 contract——大量 contract 不在列表中是设计意图。

**后果**: 中等规模项目中 C15 会产生数百条 false positive warning，磨灭信号价值。

**建议**: C15 扫描范围限制为"沿 passCriteria → checkRef → contract 路径追踪的 contract"，而非 cell 下所有 slice 的全部 contractUsages。

### F3: "间接 assert" 无终止条件，不可机器验证（1/6 角色）

**发现者**: 领域专家 [D-04]

L168 说 "directly or **indirectly** asserted by a passCriteria entry"。直接可判定（checkRef 语法解析出 contract id）。间接完全模糊——运行时依赖？mock 调用链？下游 subscriber 触发？validate-meta 无法判定"间接"关系。

**建议**: 删除 "indirectly"，只保留可判定标准（passCriteria checkRef 可语法解析出的 contract id）。或精确定义"间接"的有限步骤。

### F4: sourceFingerprint 双工具生成一致性未定义（2/6 角色）

**发现者**: 魔鬼代言人 [S-02]、工具工程师 [T-03]

L779 说索引由 validate-meta **或** generate-assembly 生成。两者输入文件集可能不同（全局 vs assembly-scoped），hash 排序/算法未规范。一个工具生成、另一个验证时会误报 stale。

**建议**: 收窄为只由 validate-meta 生成（它已处理全局元数据），generate-assembly 只消费。或规范化 fingerprint 计算（输入集、排序、算法）。

### F5: C13+C15 级联盲区：contract 和 cell 同时遗漏时不可检测（1/6 角色）

**发现者**: 领域专家 [D-08]

如果 journey 完全遗漏了某个 cell（也没列该 cell 的任何 contract），C13 不触发（无相关 contract 可检查），C15 不触发（cell 不在 journey.cells 中）。文档在 L849 承认了 "cannot prove completeness against the full contract universe"，但措辞可更显式。

---

## 跨角色共识

### 共识 A: allowedFiles 分类矛盾（三轮共识）

**发现者**: 架构师 [A-01]、领域专家 [D-07]（本轮）+ R1/R2 多角色

derived-anchor 定义 "declared == computed"，但 allowedFiles 声明时完全替换计算值。三轮审查的最稳定共识。

**建议**: derived-anchor 拆分 strict/defaulted 子类型，或将 allowedFiles 重新归类。

### 共识 B: 共享代码（pkg/、proto/）路由黑洞（3/6 角色）

**发现者**: 魔鬼代言人 [S-06]、DX [X-7-02]、架构师 [A-13]

不在任何 cell/slice/contract 目录下的文件变更 → catch-all 匹配 allowedFiles → 如果不匹配则 "no routing"。pkg/errcode 的 bug fix 影响所有 cell 但不触发任何 journey。

**建议**: select-targets 输出 "unroutable files" 列表并建议 full-scope 测试。或允许 repo 级 routing-overrides 配置。

### 共识 C: validate-meta 退出码/输出策略缺失（2/6 角色）

**发现者**: 工具工程师 [T-05]、PM（隐含）

文档有 4 种 warning 场景（C4 ownerCell、C15 coverage、assembly optional fields、D15 stale），但没有统一的退出码策略。CI 中 warning 是否阻塞？如何区分 error-only 和 warning-only 的退出码？

### 共识 D: DX 三缺（速查表/任务导向/错误输出格式）三轮未改善

**发现者**: DX [X-1-01, X-1-02, X-1-03]

不再是文档层面能解决的——需要 scaffold 工具 + 好的 validate-meta 错误信息。

---

## 改善确认（正面）

| 改善 | 评价 |
|------|------|
| B12/B13 id 目录校验 | 所有路径推导闭环 |
| L0 Cell Interaction Model + trade-off | 概念定位清晰 |
| Journey multi-role trade-off 声明 | 防止误用 journey.contracts 为完整列表 |
| Contract-Inferred Mode "best-effort" 命名 | 不再过度承诺 |
| D8 全 verify identifier 格式校验 | 错误提前到 validate-meta 阶段 |
| consistencyLevel 全序关系显式定义 | 消除隐含假设 |
| sourceFingerprint 方向 | stale index 有了检测机制 |

---

## 文档成熟度评估

| 维度 | R1 | R2 | R3 | 趋势 |
|------|----|----|----|----|
| 高严重度发现 | 22 | 7 | 5 | 收敛 |
| 验证规则数 | 36 | 43 | 49 | 增长合理 |
| 工具规格完整度 | 2/6 | 3/6 | 3.5/6 | 缓慢改善 |
| DX 速查/导航 | 缺失 | 缺失 | 缺失 | **停滞** |

---

## 下一步建议

### 文档层面（收尾）

| 优先级 | 行动 |
|--------|------|
| P0 | 新增 B14: contract domain-path 目录一致性校验 |
| P0 | 修复 C15: 限制扫描范围为 passCriteria 断言路径 |
| P0 | 精确化 L168 curated criterion: 删除 "indirectly" 或给出终止条件 |
| P1 | 收窄 sourceFingerprint 生成者为 validate-meta only |
| P1 | 补写 validate-meta 退出码策略小节 |
| P1 | 补写 generate-assembly 独立工具规格章节 |
| P2 | derived-anchor 拆分 strict/defaulted 子类型 |
| P2 | D8 增加前缀-位置一致性约束 |

### 工具层面（主线）

**PM 核心建议 [P-13]**: 开始实现 validate-meta，并行补全其余工具规格。

| 顺序 | 工具 | 理由 |
|------|------|------|
| 1 | validate-meta | 49 条规则规格最充分，其他工具的前置依赖 |
| 2 | gocell scaffold | 解决 DX 三缺的根本方案——规则给工具背，不给人背 |
| 3 | select-targets | 规格第二充分 |
| 4 | verify-cell / verify-slice | 逻辑简单 |
| 5 | generate-assembly / run-journey | 需先补全规格 |
