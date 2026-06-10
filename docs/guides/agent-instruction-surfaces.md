# Agent Instruction Surfaces

GoCell keeps AI-facing instructions short and mechanically guarded.
Instruction files are prompt context, not history databases or enforcement inventories.

## Ownership Matrix

| Content | Owner | Not Owner |
|---------|-------|-----------|
| Stable repo workflow, layout, commands, verification entrypoints | `AGENTS.md` / `CLAUDE.md` | Topic rule files |
| One-topic, future-facing behavior rules | `.claude/rules/gocell/*.md` | ADR text, issue plans, changelog |
| Multi-step task workflows and command choreography | `.claude/skills/` / `.codex/skills/` | Always-loaded repo guidance |
| Enforcement IDs, symbol sets, blind spots, Hard/Medium proof | `tools/archtest/*` package godoc and tests | Rules markdown |
| Design decisions, tradeoffs, threat models, amendments | `docs/architecture/*.md` | Rules markdown |
| Operational runbooks, alerts, dashboards, failure handling | `docs/ops/*.md` | Rules markdown |
| Future work, deferred work, issue state, prioritization | GitHub Issues + Project | Rules markdown or ADR |

## Rules Files

Every `.claude/rules/gocell/*.md` file must:

- start with a Markdown H1 heading;
- stay concise enough to scan in prompt context;
- describe future behavior constraints, not how the rule evolved;
- link to ADR, archtest godoc, docs, or GitHub Issues for detail;
- avoid long proof tables, review records, implementation logs, and deferred-work lists.

Rules may mention an enforcement ID, but the ID's proof, blind spots, exact symbols,
fixtures, and upgrade path live beside the enforcement code.

## Adding A Rule

1. Pick the narrowest topic.
2. Write the behavior in present or future tense.
3. Add only the minimum source links needed to find the authority.
4. If the rule must block regressions, add an archtest, governance rule, codegen
   funnel, or verify gate in the same change.
5. Run `bash hack/verify-rules-governance.sh`.

If the explanation needs more than a short paragraph, put it in an ADR, guide,
runbook, or package godoc and link to it.

## Mechanical Guard

`AGENT-RULES-GOVERNANCE-01` enforces the Markdown shape for GoCell rules:

- AI-robust rating: Medium — CI-blocking text-shape gate with synthetic red
  cases; prose semantics cannot be type-system Hard.
- bounded file size, line count, and line length;
- no history-style markers in rules;
- synthetic red cases plus production anti-vacuity.

The guard blocks poor shape, not legitimate new rules. A new rule passes when it
is concise and future-facing.
