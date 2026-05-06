# 代码 + metadata 同步回滚约束

GoCell 的 `kernel/metadata/schemas/*.json`、`kernel/metadata/types.go`、
`kernel/governance/rules_*.go`、`assemblies/*/assembly.yaml` 与
`cells/*/cell.yaml` 是同一资源契约的多种表达。当一次发布同时改动 schema
required 字段、Go 结构、governance 规则与示例 yaml 时，**代码与 metadata
必须作为同一原子单位回滚**，否则会出现版本错配启动失败。

## 风险来源

- schema `required` 增项：旧 yaml 缺字段 → parser `KnownFields(true)` 失败
- Go 字段类型迁移（如 `string → typed identifier`）：旧 yaml 写法触发
  unmarshal 拒绝
- governance 新规则（FMT-30 / 新 ADV / TOPO 等）：旧 yaml 通过现行 schema
  但被新规则拒
- codegen 模板字段类型变化：旧生成的 `cell_gen.go` / `modules_gen.go` 无法
  与新 metadata 类型一起编译

## 已知 break-change（K#10 ASSEMBLY-YAML-MINIMAL）

| 改动 | 影响 |
|------|------|
| `assemblies/*/assembly.yaml` 新增 `owner.team` / `owner.role` 必填 | 缺 owner 的旧 yaml 解析失败 |
| `assembly.yaml.id` 强制 `^[a-z][a-z0-9]+$` | 含 `-` 或大写字母的旧 id 在 default validate 即失败 |
| `assembly.yaml.build.deployTemplate` 限定 `{k8s, compose, binary}` | 自由值（如 `deploy.yaml`）在 default validate 即失败 |
| `cmd/{id}/modules_gen.go` 新增、`cell.yaml.goStructName` 类型化为 `metadata.GoIdentifier` | 旧 cell_gen.go 与新 types.go 不兼容；必须同时 regenerate |

## 回滚操作约束

### 必须

- 回滚同一 git commit 范围内的所有 `kernel/`、`cells/*/cell.yaml`、
  `assemblies/*/assembly.yaml`、`cmd/*/modules_gen.go`、`cells/*/cell_gen.go`
- 回滚后立刻执行 `go run ./cmd/gocell verify generated` +
  `./hack/verify-codegen-cell.sh` + `./hack/verify-codegen-contract.sh` +
  `./hack/verify-codegen-assembly.sh`，所有 4 个 gate 必须 PASS 才允许重启
- 应急场景下直接 `git revert <commit-range>`；禁止 cherry-pick 单文件回滚

### 禁止

- 仅回滚 `kernel/` 不回滚 `cells/*/cell.yaml`
- 仅回滚 `assemblies/*/assembly.yaml` 不回滚 `cmd/*/modules_gen.go`
- 在 schema 与 Go 字段类型分离回滚（生成产物必须与生成器版本严格匹配）

## 验证清单

回滚后启动前：

```bash
go run ./cmd/gocell verify generated
./hack/verify-codegen-cell.sh
./hack/verify-codegen-contract.sh
./hack/verify-codegen-assembly.sh
go run ./cmd/gocell validate --root .
```

任一 gate 红 → 回滚未完成，禁止重启服务。
