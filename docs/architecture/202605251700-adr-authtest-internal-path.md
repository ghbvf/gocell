# ADR: authtest 包移入 runtime/internal/（archtest B → Go internal/ Hard 升级）

Date: 2026-05-25
Status: Accepted
Related issue: #638 (`PR267-FU-AUTHTEST-INTERNAL`)
Related PR: #267 (source — created `runtime/auth/authtest`)
Supersedes path: `runtime/auth/authtest/` → `runtime/internal/authtest/`
AI-robust grade upgrade: AUTH-AUTHTEST-B Medium → Hard

## Context

PR #267（contract-as-auth-truth）删除 `runtime/auth.Authenticated()` 同时新建 `runtime/auth/authtest/` 子包，仅暴露 `RequireAuthenticated() auth.Policy` 供 runtime middleware 行为测试使用。当时的访问边界由 `tools/archtest/auth_authtest_boundary_test.go` 三条规则把守：

- **AUTH-AUTHTEST-A** — 禁止 `auth.Authenticated()` 字面量 call（防止被删函数被重新引入）
- **AUTH-AUTHTEST-B** — `cells/**` / `examples/**` / `kernel/**` 不得 import `runtime/auth/authtest`
- **AUTH-AUTHTEST-C** — 仅 `_test.go` 文件可 import `runtime/auth/authtest`

按 `.claude/rules/gocell/ai-robust.md` 三档分级：

- A：archtest AST-call detection（Medium，defense-in-depth — 主防线 Hard 来自符号删除）
- **B：archtest path-prefix import scan（Medium）** ← 本 ADR 升级对象
- C：archtest path-prefix import scan（Medium，无 Hard 升级路径——Go 无原生 `_test.go-only` import 限制）

B 是字符串锚点 archtest，AI 可在新建 cells/examples 文件时偶然绕过，CI 才发现。
ai-robust.md §Hard 范本目录"`internal/` wrap 包"指明此类边界可改为 Go compiler 编译期 gate。

## Decision

将 `runtime/auth/authtest/` 物理移到 `runtime/internal/authtest/`：

```
runtime/auth/authtest/{doc,policy,policy_test}.go
    → runtime/internal/authtest/{doc,policy,policy_test}.go
```

包名保持 `authtest`。

### 为何选 `runtime/internal/authtest/` 而非 `runtime/auth/internal/authtest/`

Go internal/ 规则：`x/y/internal/z` 可被 `x/y/...` subtree 内代码 import，其余不可。

实际 importer 集合（PR-time grep `'"github.com/ghbvf/gocell/runtime/auth/authtest"' -r --include="*.go"`）：

| Importer | Subtree |
|---|---|
| `runtime/bootstrap/bootstrap_test.go` | `runtime/bootstrap/` |
| `runtime/http/router/router_test.go` | `runtime/http/router/` |
| `runtime/http/router/router_authmeta_test.go` | `runtime/http/router/` |
| `runtime/auth/authtest/policy_test.go` | self |

`runtime/auth/internal/authtest/` 只允许 `runtime/auth/**` 内代码 import，会破坏 `runtime/bootstrap/`、`runtime/http/router/` 两个 consumer。

`runtime/internal/authtest/` 允许整个 `runtime/**` subtree import，恰好匹配现有 consumer set，同时阻断所有 `runtime/` 之外的 import（compile-time fail）。

> 注：issue #638 原 backlog 条目 AUTHTEST-PACKAGE-INTERNAL-PATH-01（`docs/backlog/20260520/archive/backlog.md:185`）原文写的是 `runtime/auth/internal/authtest/`，论证为"剩下两处都在 runtime/ 子树，internal 规则不会拒绝"。该论证 off-by-one——`runtime/bootstrap/` 与 `runtime/http/router/` 不在 `runtime/auth/**` 之内。本 ADR 修正之。

## Consequences

### 升级到 Hard（B）

`runtime/internal/authtest` 被 Go 编译器 internal/ 规则保护：

- `cells/` `examples/` `kernel/` `cmd/` `adapters/` `tools/` `tests/` 内任何文件（含 `_test.go`）尝试 import 都返回 compile error `use of internal package ... not allowed`。
- 删除 `auth_authtest_boundary_test.go` 中 `AUTH-AUTHTEST-B_cells_examples_kernel_no_authtest_import` 子测试与对应负面探针，约 50 LoC 净减。

### 保留 Medium（A、C）

- A：archtest AST 扫描继续守 `auth.Authenticated()` 字面 call 防止被删符号被重新声明（次防线；主防线是符号已不存在，编译期即可拦截）。无 Hard 升级路径——主防线本身已是 Hard，archtest 是冗余防御。
- C：archtest path-prefix 扫描继续守 "只有 `_test.go` 可 import authtest"。Go 没有原生 `_test.go-only` import 限制；若硬要 Hard 化必须发明 build-tag-based seal，反而比 archtest 更脆。

### Funnel 双向锁评级（per ai-robust.md §Funnel 双向锁评级）

B 是 visibility funnel（限制 import direction），不是 callsite funnel：

- 下游 = "subtree 外的 caller 不可 import" → **Hard（Go 编译器）**
- 上游 N/A — 单向能见性约束，无 "callsite 必经过 funnel" 反方向

A 是 callsite funnel（限制特定符号被调用），其下游 Medium / 上游 Hard（符号删除）已是终态。
C 是 caller-class funnel（按文件后缀分类），无 Hard 升级路径，按 Medium 暂留。

## Migration scope（同 PR 内闭合）

| 类型 | 文件数 | 文件 |
|---|---|---|
| 移动 | 3 | `runtime/auth/authtest/{doc,policy,policy_test}.go` → `runtime/internal/authtest/...` |
| 改 import path | 4 | `runtime/bootstrap/bootstrap_test.go`、`runtime/http/router/router_test.go`、`runtime/http/router/router_authmeta_test.go`、`runtime/internal/authtest/policy_test.go`（自指向） |
| 改 comment 路径引用 | 3 | `cmd/corebundle/setup_integration_test.go`、`cells/accesscore/slices/setup/handler_test.go`、`kernel/cell/celltest/mux_test.go` |
| Archtest 改写 | 2 | `tools/archtest/auth_authtest_boundary_test.go`（drop B + path 更新）、`tools/archtest/kernel_mustctor_production_decl_test.go`（allowlist + doc path） |
| ADR | 1 | 本文件 |
| ADR amendment | 1 | `docs/architecture/202605171800-adr-kernel-mustctor-removal.md`（inline 路径更新） |

不向后兼容：旧路径 `runtime/auth/authtest/` 完全删除，无 alias、无 re-export shim。

## Reference

- `ref: golang/go cmd/go/#hdr-Internal_Directories` — Go internal/ 包约定
- `.claude/rules/gocell/ai-robust.md` §Hard 范本目录 → "sealed construction / `internal/` wrap 包"
- `.claude/rules/gocell/ai-robust.md` §ADR amendment 落地必查（amendment 重写约束）
- 原 backlog: `docs/backlog/20260520/archive/backlog.md:185` AUTHTEST-PACKAGE-INTERNAL-PATH-01
- PR #267: contract-as-auth-truth（创建 `runtime/auth/authtest`）
- 前例 ADR: `docs/architecture/202605171800-adr-kernel-mustctor-removal.md`（B2-K-02 — 删除 `Must*` 构造器 + 物理重定位 test fixture，Medium → Hard 通过 deletion + relocation 同源套路）
