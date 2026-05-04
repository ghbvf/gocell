# GoCell 系统工程逐层审查 — assemblies/ 层

| 项 | 值 |
|---|---|
| 审查日期 | 2026-05-04 |
| 基线 commit | 11600a4f |
| 审查范围 | `assemblies/`（含 `corebundle/assembly.yaml` + `generated/`） |
| 选定维度 | ① 边界 / ③ 依赖完整性 / ⑨ 可演进性 / ⑩ 第一性原理推导 |

## 0. 摘要

assemblies/ 是 GoCell 框架的"最远职责边界"——再往外就是客户应用与 k8s 部署。当前仅一个 `corebundle`，schema 极简（id / cells[] / build），通过 `gocell generate assembly` 派生 `cmd/{id}/main.go` + `assemblies/{id}/generated/boundary.yaml` + `metrics-schema.yaml`。整体结构清晰，但作为"层"的存在感非常薄：仅有 1 个目录、10 行 yaml、4 个字段，且与 cmd/ 编译单元一一对应。examples/ 全部绕过 assembly 模型。第 ⑩ 维度推导显示，assemblies/ 当前是"声明性 cell 清单 + 派生基点"，**结构合理但激励不足**——没有第二个 assembly 作为差异源，schema 已被 cmd 实现细节"反向定义"。

## 1. 评级表

| 维度 | 评级 | 说明 |
|---|---|---|
| ① 边界（最远职责边界） | ⚠️ 部分具备 | id/cells/build 字段够"打包"语义，但缺 owner/version/deployTemplate 取值域，且未声明 cell 集合的最大一致性等级 |
| ③ 依赖完整性 | ✅ 已具备 | `assembly-completeness` check + `boundary.yaml` 派生 import/export contract 闭包；fingerprint 锁定生成结果 |
| ⑨ 可演进性 | ❌ 缺失 | scaffold 不支持 `gocell scaffold assembly`；CLAUDE.md 未给"加第 2 个 assembly"的步骤；多 assembly 共享 cells 边界无规则 |
| ⑩ 第一性原理推导 | ⚠️ 部分具备 | 1 个 assembly 是阶段选择而非必然；examples/ 绕过此层是反证信号；与 cmd/ 编译单元的 1:1 关系存在冗余 |

## 2. 问题清单

#### [P1] assembly.yaml schema 缺最大一致性等级与 owner
- **维度**：① 边界
- **位置**：`assemblies/corebundle/assembly.yaml:1-9` + `kernel/metadata/types.go:247-260`
- **复杂度**：Cx2
- **现象**：`AssemblyMeta` 仅 `id / cells / build{entrypoint, binary, deployTemplate}`；cells/ 有 `owner.team` 与 `consistencyLevel`，journey 也有 `owner`，唯独 assembly 没有。assembly 是"上线对象"，运维侧最需要知道"这个二进制谁负责 / 集合内允许的最大一致性等级（决定 outbox/relay 必须存在）"。当前需要遍历 cells 反推。
- **建议方向**：AssemblyMeta 加 `owner`（必填）+ `maxConsistencyLevel`（派生校验，必须 ≥ 集合内最高），由 governance 在 `validate` 阶段双向锁定。

#### [P1] `deployTemplate: k8s` 字面量无取值域约束
- **维度**：① 边界
- **位置**：`assemblies/corebundle/assembly.yaml:9` + `kernel/metadata/types.go:256-260`
- **复杂度**：Cx1
- **现象**：`BuildMeta.DeployTemplate` 是 `string`，无 enum / schema 校验，`k8s` 没有对应的模板文件存在。形态层口径下不审 k8s yaml 内容，但 schema 必须拒绝拼写错误（`k8s` vs `kubernetes` vs `K8S`）和未实现的目标。
- **建议方向**：在 `metadata` 加 `validDeployTemplates = {"k8s", "compose", "binary"}`；未实现的模板返回 `ERR_ASSEMBLY_DEPLOY_TEMPLATE_UNSUPPORTED`，避免 "看起来声明了，其实空字符串也通过" 的隐式陷阱。

#### [P1] `gocell scaffold` 不覆盖 assembly
- **维度**：⑨ 可演进性
- **位置**：`cmd/gocell/app/scaffold.go:60-71` + `cmd/gocell/app/dispatch.go:82-83`
- **复杂度**：Cx2
- **现象**：scaffold 子命令仅 `cell|slice|contract|journey`，没有 `assembly`。`generate assembly` 只能在 `assembly.yaml` 已存在时基于 metadata 派生 main.go，**没有任何工具能创建第 2 个 assembly 的初始 yaml + cmd/{id}/run.go 脚手架**。这是 ⑨ 可演进性最直接的反证：当前 1 个 assembly 不是"够用"，而是"加第 2 个的成本不在工具链里"。
- **建议方向**：增加 `gocell scaffold assembly --id=<id> --cells=<a,b> --deploy=k8s`，同步生成 `assemblies/{id}/assembly.yaml` 和 `cmd/{id}/run.go` 占位（参考 cmd/corebundle/run.go），并在 CLAUDE.md 写"加第 2 个 assembly 的标准动作"。

#### [P2] examples/ 绕过 assemblies/ 模型 — 第二真理源
- **维度**：⑩ 第一性原理推导
- **位置**：`examples/ssobff/app.go:1-23`（无 `examples/ssobff/assembly.yaml`）+ `assemblies/corebundle/assembly.yaml`
- **复杂度**：Cx2
- **现象**：ssobff/iotdevice/todoorder/demo 全部用手写 `app.go` + `assembly.New(Config{...})` 的代码路径直接 wire cells，**没有 assembly.yaml**。corebundle 的 cmd/main.go 是"由 yaml 生成"，examples 的 main.go 是"手写"。这意味着 GoCell 实际有两套 assembly 表达：声明式（assemblies/*.yaml → 生成 cmd）+ 命令式（examples/*/app.go 直接组合）。任意性切分明显——examples 是"使用 GoCell 的客户参考"，反而不示范 assembly 模型本身。
- **建议方向**：要么把 examples 也接入 assembly.yaml 体制（`examples/{name}/assembly.yaml` + `gocell generate assembly` 派生 main.go），要么在 CLAUDE.md 显式声明"examples 是命令式 demo，不示范 assembly 声明流"——但二者必选其一，避免读者把 examples 的 app.go 当 assembly 写法学。

#### [P2] cmd/ 与 assemblies/ 的 1:1 镜像存在冗余
- **维度**：⑩ 第一性原理推导
- **位置**：`cmd/corebundle/main.go:1-29` + `cmd/corebundle/run.go:38-98` + `assemblies/corebundle/assembly.yaml`
- **复杂度**：Cx3
- **现象**：`assembly.yaml` 声明 `cells: [configcore, accesscore, auditcore]`；`cmd/corebundle/main.go`（生成）传 `[]string{"configcore","accesscore","auditcore"}`；`cmd/corebundle/run.go:83-97` 又写 `corebundleModules` switch case 把同样三个 ID 映射到 `XxxModule{}`。cell 列表写了三遍：yaml → 生成 main.go 字符串数组 → 手写 module switch。删 assemblies/ 让 cmd/ 自治，会丢失：(a) `gocell validate` 对 assembly cell 集合的闭包检查；(b) `boundary.yaml` 派生（contract 入/出口、smoke targets）；(c) `metrics-schema.yaml` 派生入口。这三项是 assemblies/ 层存在的真正必然性。但 `corebundleModules` 的 switch case 是 cmd/ 维护的"手工映射表"，本质上仍是 cell 清单——这部分可以由 codegen 从 assembly.yaml 派生，消除三重写入。
- **建议方向**：`gocell generate assembly` 同时派生 `cmd/{id}/modules_gen.go`（cell ID → CellModule 工厂表），`run.go` 只保留环境加载和 bootstrap 装配；ID 列表只保留 yaml 一份。

## 3. 第 10 维度专项推导：assemblies/ 层存在的必然性

**问题**：当前仅 1 个 assembly（corebundle）。删了它，把 4 个字段直接内嵌 `cmd/corebundle/main.go` 顶部常量，会丢失什么？

**保留信号（assemblies/ 必须存在）**：
1. `boundary.yaml` 派生（`assemblies/corebundle/generated/boundary.yaml:1-44`）—— exportedContracts/importedContracts/smokeTargets 闭包，**只能从 assembly.cells[] 算**，cmd/ 拿不到 metadata Project 视图。
2. `metrics-schema.yaml` 派生（`assemblies/corebundle/generated/metrics-schema.yaml:1-20`）——以 `entrypoint: cmd/corebundle/main.go` 作为 go/packages 的根，扫描可达 metric。entrypoint 字段必须在某个声明文件里，cmd/main.go 自身指向自己产生循环。
3. `assembly-completeness` check（`cmd/gocell/app/check.go:42`）—— 校验 cells 集合的 contract 闭合，**消费的是 yaml**，不是 cmd/ Go 代码。

**反证信号（assemblies/ 当前形态可疑）**：
1. examples/ 全部不用 assembly.yaml（见 P2 finding），证明"客户使用 GoCell"未必经过 assembly 层；
2. `cmd/corebundle/run.go:83-97` 的 `corebundleModules` switch 是与 yaml 平行的手工映射表，没有从 yaml 派生，说明现状下 yaml 的"权威源"地位是局部的；
3. `BuildMeta` 三字段 + `cells[]` 不足以表达"如何打包"——版本、签名、SBOM、镜像 tag 全都不在 schema 里，schema 太薄反而暗示"目前只是个目录壳"。

**结论**：assemblies/ 层有必然性（boundary 闭包 + metrics 入口 + completeness 校验），但**当前 schema 严重欠定义**。N=1 客户不是"必然 N=1"，而是"工具链没准备好 N≥2"。激进路径：扩 schema（owner / maxConsistencyLevel / deployTemplate enum / version）+ 加 scaffold + 派生 modules_gen.go 消除 cmd/ 镜像，让 assembly 真正成为"再外面就是客户"的边界，而不是 corebundle 的标签。

## 4. 跨层观察

- **assemblies ↔ cells**：`AssemblyMeta.Cells []string` 是字符串引用，由 `registry.NewCellRegistry(project)` 在生成期解析。无字符串拼写校验前置——错填 cell ID 由 `corebundleModules` 的 switch default 在运行时报 `unsupported assembly cell` 兜底（`cmd/corebundle/run.go:93-95`）。建议 `gocell validate` 期就拦截。
- **assemblies ↔ cmd**：1:1 命名约定（assemblies/corebundle ↔ cmd/corebundle）未在 schema 或工具中编码——靠人类约定。`generateAssembly` 用 `asm.Build.Entrypoint` 字段决定写入路径（`cmd/gocell/app/generate.go:91-95`），如果有人把 `entrypoint: cmd/foo/main.go` 配在 `assemblies/bar/`，工具不会报错。
- **assemblies ↔ examples**：完全脱节（见 P2）。examples 是"GoCell 用法示例"，不示范 assembly 声明，对外部读者形成认知割裂。
- **assemblies ↔ contracts**：`boundary.yaml` 派生 exportedContracts/importedContracts，是 assembly 视角的合同闭包视图。配合 `assembly-completeness` check 形成有效的依赖闭合护栏。

## 5. 一句话结论

assemblies/ 层有不可消除的必然性（boundary 闭包 + metrics 入口 + completeness 校验），但当前 schema 欠定义、scaffold 缺失、与 cmd/ 镜像冗余、examples 全部绕开——结构合理而激励不足，**N=1 是工具阶段问题，不是架构终态**。
