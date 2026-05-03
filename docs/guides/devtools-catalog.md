# DevTools Catalog 使用指南

## 概览

`gocell export catalog` / `GET /api/v1/devtools/catalog` 提供统一的项目元数据目录（Unified Catalog），把 GoCell 项目中的 Cell、Slice、Contract、Journey、Assembly、Actor 实体，以及 cell 级依赖图（`cellDeps`）、包级 typed dep graph（`packageDeps`）、状态看板（`statusBoard`）整合为单一文档，通过查询参数（query / flag）按需裁剪输出。

设计目标：

- **gocell-web 解耦**：前端构建时调 `gocell export catalog --out=public/catalog.json`，同源 `fetch('/catalog.json')` 加载，零 CORS，零 live endpoint 部署耦合。
- **单端点多视图**：CLI flags 与 HTTP query 参数语义对称，前后端消费相同 wire schema。
- **admin-gated 默认**：HTTP 端点默认 `admin` 角色访问（`auth.AnyRole("admin")`），符合 PR-CFG-4 fail-secure 范式。

Wire schema 借鉴 [Backstage Catalog Entity model](https://backstage.io/docs/features/software-catalog/descriptor-format)（不引任何 Backstage 依赖），包级依赖图借鉴 [loov/goda](https://github.com/loov/goda) 内部 pkggraph 数据模型。

---

## CLI 用法

主命令为 `gocell export catalog`，`gocell export metadata` 是其别名（输出完全相同，byte-equal）。

### Flag 全表

| Flag | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--kinds` | 逗号列表 | `""`（全部） | 实体类型筛选，可选值：`Cell,Slice,Contract,Journey,Assembly,Actor` |
| `--layers` | 逗号列表 | `""`（全部） | 层筛选，作用于 packageDeps 节点过滤 + cell 归属层，可选值：`cells,kernel,runtime,adapters,pkg,cmd,examples,tools` |
| `--cells` | 逗号列表 | `""`（全部） | Focus 模式：仅输出指定 cell + 一阶邻居（依赖/被依赖 cell、所属 slice、contractUsages） |
| `--include` | 逗号列表 | `cellDeps,packageDeps,statusBoard,relations`（全开） | 可选输出块，值：`cellDeps,packageDeps,statusBoard,relations` |
| `--format` | `json\|yaml` | `json` | 输出格式 |
| `--out` | 文件路径 | `""`（stdout） | 输出文件路径；空则写 stdout |
| `--root` | 目录路径 | `"."` | GoCell 项目根目录（含 cells/、contracts/、journeys/ 等） |

### 示例

**示例 1：默认全量输出（stdout，JSON）**

```bash
gocell export catalog
```

输出包含所有实体、cellDeps、packageDeps、statusBoard、relations。包级 dep graph 同步加载（5-10s，CI/Docker build 场景可接受）。

**示例 2：按实体类型过滤，只看 Cell 和 Contract**

```bash
gocell export catalog --kinds=Cell,Contract --format=yaml --out=catalog.yaml
```

`entities` 只含 Kind=Cell 和 Kind=Contract 的条目，其余块不受影响。

**示例 3：聚焦 accesscore cell + 关系视图**

```bash
gocell export catalog --cells=accesscore --include=cellDeps,relations
```

输出仅包含 `accesscore` 及其一阶邻居实体，附 cellDeps 图和 relations 列表；packageDeps 和 statusBoard 不输出（未在 `--include` 中声明）。

**示例 4：导出包级 dep graph（YAML 格式）**

```bash
gocell export catalog --include=packageDeps --format=yaml --out=public/package-deps.yaml
```

触发同步 `tools/depgraph.Load()`（约 5-10s），输出 `dependencies.packages` 块含完整包图节点和边。

**示例 5：gocell-web Dockerfile build 阶段嵌入**

```dockerfile
RUN gocell export catalog --include=cellDeps,packageDeps,statusBoard,relations \
    --out=public/catalog.json
```

前端使用：

```typescript
const catalog = await fetch('/catalog.json').then(r => r.json());
```

---

## HTTP 用法

### 端点

```
GET /api/v1/devtools/catalog
```

- 鉴权：`admin` 角色（`auth.AnyRole("admin")`），非 admin 返回 403，未认证返回 401。
- bootstrap-wired：与 `runtime/http/health/` 同范式，无 `contract.yaml`（framework 自省路由，见下文工程注意）。
- 需通过 `GOCELL_PROJECT_ROOT` 环境变量指定项目根目录（corebundle 默认读取），否则 handler 不注册。

### Query 参数全表

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `kinds` | 逗号列表 | `""` | 同 CLI `--kinds` |
| `layers` | 逗号列表 | `""` | 同 CLI `--layers` |
| `cells` | 逗号列表 | `""` | 同 CLI `--cells` |
| `include` | 逗号列表 | `cellDeps,packageDeps,statusBoard,relations` | 同 CLI `--include` |
| `format` | `json\|yaml` | `json` | 响应格式；yaml 时 Content-Type: application/yaml |
| `wait` | `true\|false` | `false` | 若 `packageDeps` 正在加载中，`wait=true` 返回 503 + Retry-After；`false` 返回 200 含 `status: "loading"` |

### curl 示例

**示例 1：admin token 正常访问**

```bash
# 获取 admin token（开发环境）
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/sessions \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"YOUR_ADMIN_PASSWORD"}' \
  | jq -r '.data.accessToken')

# 拉取完整 catalog
curl -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/api/v1/devtools/catalog' | jq '.schemaVersion, (.entities | length)'
```

**示例 2：非 admin 用户访问（403）**

```bash
curl -s -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer $NON_ADMIN_TOKEN" \
  'http://localhost:8080/api/v1/devtools/catalog'
# 输出: 403
```

**示例 3：未认证访问（401）**

```bash
curl -s -o /dev/null -w "%{http_code}" \
  'http://localhost:8080/api/v1/devtools/catalog'
# 输出: 401
```

**示例 4：聚焦 cell + packageDeps 组合**

```bash
curl -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/api/v1/devtools/catalog?cells=accesscore&include=packageDeps,cellDeps' \
  | jq '.dependencies.packages.status, .dependencies.cells.nodes'
```

### lazy-load 状态机（`dependencies.packages.status`）

包级 dep graph 在 bootstrap 后台 goroutine 异步加载（`tools/depgraph.Load()`，约 5-10s）。Handler 持有原子状态：

```
bootstrap 启动
    │
    ▼
 loading ──── goroutine 完成 ──── ready
    │
    └──────── goroutine 失败 ──── error
```

| `status` 值 | 含义 | 行为 |
|-------------|------|------|
| `"loading"` | 包图尚未就绪 | 返回 200，`dependencies.packages: {"status": "loading"}`；加 `?wait=true` 改返 503 + `Retry-After: 5` |
| `"ready"` | 包图已就绪 | 返回 200，`dependencies.packages.graph` 含完整图数据 |
| `"error"` | 包图加载失败 | 返回 200，`dependencies.packages: {"status": "error", "error": "..."}`；其余块（entities/cellDeps/statusBoard）正常返回，不受影响 |

---

## gocell-web 集成

在 `gocell-web` 的 `Dockerfile` build 阶段调用 CLI 嵌入静态 catalog 文件，前端同源加载，无需任何 live endpoint：

```dockerfile
# Stage 1: 导出 catalog（需要 gocell CLI + 项目源码）
FROM golang:1.24-alpine AS catalog-builder
WORKDIR /gocell
COPY . .
RUN go install ./cmd/gocell && \
    gocell export catalog \
      --include=cellDeps,packageDeps,statusBoard,relations \
      --out=public/catalog.json

# Stage 2: 前端构建
FROM node:22-alpine AS frontend
WORKDIR /app
COPY --from=catalog-builder /gocell/public/catalog.json public/catalog.json
# ... npm install && npm run build ...
```

前端加载（TypeScript）：

```typescript
// 同源加载，零 CORS
const resp = await fetch('/catalog.json');
const catalog: CatalogDocument = await resp.json();
```

---

## Wire Envelope Schema 摘要

顶层结构（`schemaVersion: "v1"`, `apiVersion: "gocell.io/v1alpha1"`）：

```json
{
  "schemaVersion": "v1",
  "apiVersion": "gocell.io/v1alpha1",
  "generatedAt": "2026-05-03T00:00:00Z",
  "root": "/path/to/project",
  "query": { "kinds": [], "layers": [], "cells": [], "include": "..." },
  "entities": [
    {
      "kind": "Cell",
      "metadata": { "name": "accesscore", "owner": "...", "labels": {} },
      "spec": { "consistencyLevel": "L1", "type": "core" }
    }
  ],
  "statusBoard": [
    { "id": "J-useronboarding", "status": "planned", "owner": "..." }
  ],
  "dependencies": {
    "cells": {
      "nodes": ["accesscore", "configcore"],
      "edges": [{ "from": "accesscore", "to": "configcore" }]
    },
    "packages": {
      "status": "ready",
      "graph": {
        "rootModule": "github.com/ghbvf/gocell",
        "packages": [ { "importPath": "...", "layer": "cells", "cellID": "accesscore" } ],
        "edges": [ { "from": "...", "to": "..." } ]
      }
    }
  }
}
```

三个顶层功能块：

| 块 | 字段 | 说明 |
|----|------|------|
| `entities` | `[]Entity` | Cell/Slice/Contract/Journey/Assembly/Actor 实体列表，Backstage Entity model 结构 |
| `statusBoard` | `[]StatusBoardEntry` | 来自 `journeys/status-board.yaml`，status draft/planned 字段脱敏 |
| `dependencies` | `*Dependencies` | 包含 `cells`（cell 级依赖图）和 `packages`（包级 typed dep graph）两个子块 |

`entities[*].relations` 字段（如 hasPart/partOf/dependsOn/ownedBy）对称遵循 [Backstage well-known relations](https://backstage.io/docs/features/software-catalog/well-known-relations) 命名约定。

---

## Backstage 借鉴说明

本模块借鉴了 Backstage Catalog 的实体模型（Entity / kind / metadata / spec 层级结构）和关系语义（well-known relations），但**不引入任何 Backstage 依赖**：

- 仅复用其文档化的命名约定和 wire shape，满足 GoCell 项目的 cell-native 治理需求
- GoCell 的 Entity 是 Cell/Slice/Contract/Journey/Assembly/Actor，与 Backstage 的 Component/API/System/Domain 存在映射但不完全等价
- 未来若需要接入 Backstage 生态，wire schema 差量最小化，适配成本低

---

## 工程注意

### bootstrap-wired（无 contract.yaml）

`/api/v1/devtools/catalog` 路由落在 `runtime/http/devtools/` 包，通过 bootstrap `WithDevtoolsCatalog(pm, root)` Option 接入，与 `runtime/http/health/` 同范式（framework 自省路由）。**不建 cell、不建 contract.yaml**，原因：

1. devtools 无业务状态、无事件、无 outbox，建 cell 净增 ~5 YAML/schema 文件
2. 描述 catalog API 的 contract 自身会在 catalog 目录中被它自己描述，引发语义循环
3. 当前唯一鉴权需求（admin-gated）由 bootstrap-level `auth.AnyRole("admin")` 直接满足，无需 contract-level RBAC

未来若触发升级条件（见 backlog T10 DEVTOOLS-CELL-PROMOTION-01），可将其迁移为正式 cell。

### 环境变量

`corebundle` 通过 `GOCELL_PROJECT_ROOT` 决定是否启用 devtools handler：

```bash
# 本地开发（项目根目录）
GOCELL_PROJECT_ROOT=/path/to/gocell ./bin/corebundle

# Docker 部署
ENV GOCELL_PROJECT_ROOT=/app
```

未设置 `GOCELL_PROJECT_ROOT` 时，`WithDevtoolsCatalog` 不注册路由（不影响服务启动）。

### CLI 包级加载耗时

`--include=packageDeps` 触发同步调用 `tools/depgraph.Load()`（基于 `golang.org/x/tools/go/packages`），耗时约 5-10s。适用于：

- CI build 阶段（单次执行，可接受）
- Docker build-time 嵌入静态文件
- 本地开发调试

**不包含 `packageDeps`** 时（如 `--include=cellDeps,statusBoard,relations`），执行时间 < 1s。

HTTP 端点异步加载，不阻塞服务启动。

### 升级路径

若满足以下任一触发条件，参考 backlog T10 DEVTOOLS-CELL-PROMOTION-01 进行 cell 化迁移：

- (a) 某 cell 需要在 catalog 中携带 cell-自定义字段且需 contract schema 强校验
- (b) devtools 需要发事件（订阅/广播 catalog 变更）
- (c) 出现非 admin 的细粒度 RBAC 需求（不同角色看不同字段）

---

## 参考（commit message ref）

以下三条 ref 随本特性的 commit message 一并记录：

- `ref: backstage/backstage packages/catalog-model/src/entity/Entity.ts@master`
- `ref: backstage/backstage docs/features/software-catalog/well-known-relations.md@master`
- `ref: loov/goda internal/pkggraph`
