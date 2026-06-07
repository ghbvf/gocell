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
# `schema`, `cycle.exhausted`, `next`, and `idempotencyKey`. The 3-round
# circuit breaker is enforced here — when a changes-requested round is
# exhausted (round >= maxRounds), `next.agent` is forced to `human` so the
# #935/#1657 daemons stop dispatching and escalate.
#
# Subcommands:
#   emit              stdin JSON facts  -> stdout block line              (offline)
#   decode            stdin markdown/block -> stdout validated JSON        (offline)
#   extract <PR#>     gh-fetched comments -> latest fresh block JSON        (online)
#   round   <PR#>     gh-fetched comments -> max cycle.round (0 if none)    (online)
#   selftest          emit<->decode round-trip + circuit-breaker assertions (offline)
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
usage: pr-meta.sh <emit|decode|extract|round|selftest> [args]
  emit            read fact JSON on stdin, print the gocell-pr-meta:v1 block
  decode          read markdown/block on stdin, print validated JSON
  extract <PR#>   fetch PR comments, print the latest block JSON iff fresh
  round   <PR#>   fetch PR comments, print max cycle.round (0 if none)
  selftest        run offline emit<->decode + circuit-breaker assertions
EOF
}

# py runs the shared python engine. argv: <mode> <schema-path>; stdin passes
# through. The engine holds every pure transform (validate/derive/encode/decode)
# so the bash layer only adds gh fetching for the online subcommands.
#
# The program is delivered on fd 3 (python3 /dev/fd/3) instead of via a `-`
# stdin heredoc, so stdin stays free for the piped JSON/markdown the engine
# reads. `python3 - <<'PY'` would make the heredoc itself python's stdin.
py() {
    python3 /dev/fd/3 "$@" 3<<'PY'
import sys, json, base64, re

SCHEMA_CONST = "gocell-pr-meta/v1"
MARKER = "gocell-pr-meta:v1"
BLOCK_RE = re.compile(r"<!--\s*gocell-pr-meta:v1\s+([A-Za-z0-9+/=]+)\s*-->")


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
    obj["schema"] = SCHEMA_CONST
    cyc = dict(obj.get("cycle") or {})
    if "round" not in cyc:
        raise ValueError("cycle.round is required in input facts")
    cyc.setdefault("maxRounds", 3)
    cyc["exhausted"] = bool(cyc["round"] >= cyc["maxRounds"])
    obj["cycle"] = cyc
    obj["next"] = derive_next(obj.get("verdict"), cyc["exhausted"])
    obj.setdefault("session", None)
    obj.setdefault("worktree", None)
    obj["idempotencyKey"] = "%s#%s@%s:%s/%s#%s" % (
        obj.get("repo"), obj.get("pr"), obj.get("headSha"),
        obj.get("kind"), obj.get("phase"), cyc["round"])
    return obj


def canon(obj):
    return json.dumps(obj, separators=(",", ":"), sort_keys=True)


def extract_payloads(blob):
    return BLOCK_RE.findall(blob)


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


def decode_payload(payload, schema):
    raw = base64.b64decode(payload, validate=True)
    obj = json.loads(raw)
    errs = validate(obj, schema)
    if errs:
        raise ValueError("block fails schema:\n  " + "\n  ".join(errs))
    return obj


def do_decode(schema):
    payloads = extract_payloads(sys.stdin.read())
    if not payloads:
        sys.stderr.write("pr-meta decode: no gocell-pr-meta:v1 block found\n")
        sys.exit(2)
    try:
        obj = decode_payload(payloads[-1], schema)  # latest block wins
    except Exception as e:
        sys.stderr.write("pr-meta decode: %s\n" % e)
        sys.exit(2)
    sys.stdout.write(canon(obj) + "\n")


def do_maxround(schema):
    best = 0
    for payload in extract_payloads(sys.stdin.read()):
        try:
            obj = decode_payload(payload, schema)
            r = int(obj["cycle"]["round"])
        except Exception:
            continue  # skip malformed/foreign blocks
        if r > best:
            best = r
    sys.stdout.write("%d\n" % best)


def main():
    mode, schema_path = sys.argv[1], sys.argv[2]
    with open(schema_path) as f:
        schema = json.load(f)
    if mode == "emit":
        do_emit(schema)
    elif mode == "decode":
        do_decode(schema)
    elif mode == "maxround":
        do_maxround(schema)
    else:
        sys.stderr.write("pr-meta engine: unknown mode %r\n" % mode)
        sys.exit(64)


main()
PY
}

cmd_emit() { py emit "${SCHEMA_FILE}"; }

cmd_decode() { py decode "${SCHEMA_FILE}"; }

cmd_extract() {
    local pr="${1:-}"
    if [[ -z "${pr}" ]]; then echo "pr-meta extract: missing <PR#>" >&2; return 64; fi
    local bodies decoded live_sha dec_sha dec_repo dec_pr
    bodies="$(gh api "repos/${REPO_SLUG}/issues/${pr}/comments" --paginate --jq '.[].body')" \
        || { echo "pr-meta extract: gh api comments failed" >&2; return 1; }
    decoded="$(printf '%s\n' "${bodies}" | py decode "${SCHEMA_FILE}")" \
        || { echo "pr-meta extract: no valid block on PR #${pr}" >&2; return 2; }
    live_sha="$(gh pr view "${pr}" --repo "${REPO_SLUG}" --json headRefOid --jq .headRefOid)" \
        || { echo "pr-meta extract: gh pr view failed" >&2; return 1; }
    dec_sha="$(printf '%s' "${decoded}" | jq -r .headSha)"
    dec_repo="$(printf '%s' "${decoded}" | jq -r .repo)"
    dec_pr="$(printf '%s' "${decoded}" | jq -r .pr)"
    if [[ "${dec_repo}" != "${REPO_SLUG}" || "${dec_pr}" != "${pr}" || "${dec_sha}" != "${live_sha}" ]]; then
        echo "pr-meta extract: stale block (repo=${dec_repo} pr=${dec_pr} headSha=${dec_sha} vs live repo=${REPO_SLUG} pr=${pr} headSha=${live_sha})" >&2
        return 3
    fi
    printf '%s\n' "${decoded}"
}

cmd_round() {
    local pr="${1:-}"
    if [[ -z "${pr}" ]]; then echo "pr-meta round: missing <PR#>" >&2; return 64; fi
    local bodies
    bodies="$(gh api "repos/${REPO_SLUG}/issues/${pr}/comments" --paginate --jq '.[].body')" \
        || { echo "pr-meta round: gh api comments failed" >&2; return 1; }
    printf '%s\n' "${bodies}" | py maxround "${SCHEMA_FILE}"
}

# ---- selftest --------------------------------------------------------------

st_fail=0
st_assert_eq() { # <label> <got> <want>
    if [[ "${2}" == "${3}" ]]; then
        echo "  ok: ${1}"
    else
        echo "  FAIL: ${1} — got [${2}] want [${3}]" >&2
        st_fail=1
    fi
}

st_field() { printf '%s' "${1}" | jq -r "${2}"; }

cmd_selftest() {
    echo "pr-meta selftest"
    local headsha="0123456789abcdef0123456789abcdef01234567"
    local findings='{"total":0,"fixed":0,"unresolved":0,"blocking":0,"byP":{"p0":0,"p1":0,"p2":0,"p3":0},"byCx":{"cx1":0,"cx2":0,"cx3":0,"cx4":0}}'
    local base block json block2

    # 1. ship round 0 -> needs-review-again, next.agent=codex, not exhausted
    base="$(jq -nc --arg sha "${headsha}" --argjson f "${findings}" \
        '{repo:"ghbvf/gocell",pr:1660,kind:"ship",phase:"ship",tool:"claude-code",baseRef:"develop",headRef:"Feature/1660-pr-meta-protocol",headSha:$sha,session:null,worktree:null,verdict:"needs-review-again",findings:$f,cycle:{round:0}}')"
    block="$(printf '%s' "${base}" | cmd_emit)"
    st_assert_eq "ship: single-line block, base64-only payload" \
        "$(printf '%s' "${block}" | grep -cE '^<!-- gocell-pr-meta:v1 [A-Za-z0-9+/=]+ -->$')" "1"
    json="$(printf '%s\n' "${block}" | cmd_decode)"
    st_assert_eq "ship: schema const" "$(st_field "${json}" .schema)" "gocell-pr-meta/v1"
    st_assert_eq "ship: verdict" "$(st_field "${json}" .verdict)" "needs-review-again"
    st_assert_eq "ship: next.agent" "$(st_field "${json}" .next.agent)" "codex"
    st_assert_eq "ship: next.command" "$(st_field "${json}" '.next.command')" "codex review"
    st_assert_eq "ship: cycle.exhausted" "$(st_field "${json}" .cycle.exhausted)" "false"
    st_assert_eq "ship: cycle.maxRounds default" "$(st_field "${json}" .cycle.maxRounds)" "3"
    st_assert_eq "ship: idempotencyKey" "$(st_field "${json}" .idempotencyKey)" \
        "ghbvf/gocell#1660@${headsha}:ship/ship#0"
    # fixpoint round-trip: re-emitting the decoded object is byte-identical
    block2="$(printf '%s' "${json}" | cmd_emit)"
    st_assert_eq "ship: emit/decode fixpoint" "${block}" "${block2}"

    # 2. fix round 1 -> needs-check-fix, next.command=/pr-review --check
    base="$(jq -nc --arg sha "${headsha}" --argjson f "${findings}" \
        '{repo:"ghbvf/gocell",pr:1660,kind:"fix",phase:"fix",tool:"claude-code",baseRef:"develop",headRef:"x",headSha:$sha,session:null,worktree:null,verdict:"needs-check-fix",findings:$f,cycle:{round:1}}')"
    json="$(printf '%s' "${base}" | cmd_emit | cmd_decode)"
    st_assert_eq "fix: verdict" "$(st_field "${json}" .verdict)" "needs-check-fix"
    st_assert_eq "fix: next.command" "$(st_field "${json}" '.next.command')" "/pr-review --check"

    # 3. pr-review review changes-requested round 2 (< 3) -> next=/fix, not exhausted
    base="$(jq -nc --arg sha "${headsha}" --argjson f "${findings}" \
        '{repo:"ghbvf/gocell",pr:1660,kind:"pr-review",phase:"review",tool:"claude-code",baseRef:"develop",headRef:"x",headSha:$sha,session:null,worktree:null,verdict:"changes-requested",findings:$f,cycle:{round:2}}')"
    json="$(printf '%s' "${base}" | cmd_emit | cmd_decode)"
    st_assert_eq "review r2: exhausted" "$(st_field "${json}" .cycle.exhausted)" "false"
    st_assert_eq "review r2: next.agent" "$(st_field "${json}" .next.agent)" "claude"
    st_assert_eq "review r2: next.command" "$(st_field "${json}" '.next.command')" "/fix"

    # 4. circuit breaker: changes-requested round 3 (== 3) -> exhausted, next=human
    base="$(jq -nc --arg sha "${headsha}" --argjson f "${findings}" \
        '{repo:"ghbvf/gocell",pr:1660,kind:"pr-review",phase:"review",tool:"claude-code",baseRef:"develop",headRef:"x",headSha:$sha,session:null,worktree:null,verdict:"changes-requested",findings:$f,cycle:{round:3}}')"
    json="$(printf '%s' "${base}" | cmd_emit | cmd_decode)"
    st_assert_eq "breaker r3: exhausted" "$(st_field "${json}" .cycle.exhausted)" "true"
    st_assert_eq "breaker r3: next.agent" "$(st_field "${json}" .next.agent)" "human"
    st_assert_eq "breaker r3: next.command null" "$(st_field "${json}" '.next.command')" "null"
    st_assert_eq "breaker r3: next.triggerLabel null" "$(st_field "${json}" '.next.triggerLabel')" "null"

    # 5. pr-review check ready round 3 -> terminal, next.agent null
    base="$(jq -nc --arg sha "${headsha}" --argjson f "${findings}" \
        '{repo:"ghbvf/gocell",pr:1660,kind:"pr-review",phase:"check",tool:"claude-code",baseRef:"develop",headRef:"x",headSha:$sha,session:null,worktree:null,verdict:"ready",findings:$f,cycle:{round:3}}')"
    json="$(printf '%s' "${base}" | cmd_emit | cmd_decode)"
    st_assert_eq "check ready: next.agent null" "$(st_field "${json}" '.next.agent')" "null"

    # 6. maxround over multiple blocks picks the highest round
    base="$(jq -nc --arg sha "${headsha}" --argjson f "${findings}" \
        '{repo:"ghbvf/gocell",pr:1660,kind:"fix",phase:"fix",tool:"claude-code",baseRef:"develop",headRef:"x",headSha:$sha,session:null,worktree:null,verdict:"needs-check-fix",findings:$f,cycle:{round:1}}')"
    block="$(printf '%s' "${base}" | cmd_emit)"
    base="$(jq -nc --arg sha "${headsha}" --argjson f "${findings}" \
        '{repo:"ghbvf/gocell",pr:1660,kind:"fix",phase:"fix",tool:"claude-code",baseRef:"develop",headRef:"x",headSha:$sha,session:null,worktree:null,verdict:"needs-check-fix",findings:$f,cycle:{round:3}}')"
    block2="$(printf '%s' "${base}" | cmd_emit)"
    st_assert_eq "maxround over two blocks" \
        "$(printf '%s\n%s\n' "${block}" "${block2}" | py maxround "${SCHEMA_FILE}")" "3"

    # 7. negative: a bad verdict is rejected by emit (fail-closed)
    base="$(jq -nc --arg sha "${headsha}" --argjson f "${findings}" \
        '{repo:"ghbvf/gocell",pr:1660,kind:"ship",phase:"ship",tool:"claude-code",baseRef:"develop",headRef:"x",headSha:$sha,session:null,worktree:null,verdict:"bogus",findings:$f,cycle:{round:0}}')"
    if printf '%s' "${base}" | cmd_emit >/dev/null 2>&1; then
        echo "  FAIL: bad verdict accepted by emit" >&2; st_fail=1
    else
        echo "  ok: bad verdict rejected by emit"
    fi

    if [[ "${st_fail}" -ne 0 ]]; then
        echo "selftest: FAIL" >&2
        return 1
    fi
    echo "selftest: PASS"
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
        selftest) cmd_selftest "$@" ;;
        ""|-h|--help|help) usage; [[ "${sub}" == "" ]] && exit 64 || exit 0 ;;
        *) echo "pr-meta: unknown subcommand '${sub}'" >&2; usage; exit 64 ;;
    esac
}

main "$@"
