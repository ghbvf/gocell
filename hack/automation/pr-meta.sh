#!/usr/bin/env bash
# pr-meta.sh — produce and consume the gocell-pr-meta:v1 machine block that
# rides in a hidden HTML comment after the human footer of every pm:ship /
# pm:fix / pm:pr-review / pm:ci / pm:oos PR comment.
#
# Wire form (single line, appended after the visible footer):
#   <!-- gocell-pr-meta:v1 <standard-base64(JSON)> -->
#
# Why standard base64 (not base64url): CommonMark forbids `--` inside an HTML
# comment body — a `--` demotes the whole block to *visible* text. The standard
# base64 alphabet (A-Za-z0-9+/=) never contains `-`, so `--`/`-->` is structurally
# impossible in the payload. base64url's `-` could emit `--`; we deliberately
# diverge from issue #1660's literal "base64url" for this reason.
#
# This helper is the single source for the state machine. Producers (ship/fix/
# pr-review skills + codex-pr-router) call `emit-block --kind=K --pr=N` with only
# the irreducible facts; the engine derives EVERYTHING else so no producer
# hand-encodes the mapping:
#   - derive_facts(): kind -> {phase, verdict, round} (the single mapping source,
#     selftest-locked). phase/fixed-verdict by kind; ci verdict from failedChecks;
#     round ship=0 / fix=roundBase+1 / pr-review,ci,oos=roundBase (carry).
#   - derive(): `schema`, `cycle.exhausted`, `next`, `idempotencyKey`, and rejects
#     any incoherent (kind,phase,verdict).
# The bash wrapper auto-fetches refs (gh pr view) + roundBase (`round`) + env
# session/worktree unless overridden. The 3-round circuit breaker is enforced
# here — when a changes-requested round is exhausted (round >= maxRounds),
# `next.agent` is forced to `human` so the #935/#1657 daemons stop dispatching
# and escalate.
#
# Subcommands:
#   emit-block --kind=K --pr=N [flags]  derive facts -> stdout block line     (online)
#   decode            stdin markdown/block -> stdout validated JSON           (offline)
#   extract <PR#>     gh-fetched comments -> latest block JSON iff fresh       (online)
#   round   <PR#>     gh-fetched comments -> max cycle.round for this PR       (online)
#   selftest          offline protocol self-test (no network)                 (offline)
#
# Exit codes: 0 ok · 1 gh/IO error · 2 no/invalid block · 3 stale block · 64 usage error
#
# Trust model (layered, fail-safe): `round`/`extract` only read comments that are
# both from a trusted author (author_association OWNER/MEMBER/COLLABORATOR) AND a
# real pm:* protocol comment (carry a <!-- pm:ship|fix|pr-review|ci|oos --> marker) — F3;
# only count/accept blocks whose repo+pr match this PR (cross-PR copy-paste
# ignored); and only accept *canonical* blocks — every derived field (next/
# idempotencyKey/cycle.maxRounds/cycle.exhausted) must equal what emit would
# re-derive from the block's own facts (forgery rejected — F1); maxRounds is a
# sealed constant (F2). `extract` additionally rejects blocks whose headSha !=
# the live PR head (stale). Accepted residual (internal single-tenant repo): a
# trusted member who *intentionally* crafts a pm:* comment can still post a
# fresh canonical block or inflate cycle.round — but both fail *safe* (toward
# human escalation, never auto-merge) and are recoverable by deleting the
# comment. Cryptographic block signing (HMAC / GitHub App identity) would close
# this last gap but is deferred (YAGNI for this repo).
#
# Schema single source: hack/automation/schema/pr-meta.v1.json
#
# ref: kubernetes/kubernetes hack/verify-shellcheck.sh — script shape
#      (set -euo pipefail, REPO_ROOT resolution, ref-attribution comment).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
SCHEMA_FILE="${REPO_ROOT}/hack/automation/schema/pr-meta.v1.json"
REPO_SLUG="ghbvf/gocell"

usage() {
    cat >&2 <<'EOF'
usage: pr-meta.sh <emit-block|decode|extract|round|selftest> [args]
  emit-block --kind=K --pr=N [flags]   build + print the gocell-pr-meta:v1 block
                  derives phase/verdict/round/refs/session/worktree from --kind +
                  --pr; flags: --tool --phase --verdict --findings --ci --oos and
                  overrides --head-sha --base-ref --head-ref --round-base
                  --session --worktree (router supplies its gated values)
  decode          read markdown/block on stdin, print validated JSON
  extract <PR#>   fetch PR comments, print the latest block JSON iff fresh
  round   <PR#>   fetch PR comments, print max cycle.round for this PR (0 if none)
  selftest        offline protocol self-test (no network required)
exit codes: 0 ok | 1 gh/IO error | 2 no/invalid block | 3 stale block | 64 usage error
EOF
}

# ---- shared python engine ---------------------------------------------------
#
# The engine is written to a temp file once per run and invoked as
# `python3 <file> <mode> ...`, leaving stdin free for the piped JSON/markdown.
# (A `python3 - <<'PY'` heredoc would make the heredoc itself python's stdin;
# the temp-file form also avoids any /dev/fd portability assumption.)

PRMETA_ENGINE=""
cleanup_engine() {
    if [[ -n "${PRMETA_ENGINE}" && -f "${PRMETA_ENGINE}" ]]; then
        rm -f "${PRMETA_ENGINE}"
    fi
}
trap cleanup_engine EXIT

ensure_engine() {
    if [[ -n "${PRMETA_ENGINE}" ]]; then
        return 0
    fi
    PRMETA_ENGINE="$(mktemp "${TMPDIR:-/tmp}/pr-meta-engine.XXXXXX")"
    cat > "${PRMETA_ENGINE}" <<'PY'
import sys, json, base64, re

SCHEMA_CONST = "gocell-pr-meta/v1"
MARKER = "gocell-pr-meta:v1"
BLOCK_RE = re.compile(r"<!--\s*gocell-pr-meta:v1\s+([A-Za-z0-9+/=]+)\s*-->")
MAX_ROUNDS = 3  # sealed circuit-breaker ceiling; producer facts cannot raise it
DERIVED_KEYS = ("schema", "next", "idempotencyKey")  # cycle.maxRounds/exhausted also derived
ZERO_FINDINGS = {
    "total": 0, "fixed": 0, "unresolved": 0, "blocking": 0,
    "byP": {"p0": 0, "p1": 0, "p2": 0, "p3": 0},
    "byCx": {"cx1": 0, "cx2": 0, "cx3": 0, "cx4": 0},
}

# Coherent (kind, phase, verdict) triples. Producers report facts; an incoherent
# triple (e.g. kind=fix with verdict=approved) would route derive_next down the
# wrong branch, so emit fails closed on anything outside this set.
COHERENT = {
    ("ship", "ship", "needs-review-again"),
    ("fix", "fix", "needs-check-fix"),
    ("pr-review", "review", "approved"),
    ("pr-review", "review", "changes-requested"),
    ("pr-review", "check", "ready"),
    ("pr-review", "check", "changes-requested"),
    ("ci", "check", "ci-failed"),
    ("ci", "check", "ci-green"),
    ("oos", "review", "oos-filed"),
}


def type_ok(obj, t):
    for tt in (t if isinstance(t, list) else [t]):
        if tt == "object" and isinstance(obj, dict):
            return True
        if tt == "array" and isinstance(obj, list):
            return True
        if tt == "string" and isinstance(obj, str):
            return True
        if tt == "integer" and isinstance(obj, int) and not isinstance(obj, bool):
            return True
        if tt == "number" and isinstance(obj, (int, float)) and not isinstance(obj, bool):
            return True
        if tt == "boolean" and isinstance(obj, bool):
            return True
        if tt == "null" and obj is None:
            return True
    return False


# validate is a bounded recursive validator covering exactly the JSON Schema
# keywords pr-meta.v1.json uses (type/const/enum/pattern/minLength/minimum/
# required/properties/additionalProperties). The schema file stays the single
# source of truth — this walker reads it, it does not hard-code field lists.
# Array items are validated recursively when the schema has type:array + items.
def validate(obj, schema, path="$"):
    errs = []
    if "const" in schema:
        if obj != schema["const"]:
            errs.append("%s: expected const %r, got %r" % (path, schema["const"], obj))
        return errs
    if "enum" in schema:
        if obj not in schema["enum"]:
            errs.append("%s: %r not in enum %r" % (path, obj, schema["enum"]))
        return errs
    t = schema.get("type")
    if t is not None and not type_ok(obj, t):
        errs.append("%s: expected type %r, got %s" % (path, t, type(obj).__name__))
        return errs
    if isinstance(obj, dict):
        props = schema.get("properties", {})
        for req in schema.get("required", []):
            if req not in obj:
                errs.append("%s: missing required key %r" % (path, req))
        if schema.get("additionalProperties", True) is False:
            for k in obj:
                if k not in props:
                    errs.append("%s: additional property %r not allowed" % (path, k))
        for k, v in obj.items():
            if k in props:
                errs += validate(v, props[k], "%s.%s" % (path, k))
    if isinstance(obj, list):
        items_schema = schema.get("items")
        if items_schema is not None:
            for i, elem in enumerate(obj):
                errs += validate(elem, items_schema, "%s[%d]" % (path, i))
    if isinstance(obj, str):
        if "pattern" in schema and not re.search(schema["pattern"], obj):
            errs.append("%s: %r does not match pattern %s" % (path, obj, schema["pattern"]))
        if "minLength" in schema and len(obj) < schema["minLength"]:
            errs.append("%s: shorter than minLength %d" % (path, schema["minLength"]))
    if isinstance(obj, int) and not isinstance(obj, bool):
        if "minimum" in schema and obj < schema["minimum"]:
            errs.append("%s: %d < minimum %d" % (path, obj, schema["minimum"]))
    return errs


def derive_next(verdict, exhausted):
    if verdict == "needs-review-again":
        return {"agent": "codex", "command": "codex review", "sandbox": True,
                "triggerLabel": "pr-status/needs-review-again", "requiresSameHeadSha": True}
    if verdict == "needs-check-fix":
        return {"agent": "claude", "command": "/pr-review --check", "sandbox": True,
                "triggerLabel": "pr-status/needs-check-fix", "requiresSameHeadSha": True}
    if verdict == "changes-requested":
        if exhausted:
            # Circuit breaker: 3 review<->fix rounds exhausted -> stop the loop,
            # escalate to a human. Daemons must not auto-dispatch on this.
            return {"agent": "human", "command": None, "sandbox": False,
                    "triggerLabel": None, "requiresSameHeadSha": False}
        # 5-state: non-exhausted changes-requested triggers pr-status/needs-fix
        return {"agent": "claude", "command": "/fix", "sandbox": True,
                "triggerLabel": "pr-status/needs-fix", "requiresSameHeadSha": True}
    if verdict in ("approved", "ready"):
        return {"agent": None, "command": None, "sandbox": False,
                "triggerLabel": None, "requiresSameHeadSha": False}
    if verdict == "ci-green":
        return {"agent": None, "command": None, "sandbox": False,
                "triggerLabel": None, "requiresSameHeadSha": False}
    if verdict == "ci-failed":
        # CI fix is handled inline by ship/fix's own 3-round loop, so a posted
        # pm:ci is terminal: green or exhausted->human — NOT auto-/fix.
        return {"agent": "human", "command": None, "sandbox": False,
                "triggerLabel": None, "requiresSameHeadSha": False}
    if verdict == "oos-filed":
        # Drafts staged for human `gh issue create`; no auto-filer.
        return {"agent": "human", "command": None, "sandbox": False,
                "triggerLabel": None, "requiresSameHeadSha": False}
    raise ValueError("unknown verdict %r" % verdict)


def validate_kind_facts(obj):
    kind = obj.get("kind")
    if kind in ("ship", "fix", "pr-review"):
        if not isinstance(obj.get("findings"), dict):
            raise ValueError("kind %s requires explicit findings facts" % kind)
    elif kind == "ci":
        ci = obj.get("ci")
        if not isinstance(ci, dict):
            raise ValueError("kind ci requires explicit ci facts")
        if "failedChecks" not in ci:
            raise ValueError("kind ci requires ci.failedChecks facts")
        if "passedChecks" not in ci:
            raise ValueError("kind ci requires ci.passedChecks facts")
        if "totalChecks" not in ci:
            raise ValueError("kind ci requires ci.totalChecks facts")
        if not isinstance(ci.get("failedChecks"), list):
            raise ValueError("kind ci requires ci.failedChecks to be an array")
    elif kind == "oos":
        oos = obj.get("oos")
        if not isinstance(oos, dict):
            raise ValueError("kind oos requires explicit oos facts")
        items = oos.get("items")
        if not isinstance(items, list) or not items:
            raise ValueError("kind oos requires non-empty oos.items facts")
    if kind != "ci" and obj.get("ci") is not None:
        raise ValueError("kind %s must not carry ci facts" % kind)
    if kind != "oos" and obj.get("oos") is not None:
        raise ValueError("kind %s must not carry oos facts" % kind)


def derive(facts):
    obj = dict(facts)
    triple = (obj.get("kind"), obj.get("phase"), obj.get("verdict"))
    if triple not in COHERENT:
        raise ValueError("incoherent (kind,phase,verdict)=%r" % (triple,))
    validate_kind_facts(obj)
    obj["schema"] = SCHEMA_CONST
    rnd = (obj.get("cycle") or {}).get("round")
    if rnd is None:
        raise ValueError("cycle.round is required in input facts")
    # cycle is rebuilt from round alone: maxRounds is the sealed MAX_ROUNDS
    # constant (producer facts cannot raise the breaker ceiling, F2) and
    # exhausted is always recomputed.
    exhausted = bool(rnd >= MAX_ROUNDS)
    obj["cycle"] = {"round": rnd, "maxRounds": MAX_ROUNDS, "exhausted": exhausted}
    obj["next"] = derive_next(obj["verdict"], exhausted)
    obj.setdefault("session", None)
    obj.setdefault("worktree", None)
    # ci/oos are facts (not derived). After validate_kind_facts has checked the
    # owning kind's presence contract, set defaults for canonical key equality.
    obj.setdefault("ci", None)
    obj.setdefault("oos", None)
    obj["idempotencyKey"] = "%s#%s@%s:%s/%s#%s" % (
        obj.get("repo"), obj.get("pr"), obj.get("headSha"),
        obj.get("kind"), obj.get("phase"), rnd)
    return obj


# PHASE_BY_KIND / FIXED_VERDICT_BY_KIND are the single source of the
# kind->{phase,verdict} mapping that producers (ship/fix/pr-review skills +
# codex-pr-router) used to hand-encode. derive_facts() below derives phase /
# verdict / cycle.round from these, so no producer re-states the mapping.
PHASE_BY_KIND = {"ship": "ship", "fix": "fix", "ci": "check", "oos": "review"}
FIXED_VERDICT_BY_KIND = {
    "ship": "needs-review-again",
    "fix": "needs-check-fix",
    "oos": "oos-filed",
}


def derive_facts(minimal):
    """Map minimal producer facts -> full facts (phase/verdict/cycle.round filled).

    Single source of the kind->{phase,verdict,round} mapping (selftest-locked).
    Producers supply only the irreducible facts; the mapping lives here once so
    neither the skills nor the codex-pr-router hand-encode it:

      phase:   ship->ship, fix->fix, ci->check, oos->review,
               pr-review->minimal["phase"] (review|check, a genuine mode choice)
      verdict: ship/fix/oos fixed by kind; ci-> ci-green if ci.failedChecks empty
               else ci-failed; pr-review-> minimal["verdict"] (a review judgment)
      round:   ship->0; fix->roundBase+1; pr-review/ci/oos->roundBase (carry the
               round of the comment cycle they ride — fixes the prior fix-path
               pm:ci R+2 drift)

    `roundBase` is the PR's current max cycle.round (== `pr-meta.sh round <PR>`),
    consumed here and NOT written to the wire block. The derived triple is then
    validated against COHERENT by derive().
    """
    kind = minimal.get("kind")
    round_base = minimal.get("roundBase")
    if round_base is None:
        raise ValueError("derive_facts: roundBase is required")
    if not isinstance(round_base, int) or isinstance(round_base, bool):
        raise ValueError("derive_facts: roundBase must be an integer")
    f = {k: v for k, v in minimal.items() if k != "roundBase"}

    # phase
    if kind == "pr-review":
        phase = minimal.get("phase")
        if phase not in ("review", "check"):
            raise ValueError(
                "derive_facts: pr-review requires phase in {review,check}, got %r" % (phase,))
    else:
        phase = PHASE_BY_KIND.get(kind)
        if phase is None:
            raise ValueError("derive_facts: unknown kind %r" % (kind,))
    f["phase"] = phase

    if kind in ("ship", "fix", "pr-review"):
        if not isinstance(minimal.get("findings"), dict):
            raise ValueError("derive_facts: kind %s requires explicit findings facts" % kind)
    if kind == "ci":
        ci = minimal.get("ci")
        if not isinstance(ci, dict):
            raise ValueError("derive_facts: kind ci requires explicit ci facts")
        if "failedChecks" not in ci:
            raise ValueError("derive_facts: kind ci requires ci.failedChecks facts")
        if "passedChecks" not in ci:
            raise ValueError("derive_facts: kind ci requires ci.passedChecks facts")
        if "totalChecks" not in ci:
            raise ValueError("derive_facts: kind ci requires ci.totalChecks facts")
        if not isinstance(ci.get("failedChecks"), list):
            raise ValueError("derive_facts: kind ci requires ci.failedChecks to be an array")
    if kind == "oos":
        oos = minimal.get("oos")
        if not isinstance(oos, dict):
            raise ValueError("derive_facts: kind oos requires explicit oos facts")
        items = oos.get("items")
        if not isinstance(items, list) or not items:
            raise ValueError("derive_facts: kind oos requires non-empty oos.items facts")

    # verdict
    if kind in FIXED_VERDICT_BY_KIND:
        verdict = FIXED_VERDICT_BY_KIND[kind]
    elif kind == "ci":
        failed = minimal["ci"]["failedChecks"]
        verdict = "ci-failed" if failed else "ci-green"
    else:  # pr-review
        verdict = minimal.get("verdict")
        if verdict is None:
            raise ValueError("derive_facts: pr-review requires an explicit verdict")
    f["verdict"] = verdict

    # round
    if kind == "ship":
        rnd = 0
    elif kind == "fix":
        rnd = round_base + 1
    else:  # pr-review / ci / oos carry the round of the comment cycle they ride
        rnd = round_base
    f["cycle"] = {"round": rnd}

    # ci/oos carry no review counts; all review-bearing kinds must supply them.
    f.setdefault("findings", ZERO_FINDINGS)
    return f


def canon(obj):
    return json.dumps(obj, separators=(",", ":"), sort_keys=True)


def extract_payloads(blob):
    return BLOCK_RE.findall(blob)


def facts_of(obj):
    # Strip every emit-derived field so the block can be re-derived and compared
    # against itself. cycle keeps only round (maxRounds/exhausted are derived).
    # ci/oos are facts (not derived) so they survive naturally.
    f = {k: v for k, v in obj.items() if k not in DERIVED_KEYS}
    f["cycle"] = {"round": (obj.get("cycle") or {}).get("round")}
    return f


def decode_payload(payload, schema):
    raw = base64.b64decode(payload, validate=True)
    obj = json.loads(raw)
    errs = validate(obj, schema)
    if errs:
        raise ValueError("block fails schema:\n  " + "\n  ".join(errs))
    # Canonical check (F1): accept a block only if it equals what emit would
    # derive from its own facts. Rejects hand-forged next / idempotencyKey /
    # cycle.maxRounds / cycle.exhausted even when the block is schema-valid.
    if canon(derive(facts_of(obj))) != canon(obj):
        raise ValueError("block is not canonical (derived fields forged or inconsistent)")
    return obj


def valid_blocks(blob, schema):
    out = []
    for payload in extract_payloads(blob):
        try:
            out.append(decode_payload(payload, schema))
        except Exception:
            continue  # skip malformed / foreign blocks
    return out


def _encode_block(obj, schema, ctx):
    errs = validate(obj, schema)
    if errs:
        sys.stderr.write("pr-meta %s: derived object fails schema:\n  " % ctx + "\n  ".join(errs) + "\n")
        sys.exit(1)
    payload = base64.b64encode(canon(obj).encode("utf-8")).decode("ascii")
    if "--" in payload:  # impossible for standard base64; fail-closed guard
        sys.stderr.write("pr-meta %s: base64 payload contains '--' (HTML-comment unsafe)\n" % ctx)
        sys.exit(1)
    sys.stdout.write("<!-- %s %s -->\n" % (MARKER, payload))


def do_emitblock(schema):
    # Minimal producer facts on stdin -> derive_facts (kind->phase/verdict/round
    # mapping) -> derive (schema/cycle/next/idempotencyKey + COHERENT check) ->
    # block line. The only producer-facing emit path; raw full-fact emit is gone.
    try:
        minimal = json.load(sys.stdin)
        obj = derive(derive_facts(minimal))
    except Exception as e:
        sys.stderr.write("pr-meta emit-block: %s\n" % e)
        sys.exit(1)
    _encode_block(obj, schema, "emit-block")


def do_decode(schema):
    blocks = valid_blocks(sys.stdin.read(), schema)
    if not blocks:
        sys.stderr.write("pr-meta decode: no valid gocell-pr-meta:v1 block found\n")
        sys.exit(2)
    sys.stdout.write(canon(blocks[-1]) + "\n")  # latest block wins


def do_extract(schema, live_repo, live_pr, live_sha):
    blocks = valid_blocks(sys.stdin.read(), schema)
    blocks = [b for b in blocks if b.get("repo") == live_repo and str(b.get("pr")) == str(live_pr)]
    if not blocks:
        sys.stderr.write("pr-meta extract: no valid block for %s#%s\n" % (live_repo, live_pr))
        sys.exit(2)
    obj = blocks[-1]
    if obj.get("headSha") != live_sha:
        sys.stderr.write("pr-meta extract: stale block (headSha=%s vs live %s)\n"
                         % (obj.get("headSha"), live_sha))
        sys.exit(3)
    sys.stdout.write(canon(obj) + "\n")


def do_maxround(schema, live_repo, live_pr):
    best = 0
    for b in valid_blocks(sys.stdin.read(), schema):
        if b.get("repo") != live_repo or str(b.get("pr")) != str(live_pr):
            continue  # ignore cross-PR contamination
        try:
            r = int(b["cycle"]["round"])
        except Exception:
            continue
        if r > best:
            best = r
    sys.stdout.write("%d\n" % best)


# ---------------------------------------------------------------------------
# Selftest helpers
# ---------------------------------------------------------------------------

def _make_facts(kind, phase, verdict, rnd=1, **extra):
    """Build a minimal facts dict for the given triple."""
    f = {
        "repo": "ghbvf/gocell",
        "pr": 42,
        "kind": kind,
        "phase": phase,
        "tool": "claude-code",
        "baseRef": "develop",
        "headRef": "feature/test",
        "headSha": "a" * 40,
        "verdict": verdict,
        "findings": ZERO_FINDINGS,
        "cycle": {"round": rnd},
    }
    f.update(extra)
    return f


def _emit_decode(facts, schema):
    """emit facts -> block line -> decode -> return obj."""
    obj = derive(facts)
    errs = validate(obj, schema)
    if errs:
        raise AssertionError("emit produced invalid object: %s" % errs)
    payload = base64.b64encode(canon(obj).encode("utf-8")).decode("ascii")
    block_line = "<!-- %s %s -->" % (MARKER, payload)
    decoded_list = valid_blocks(block_line, schema)
    if not decoded_list:
        raise AssertionError("decode returned no blocks")
    return decoded_list[0]


def do_selftest(schema):
    # F7: explicit expected-check count so a silently-dropped check fails the
    # selftest rather than printing "OK (N checks)" with a lower-than-expected N.
    # Update this constant whenever a check is added or removed. Breakdown:
    #   9 round-trip + 2 five-state + 4 schema-reject + 4 forgery + 1 incoherent
    #   + 1 oos-array + 1 ci-array + 2 exhausted + 22 emitblock-derive
    #   + 5 kind-coverage + 9 kind-facts-contract = 60
    EXPECTED_CHECKS = 60

    checks = 0
    failures = []

    def ok(name):
        nonlocal checks
        checks += 1

    def fail(name, msg):
        failures.append("FAIL [%s]: %s" % (name, msg))

    def assert_eq(name, got, want):
        nonlocal checks
        checks += 1
        if got != want:
            failures.append("FAIL [%s]: got %r, want %r" % (name, got, want))

    def assert_raises(name, fn):
        nonlocal checks
        checks += 1
        try:
            fn()
            failures.append("FAIL [%s]: expected exception but none raised" % name)
        except Exception:
            pass  # expected

    def assert_decode_fails(name, block_line):
        nonlocal checks
        checks += 1
        result = valid_blocks(block_line, schema)
        if result:
            failures.append("FAIL [%s]: expected decode failure but got: %s" % (name, result))

    # ------------------------------------------------------------------
    # 1. Round-trip every kind
    # ------------------------------------------------------------------

    kinds = [
        ("ship",      "ship",   "needs-review-again"),
        ("fix",       "fix",    "needs-check-fix"),
        ("pr-review", "review", "approved"),
        ("pr-review", "review", "changes-requested"),  # non-exhausted, round=1
        ("pr-review", "check",  "ready"),
        ("pr-review", "check",  "changes-requested"),  # non-exhausted, round=1
        ("ci",        "check",  "ci-failed"),
        ("ci",        "check",  "ci-green"),
        ("oos",       "review", "oos-filed"),
    ]
    for kind, phase, verdict in kinds:
        name = "round-trip/%s/%s/%s" % (kind, phase, verdict)
        try:
            extra = {}
            if kind == "ci":
                failed = [] if verdict == "ci-green" else [{"name": "x", "url": "u"}]
                extra["ci"] = {"failedChecks": failed, "passedChecks": 1, "totalChecks": 2}
            if kind == "oos":
                extra["oos"] = {"items": [
                    {
                        "fileLine": "path/to/file.go:1",
                        "rootCause": {"code": "c", "arch": "a", "history": "h"},
                        "solutionSeeds": {"minimal": "m", "thorough": "t", "refactor": "r"},
                    }
                ]}
            facts = _make_facts(kind, phase, verdict, rnd=1, **extra)
            decoded = _emit_decode(facts, schema)
            expected = derive(facts_of(decoded))
            if canon(decoded) != canon(expected):
                failures.append("FAIL [%s]: decoded != re-derived" % name)
            else:
                ok(name)
        except Exception as e:
            failures.append("FAIL [%s]: %s" % (name, e))

    # ------------------------------------------------------------------
    # 2. 5-state assertion: changes-requested non-exhausted -> needs-fix
    # ------------------------------------------------------------------

    name = "5-state/pr-review/review/changes-requested"
    try:
        facts = _make_facts("pr-review", "review", "changes-requested", rnd=1)
        decoded = _emit_decode(facts, schema)
        got_label = decoded["next"]["triggerLabel"]
        assert_eq(name, got_label, "pr-status/needs-fix")
    except Exception as e:
        failures.append("FAIL [%s]: %s" % (name, e))

    name = "5-state/pr-review/check/changes-requested"
    try:
        facts = _make_facts("pr-review", "check", "changes-requested", rnd=1)
        decoded = _emit_decode(facts, schema)
        got_label = decoded["next"]["triggerLabel"]
        assert_eq(name, got_label, "pr-status/needs-fix")
    except Exception as e:
        failures.append("FAIL [%s]: %s" % (name, e))

    # ------------------------------------------------------------------
    # 3. Schema rejection
    # ------------------------------------------------------------------

    # 3a. Missing required key (no "verdict")
    name = "schema-reject/missing-required-key"
    try:
        facts = _make_facts("ship", "ship", "needs-review-again")
        obj = derive(facts)
        del obj["verdict"]
        payload = base64.b64encode(canon(obj).encode("utf-8")).decode("ascii")
        block_line = "<!-- %s %s -->" % (MARKER, payload)
        assert_decode_fails(name, block_line)
    except Exception as e:
        failures.append("FAIL [%s]: %s" % (name, e))

    # 3b. Extra key (additionalProperties violation)
    name = "schema-reject/extra-key"
    try:
        facts = _make_facts("ship", "ship", "needs-review-again")
        obj = derive(facts)
        obj["__extraKey__"] = "forbidden"
        payload = base64.b64encode(canon(obj).encode("utf-8")).decode("ascii")
        block_line = "<!-- %s %s -->" % (MARKER, payload)
        assert_decode_fails(name, block_line)
    except Exception as e:
        failures.append("FAIL [%s]: %s" % (name, e))

    # 3c. Bad enum value for kind
    name = "schema-reject/bad-enum-kind"
    try:
        facts = _make_facts("ship", "ship", "needs-review-again")
        obj = derive(facts)
        obj["kind"] = "bogus-kind"
        payload = base64.b64encode(canon(obj).encode("utf-8")).decode("ascii")
        block_line = "<!-- %s %s -->" % (MARKER, payload)
        assert_decode_fails(name, block_line)
    except Exception as e:
        failures.append("FAIL [%s]: %s" % (name, e))

    # 3d. Bad headSha (not 40 hex)
    name = "schema-reject/bad-headsha"
    try:
        facts = _make_facts("ship", "ship", "needs-review-again")
        obj = derive(facts)
        obj["headSha"] = "notahexsha"
        # idempotencyKey also references headSha — just rebuild it to keep
        # the test focused on the headSha pattern check
        obj["idempotencyKey"] = "ghbvf/gocell#42@notahexsha:ship/ship#1"
        payload = base64.b64encode(canon(obj).encode("utf-8")).decode("ascii")
        block_line = "<!-- %s %s -->" % (MARKER, payload)
        assert_decode_fails(name, block_line)
    except Exception as e:
        failures.append("FAIL [%s]: %s" % (name, e))

    # ------------------------------------------------------------------
    # 4. Canonical-forgery rejection
    # ------------------------------------------------------------------

    def _mutate_and_check(name, field_path, new_value):
        """Take a valid block, mutate a field, assert decode fails."""
        try:
            facts = _make_facts("ship", "ship", "needs-review-again")
            obj = derive(facts)
            # Navigate and set field_path (dot-notation)
            parts = field_path.split(".")
            target = obj
            for p in parts[:-1]:
                target = target[p]
            target[parts[-1]] = new_value
            payload = base64.b64encode(canon(obj).encode("utf-8")).decode("ascii")
            block_line = "<!-- %s %s -->" % (MARKER, payload)
            result = valid_blocks(block_line, schema)
            nonlocal checks
            checks += 1
            if result:
                failures.append("FAIL [%s]: expected decode failure but got valid block" % name)
        except Exception as e:
            failures.append("FAIL [%s]: %s" % (name, e))

    _mutate_and_check("forgery/next.agent",         "next.agent",         "human")
    _mutate_and_check("forgery/idempotencyKey",      "idempotencyKey",     "forged-key")
    _mutate_and_check("forgery/cycle.exhausted",     "cycle.exhausted",    True)
    _mutate_and_check("forgery/cycle.maxRounds",     "cycle.maxRounds",    99)

    # ------------------------------------------------------------------
    # 5. Incoherent triple rejection
    # ------------------------------------------------------------------

    name = "incoherent/fix+approved"
    assert_raises(name, lambda: derive(_make_facts("fix", "fix", "approved")))

    # ------------------------------------------------------------------
    # 6. Array-items validation: oos block whose items[0] missing required key
    # ------------------------------------------------------------------

    name = "array-items/oos-missing-fileLine"
    try:
        facts = _make_facts("oos", "review", "oos-filed")
        # Add a valid oos object but with items[0] missing the required "fileLine"
        facts["oos"] = {
            "items": [
                {
                    # "fileLine" is required but intentionally omitted
                    "rootCause": {"code": "c", "arch": "a", "history": "h"},
                    "solutionSeeds": {"minimal": "m", "thorough": "t", "refactor": "r"},
                }
            ]
        }
        obj = derive(facts)
        payload = base64.b64encode(canon(obj).encode("utf-8")).decode("ascii")
        block_line = "<!-- %s %s -->" % (MARKER, payload)
        result = valid_blocks(block_line, schema)
        checks += 1
        if result:
            failures.append("FAIL [%s]: expected decode failure (missing fileLine) but got valid block" % name)
    except Exception as e:
        failures.append("FAIL [%s]: %s" % (name, e))

    # ------------------------------------------------------------------
    # 7. Array-items validation: ci block whose failedChecks[0] missing required key
    # F8: the validate() array-items recursion's ci branch was previously untested.
    # ------------------------------------------------------------------

    name = "array-items/ci-failedChecks-missing-name"
    try:
        # ci kind needs a coherent triple: kind=ci, phase=check, verdict=ci-failed
        facts = _make_facts("ci", "check", "ci-failed",
                            ci={"failedChecks": [{"link": "https://example.com/run/1"}],
                                "passedChecks": 0, "totalChecks": 1})
        # "name" is required in each failedChecks item per the schema; omitting it
        # must cause decode to reject the block.
        obj = derive(facts)
        payload = base64.b64encode(canon(obj).encode("utf-8")).decode("ascii")
        block_line = "<!-- %s %s -->" % (MARKER, payload)
        assert_decode_fails(name, block_line)
    except Exception as e:
        failures.append("FAIL [%s]: %s" % (name, e))

    # ------------------------------------------------------------------
    # 8. Exhausted branch: rnd=3 forces next.agent=="human"
    # F9: exhausted path was untested; only rnd=1 (non-exhausted) was covered.
    # ------------------------------------------------------------------

    name = "exhausted/pr-review/review/changes-requested"
    try:
        facts = _make_facts("pr-review", "review", "changes-requested", rnd=3)
        decoded = _emit_decode(facts, schema)
        assert_eq(name, decoded["next"]["agent"], "human")
    except Exception as e:
        failures.append("FAIL [%s]: %s" % (name, e))

    name = "exhausted/pr-review/check/changes-requested"
    try:
        facts = _make_facts("pr-review", "check", "changes-requested", rnd=3)
        decoded = _emit_decode(facts, schema)
        assert_eq(name, decoded["next"]["agent"], "human")
    except Exception as e:
        failures.append("FAIL [%s]: %s" % (name, e))

    # ------------------------------------------------------------------
    # 9. emit-block derive_facts: kind -> phase/verdict/round single source
    # F10: locks the mapping that producers (ship/fix/pr-review skills +
    # codex-pr-router) used to hand-encode. Any drift in phase/verdict/round
    # derivation fails here, not silently in a producer.
    # ------------------------------------------------------------------

    def _minimal(kind, round_base, **extra):
        m = {"repo": "ghbvf/gocell", "pr": 42, "kind": kind, "tool": "claude-code",
             "baseRef": "develop", "headRef": "feature/test", "headSha": "a" * 40,
             "roundBase": round_base, "session": None, "worktree": None}
        m.update(extra)
        return m

    # ship: phase=ship, verdict=needs-review-again, round=0 (roundBase ignored)
    try:
        f = derive_facts(_minimal("ship", 5, findings=ZERO_FINDINGS))
        assert_eq("emitblock/ship/phase", f["phase"], "ship")
        assert_eq("emitblock/ship/verdict", f["verdict"], "needs-review-again")
        assert_eq("emitblock/ship/round", f["cycle"]["round"], 0)
    except Exception as e:
        failures.append("FAIL [emitblock/ship]: %s" % e)

    # fix: round = roundBase + 1
    try:
        f = derive_facts(_minimal("fix", 2, findings=ZERO_FINDINGS))
        assert_eq("emitblock/fix/phase", f["phase"], "fix")
        assert_eq("emitblock/fix/verdict", f["verdict"], "needs-check-fix")
        assert_eq("emitblock/fix/round", f["cycle"]["round"], 3)
    except Exception as e:
        failures.append("FAIL [emitblock/fix]: %s" % e)

    # oos: phase=review, verdict=oos-filed, round=carry
    try:
        f = derive_facts(_minimal("oos", 2, oos={"items": [
            {
                "fileLine": "path/to/file.go:1",
                "rootCause": {"code": "c", "arch": "a", "history": "h"},
                "solutionSeeds": {"minimal": "m", "thorough": "t", "refactor": "r"},
            }
        ]}))
        assert_eq("emitblock/oos/phase", f["phase"], "review")
        assert_eq("emitblock/oos/verdict", f["verdict"], "oos-filed")
        assert_eq("emitblock/oos/round", f["cycle"]["round"], 2)
    except Exception as e:
        failures.append("FAIL [emitblock/oos]: %s" % e)

    # ci: verdict from failedChecks; round = carry (NOT +1 — fixes fix-path R+2)
    try:
        f = derive_facts(_minimal(
            "ci", 2, ci={"failedChecks": [], "passedChecks": 5, "totalChecks": 5}))
        assert_eq("emitblock/ci-green/verdict", f["verdict"], "ci-green")
        assert_eq("emitblock/ci-green/phase", f["phase"], "check")
        assert_eq("emitblock/ci-green/round", f["cycle"]["round"], 2)
        f = derive_facts(_minimal(
            "ci", 2, ci={"failedChecks": [{"name": "x", "url": "u"}],
                         "passedChecks": 4, "totalChecks": 5}))
        assert_eq("emitblock/ci-failed/verdict", f["verdict"], "ci-failed")
    except Exception as e:
        failures.append("FAIL [emitblock/ci]: %s" % e)

    # pr-review: phase + verdict from producer (genuine judgment); round = carry
    try:
        f = derive_facts(_minimal(
            "pr-review", 1, phase="review", verdict="changes-requested", findings=ZERO_FINDINGS))
        assert_eq("emitblock/pr-review-review/phase", f["phase"], "review")
        assert_eq("emitblock/pr-review-review/round", f["cycle"]["round"], 1)
        f = derive_facts(_minimal(
            "pr-review", 1, phase="check", verdict="ready", findings=ZERO_FINDINGS))
        assert_eq("emitblock/pr-review-check/phase", f["phase"], "check")
        assert_eq("emitblock/pr-review-check/verdict", f["verdict"], "ready")
    except Exception as e:
        failures.append("FAIL [emitblock/pr-review]: %s" % e)

    # end-to-end: minimal -> derive_facts -> derive -> encode -> decode canonical
    try:
        full = derive(derive_facts(_minimal("ship", 0, findings=ZERO_FINDINGS)))
        payload = base64.b64encode(canon(full).encode("utf-8")).decode("ascii")
        decoded_list = valid_blocks("<!-- %s %s -->" % (MARKER, payload), schema)
        if not decoded_list or canon(decoded_list[0]) != canon(full):
            failures.append("FAIL [emitblock/e2e]: round-trip mismatch")
        else:
            ok("emitblock/e2e")
    except Exception as e:
        failures.append("FAIL [emitblock/e2e]: %s" % e)

    # F4 completeness: derive the kind set from PHASE_BY_KIND (+ pr-review) so a
    # new kind added to the mapping is automatically exercised here — it can't be
    # added silently without a selftest case (the added check also trips
    # EXPECTED_CHECKS, forcing a deliberate update).
    for k in list(PHASE_BY_KIND.keys()) + ["pr-review"]:
        try:
            extra = {}
            if k in ("ship", "fix"):
                extra["findings"] = ZERO_FINDINGS
            if k == "ci":
                extra["ci"] = {"failedChecks": [], "passedChecks": 0, "totalChecks": 0}
            if k == "oos":
                extra["oos"] = {"items": [
                    {
                        "fileLine": "path/to/file.go:1",
                        "rootCause": {"code": "c", "arch": "a", "history": "h"},
                        "solutionSeeds": {"minimal": "m", "thorough": "t", "refactor": "r"},
                    }
                ]}
            if k == "pr-review":
                extra.update({"phase": "review", "verdict": "approved", "findings": ZERO_FINDINGS})
            f = derive_facts(_minimal(k, 0, **extra))
            want_phase = "review" if k == "pr-review" else PHASE_BY_KIND[k]
            assert_eq("emitblock/kind-coverage/%s" % k, f["phase"], want_phase)
        except Exception as e:
            failures.append("FAIL [emitblock/kind-coverage/%s]: %s" % (k, e))

    # derive_facts rejects malformed input (fail-closed)
    assert_raises("emitblock/reject/unknown-kind", lambda: derive_facts(_minimal("bogus", 0)))
    assert_raises("emitblock/reject/no-roundbase", lambda: derive_facts({"kind": "ship"}))
    assert_raises("emitblock/reject/ship-no-findings", lambda: derive_facts(_minimal("ship", 0)))
    assert_raises("emitblock/reject/fix-no-findings", lambda: derive_facts(_minimal("fix", 0)))
    assert_raises("emitblock/reject/prreview-no-findings",
                  lambda: derive_facts(_minimal("pr-review", 0, phase="review", verdict="approved")))
    assert_raises("emitblock/reject/prreview-no-phase",
                  lambda: derive_facts(_minimal("pr-review", 0, findings=ZERO_FINDINGS, verdict="approved")))
    assert_raises("emitblock/reject/prreview-no-verdict",
                  lambda: derive_facts(_minimal("pr-review", 0, findings=ZERO_FINDINGS, phase="review")))
    assert_raises("emitblock/reject/ci-no-ci", lambda: derive_facts(_minimal("ci", 0)))
    assert_raises("emitblock/reject/ci-no-failedchecks",
                  lambda: derive_facts(_minimal("ci", 0, ci={"passedChecks": 0, "totalChecks": 0})))
    assert_raises("emitblock/reject/ci-no-passedchecks",
                  lambda: derive_facts(_minimal("ci", 0, ci={"failedChecks": [], "totalChecks": 0})))
    assert_raises("emitblock/reject/ci-no-totalchecks",
                  lambda: derive_facts(_minimal("ci", 0, ci={"failedChecks": [], "passedChecks": 0})))
    assert_raises("emitblock/reject/oos-no-oos", lambda: derive_facts(_minimal("oos", 0)))
    assert_raises("emitblock/reject/oos-empty-items",
                  lambda: derive_facts(_minimal("oos", 0, oos={"items": []})))

    # ------------------------------------------------------------------
    # Report
    # ------------------------------------------------------------------

    if failures:
        for f in failures:
            sys.stderr.write(f + "\n")
        sys.exit(1)
    # F7: assert the exact number of checks ran — a silently-dropped check
    # still causes the selftest to fail even if failures is empty.
    if checks != EXPECTED_CHECKS:
        sys.stderr.write(
            "FAIL [check-count]: expected %d checks, ran %d\n"
            % (EXPECTED_CHECKS, checks)
        )
        sys.exit(1)
    sys.stdout.write("pr-meta selftest: OK (%d checks)\n" % checks)


def main():
    if len(sys.argv) < 3:
        sys.stderr.write("pr-meta engine: usage: <mode> <schema-path> [args]\n")
        sys.exit(64)
    mode, schema_path = sys.argv[1], sys.argv[2]
    with open(schema_path) as f:
        schema = json.load(f)
    if mode == "emitblock":
        do_emitblock(schema)
    elif mode == "decode":
        do_decode(schema)
    elif mode == "extract":
        do_extract(schema, sys.argv[3], sys.argv[4], sys.argv[5])
    elif mode == "maxround":
        do_maxround(schema, sys.argv[3], sys.argv[4])
    elif mode == "selftest":
        do_selftest(schema)
    else:
        sys.stderr.write("pr-meta engine: unknown mode %r\n" % mode)
        sys.exit(64)


main()
PY
}

py() {
    ensure_engine
    python3 "${PRMETA_ENGINE}" "$@"
}

# normalize_pr strips a leading '#' and asserts a positive integer, matching the
# schema's "pr": {"type":"integer","minimum":1}. Prints the clean number.
normalize_pr() {
    local pr="${1#\#}"
    if ! [[ "${pr}" =~ ^[0-9]+$ ]]; then
        echo "pr-meta: PR# must be a positive integer (got '${1}')" >&2
        return 64
    fi
    printf '%s' "${pr}"
}

# cmd_emit_block is the single producer-facing funnel: ship/fix/pr-review skills
# and the codex-pr-router all call it instead of hand-building the
# kind/phase/verdict/round JSON. It assembles the minimal facts (deriving
# refs/roundBase/session/worktree unless overridden) and pipes them to the
# offline `emitblock` engine mode, which applies derive_facts (the single mapping
# source) + derive (schema/cycle/next/idempotencyKey). The router overrides
# --head-sha/--base-ref/--head-ref/--round-base to preserve its gated values.
cmd_emit_block() {
    local kind="" pr="" tool="claude-code" phase="" verdict=""
    local findings="" ci="" oos=""
    local head_sha="" base_ref="" head_ref="" round_base=""
    local session="__ENV__" worktree="__ENV__"
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --kind=*)       kind="${1#*=}" ;;
            --pr=*)         pr="${1#*=}" ;;
            --tool=*)       tool="${1#*=}" ;;
            --phase=*)      phase="${1#*=}" ;;
            --verdict=*)    verdict="${1#*=}" ;;
            --findings=*)   findings="${1#*=}" ;;
            --ci=*)         ci="${1#*=}" ;;
            --oos=*)        oos="${1#*=}" ;;
            --head-sha=*)   head_sha="${1#*=}" ;;
            --base-ref=*)   base_ref="${1#*=}" ;;
            --head-ref=*)   head_ref="${1#*=}" ;;
            --round-base=*) round_base="${1#*=}" ;;
            --session=*)    session="${1#*=}" ;;
            --worktree=*)   worktree="${1#*=}" ;;
            *) echo "pr-meta emit-block: unknown flag '$1'" >&2; return 64 ;;
        esac
        shift
    done
    [[ -n "${kind}" ]] || { echo "pr-meta emit-block: --kind required" >&2; return 64; }
    pr="$(normalize_pr "${pr}")" || return 64

    # Refs: derive any not explicitly overridden via a single gh pr view.
    if [[ -z "${head_sha}" || -z "${base_ref}" || -z "${head_ref}" ]]; then
        local view
        view="$(gh pr view "${pr}" --repo "${REPO_SLUG}" \
            --json baseRefName,headRefName,headRefOid)" \
            || { echo "pr-meta emit-block: gh pr view failed" >&2; return 1; }
        [[ -n "${base_ref}" ]] || base_ref="$(printf '%s' "${view}" | jq -r '.baseRefName')"
        [[ -n "${head_ref}" ]] || head_ref="$(printf '%s' "${view}" | jq -r '.headRefName')"
        [[ -n "${head_sha}" ]] || head_sha="$(printf '%s' "${view}" | jq -r '.headRefOid')"
    fi

    # roundBase: derive via `round <pr>` unless overridden.
    if [[ -z "${round_base}" ]]; then
        round_base="$(cmd_round "${pr}")" \
            || { echo "pr-meta emit-block: round lookup failed" >&2; return 1; }
    fi

    # session/worktree: __ENV__ sentinel => derive from env; an explicit flag
    # (including empty, used by the router) passes through, with empty => null.
    if [[ "${session}" == "__ENV__" ]]; then session="${CLAUDE_CODE_SESSION_ID:-}"; fi
    if [[ "${worktree}" == "__ENV__" ]]; then
        worktree="$(git rev-parse --show-toplevel 2>/dev/null || true)"
    fi

    local minimal
    minimal="$(jq -nc \
        --arg kind "${kind}" \
        --argjson pr "${pr}" \
        --arg repo "${REPO_SLUG}" \
        --arg baseRef "${base_ref}" \
        --arg headRef "${head_ref}" \
        --arg headSha "${head_sha}" \
        --arg tool "${tool}" \
        --argjson roundBase "${round_base}" \
        --arg phase "${phase}" \
        --arg verdict "${verdict}" \
        --arg session "${session}" \
        --arg worktree "${worktree}" \
        --argjson findings "${findings:-null}" \
        --argjson ci "${ci:-null}" \
        --argjson oos "${oos:-null}" \
        '{kind:$kind, pr:$pr, repo:$repo, baseRef:$baseRef, headRef:$headRef,
          headSha:$headSha, tool:$tool, roundBase:$roundBase,
          session:(if $session=="" then null else $session end),
          worktree:(if $worktree=="" then null else $worktree end)}
         + (if $phase=="" then {} else {phase:$phase} end)
         + (if $verdict=="" then {} else {verdict:$verdict} end)
         + (if $findings==null then {} else {findings:$findings} end)
         + (if $ci==null then {} else {ci:$ci} end)
         + (if $oos==null then {} else {oos:$oos} end)')" \
        || { echo "pr-meta emit-block: failed to assemble facts JSON" >&2; return 1; }

    printf '%s' "${minimal}" | py emitblock "${SCHEMA_FILE}"
}

cmd_decode() { py decode "${SCHEMA_FILE}"; }

# fetch_trusted_bodies prints the bodies of PR comments that are BOTH from a
# trusted author (author_association OWNER/MEMBER/COLLABORATOR) AND a real pm:*
# protocol comment (carry a <!-- pm:ship|fix|pr-review|ci|oos --> marker). This
# is the single trust boundary for the dispatch protocol (F3): neither an
# untrusted commenter nor a canonical block pasted into a trusted author's plain
# (non-pm) comment can reach extract/round as a dispatch fact.
fetch_trusted_bodies() {
    local pr="$1"
    gh api "repos/${REPO_SLUG}/issues/${pr}/comments" --paginate \
        --jq '.[] | select((.author_association == "OWNER" or .author_association == "MEMBER" or .author_association == "COLLABORATOR") and (.body | test("<!-- pm:(ship|fix|pr-review|ci|oos) -->"))) | .body'
}

cmd_extract() {
    local pr
    pr="$(normalize_pr "${1:-}")" || return 64
    local bodies live_sha
    bodies="$(fetch_trusted_bodies "${pr}")" \
        || { echo "pr-meta extract: gh api comments failed" >&2; return 1; }
    live_sha="$(gh pr view "${pr}" --repo "${REPO_SLUG}" --json headRefOid --jq .headRefOid)" \
        || { echo "pr-meta extract: gh pr view failed" >&2; return 1; }
    printf '%s\n' "${bodies}" | py extract "${SCHEMA_FILE}" "${REPO_SLUG}" "${pr}" "${live_sha}"
}

cmd_round() {
    local pr
    pr="$(normalize_pr "${1:-}")" || return 64
    local bodies
    bodies="$(fetch_trusted_bodies "${pr}")" \
        || { echo "pr-meta round: gh api comments failed" >&2; return 1; }
    printf '%s\n' "${bodies}" | py maxround "${SCHEMA_FILE}" "${REPO_SLUG}" "${pr}"
}

cmd_selftest() { py selftest "${SCHEMA_FILE}"; }

# ---- dispatch --------------------------------------------------------------

main() {
    local sub="${1:-}"
    if [[ $# -gt 0 ]]; then shift; fi
    case "${sub}" in
        emit-block) cmd_emit_block "$@" ;;
        decode)   cmd_decode "$@" ;;
        extract)  cmd_extract "$@" ;;
        round)    cmd_round "$@" ;;
        selftest) cmd_selftest "$@" ;;
        -h|--help|help) usage; exit 0 ;;
        "") usage; exit 64 ;;
        *) echo "pr-meta: unknown subcommand '${sub}'" >&2; usage; exit 64 ;;
    esac
}

main "$@"
