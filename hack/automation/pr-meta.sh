#!/usr/bin/env bash
# pr-meta.sh — produce and consume the gocell-pr-meta:v1 machine block that
# rides in a hidden HTML comment after the human footer of every pm:ship /
# pm:fix / pm:pr-review PR comment.
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
# This helper is the single source for the state machine: producers emit only
# the *facts* (kind/phase/verdict/refs/findings/cycle.round); `emit` derives
# `schema`, `cycle.exhausted`, `next`, and `idempotencyKey`, and rejects any
# incoherent (kind,phase,verdict). The 3-round circuit breaker is enforced
# here — when a changes-requested round is exhausted (round >= maxRounds),
# `next.agent` is forced to `human` so the #935/#1657 daemons stop dispatching
# and escalate.
#
# Subcommands:
#   emit              stdin JSON facts  -> stdout block line                 (offline)
#   decode            stdin markdown/block -> stdout validated JSON           (offline)
#   extract <PR#>     gh-fetched comments -> latest block JSON iff fresh       (online)
#   round   <PR#>     gh-fetched comments -> max cycle.round for this PR       (online)
#
# Exit codes: 0 ok · 1 gh/IO error · 2 no/invalid block · 3 stale block · 64 usage error
#
# Trust model (layered, fail-safe): `round`/`extract` only read comments that are
# both from a trusted author (author_association OWNER/MEMBER/COLLABORATOR) AND a
# real pm:* protocol comment (carry a <!-- pm:ship|fix|pr-review --> marker) — F3;
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
usage: pr-meta.sh <emit|decode|extract|round> [args]
  emit            read fact JSON on stdin, print the gocell-pr-meta:v1 block
  decode          read markdown/block on stdin, print validated JSON
  extract <PR#>   fetch PR comments, print the latest block JSON iff fresh
  round   <PR#>   fetch PR comments, print max cycle.round for this PR (0 if none)
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
        return {"agent": "claude", "command": "/fix", "sandbox": True,
                "triggerLabel": "pr-review/changes-requested", "requiresSameHeadSha": True}
    if verdict in ("approved", "ready"):
        return {"agent": None, "command": None, "sandbox": False,
                "triggerLabel": None, "requiresSameHeadSha": False}
    raise ValueError("unknown verdict %r" % verdict)


def derive(facts):
    obj = dict(facts)
    triple = (obj.get("kind"), obj.get("phase"), obj.get("verdict"))
    if triple not in COHERENT:
        raise ValueError("incoherent (kind,phase,verdict)=%r" % (triple,))
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
    obj["idempotencyKey"] = "%s#%s@%s:%s/%s#%s" % (
        obj.get("repo"), obj.get("pr"), obj.get("headSha"),
        obj.get("kind"), obj.get("phase"), rnd)
    return obj


def canon(obj):
    return json.dumps(obj, separators=(",", ":"), sort_keys=True)


def extract_payloads(blob):
    return BLOCK_RE.findall(blob)


def facts_of(obj):
    # Strip every emit-derived field so the block can be re-derived and compared
    # against itself. cycle keeps only round (maxRounds/exhausted are derived).
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


def do_emit(schema):
    try:
        facts = json.load(sys.stdin)
        obj = derive(facts)
    except Exception as e:
        sys.stderr.write("pr-meta emit: %s\n" % e)
        sys.exit(1)
    errs = validate(obj, schema)
    if errs:
        sys.stderr.write("pr-meta emit: derived object fails schema:\n  " + "\n  ".join(errs) + "\n")
        sys.exit(1)
    payload = base64.b64encode(canon(obj).encode("utf-8")).decode("ascii")
    if "--" in payload:  # impossible for standard base64; fail-closed guard
        sys.stderr.write("pr-meta emit: base64 payload contains '--' (HTML-comment unsafe)\n")
        sys.exit(1)
    sys.stdout.write("<!-- %s %s -->\n" % (MARKER, payload))


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


def main():
    if len(sys.argv) < 3:
        sys.stderr.write("pr-meta engine: usage: <mode> <schema-path> [args]\n")
        sys.exit(64)
    mode, schema_path = sys.argv[1], sys.argv[2]
    with open(schema_path) as f:
        schema = json.load(f)
    if mode == "emit":
        do_emit(schema)
    elif mode == "decode":
        do_decode(schema)
    elif mode == "extract":
        do_extract(schema, sys.argv[3], sys.argv[4], sys.argv[5])
    elif mode == "maxround":
        do_maxround(schema, sys.argv[3], sys.argv[4])
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

cmd_emit() { py emit "${SCHEMA_FILE}"; }

cmd_decode() { py decode "${SCHEMA_FILE}"; }

# fetch_trusted_bodies prints the bodies of PR comments that are BOTH from a
# trusted author (author_association OWNER/MEMBER/COLLABORATOR) AND a real pm:*
# protocol comment (carry a <!-- pm:ship|fix|pr-review --> marker). This is the
# single trust boundary for the dispatch protocol (F3): neither an untrusted
# commenter nor a canonical block pasted into a trusted author's plain (non-pm)
# comment can reach extract/round as a dispatch fact.
fetch_trusted_bodies() {
    local pr="$1"
    gh api "repos/${REPO_SLUG}/issues/${pr}/comments" --paginate \
        --jq '.[] | select((.author_association == "OWNER" or .author_association == "MEMBER" or .author_association == "COLLABORATOR") and (.body | test("<!-- pm:(ship|fix|pr-review) -->"))) | .body'
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

# ---- dispatch --------------------------------------------------------------

main() {
    local sub="${1:-}"
    if [[ $# -gt 0 ]]; then shift; fi
    case "${sub}" in
        emit)     cmd_emit "$@" ;;
        decode)   cmd_decode "$@" ;;
        extract)  cmd_extract "$@" ;;
        round)    cmd_round "$@" ;;
        -h|--help|help) usage; exit 0 ;;
        "") usage; exit 64 ;;
        *) echo "pr-meta: unknown subcommand '${sub}'" >&2; usage; exit 64 ;;
    esac
}

main "$@"
