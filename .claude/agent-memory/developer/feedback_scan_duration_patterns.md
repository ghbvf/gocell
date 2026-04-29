---
name: duration-const archtest upgrade pattern
description: How to upgrade a pure-AST archtest scanner to types-based packages.Visit + 5-node predicate
type: feedback
---

When upgrading a pure-AST archtest to types-based:

1. Use `typeseval.LoadPackages(root, prodscan.Patterns(root)...)` + `packages.Visit` (not SharedResolver, which is for the outbox_topic scanner that needs TypesInfo).
2. `visited[abs]` map is mandatory to prevent duplicate scans when packages appear multiple times in the dependency graph.
3. `i >= len(p.GoFiles)` guard is required before indexing `p.GoFiles[i]`.
4. The refined `isLiteralDurationExpr` predicate must reject `"0"` via `allLiteralOrLitProduct` — zero sentinel.
5. Exclusion list must include `/storetest/` and `/healthtest/` alongside `/locktest/` and `/outboxtest/`.
6. `drainDeadline` var → `const defaultRMQDrainDeadline` + `testOnlyDrainDeadlineOverride` + `currentDrainDeadline()` is the canonical test-injection pattern for package-level timing vars.

**Why:** PR-CI-6 plan explicitly specified packages.Load+go/types but original implementation used pure AST.
**How to apply:** Follow this pattern for any future archtest scanner that needs to detect AST patterns in production code — always use packages.Visit + visited map, not filepath.Walk.
