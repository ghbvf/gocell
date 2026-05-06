package governance

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// CodeContractHealthTypedEnvelope is the rule code for CH-06 — typed response
// envelope alignment. Emitted as SeverityError when an HTTP contract's
// declared response set (SuccessStatus + responses[] keys) does not match
// the typed response struct set generated into types_gen.go.
//
// CH-06 closes the drift loop introduced by typed response envelope migration
// (PR-V1-CONTRACT-TYPED-RESPONSE-ENVELOPE): the post-service response surface
// is no longer reverse-inferred from errcode.Kind in handler AST (CH-04's
// remaining job is the pre-service helper-emission set — DecodeJSONStrict
// 400/413, ParsePageParams 400, ParseUUIDPathParam 400 — which still has no
// other source of truth). Instead, the contract.yaml responses[] table and
// the generated typed response struct set must agree exactly. Any builder bug
// (missing lift), orphan struct (stale generated file), or contract drift
// (yaml edited without regen) is statically caught here, replacing the
// fragile reverse inference path described in roadmap 06.FU.
//
// ref: oapi-codegen pkg/codegen/templates/strict/strict-responses.tmpl@main —
// the typed-response-set semantic this rule guards.
const CodeContractHealthTypedEnvelope = "CH-06"

// typedResponseStructPattern matches the generated typed response struct names
// produced by tools/codegen/contractgen/templates/types.tmpl. The status code
// is captured as the second-to-last 3-digit run before the suffix.
//
// Examples that match:
//
//   - Get200JSONResponse        → status 200
//   - Delete204NoContentResponse → status 204
//   - Get404ErrorResponse        → status 404
//   - HandleEnqueue201JSONResponse → status 201
//
// The leading run is the {HandlerMethod} (PascalCase, may contain digits if
// the contract is named that way), so the regex anchors the status as the
// 3-digit run immediately before the {Suffix} group.
//
// Correctness depends on archtest CODEGEN-CONTRACT-USER-OVERLAP-01
// (tools/archtest/codegen_contract_gen_test.go) which prevents hand-written
// .go files from landing under generated/contracts/. Without that guard a
// user-written DTO accidentally named e.g. `Foo200JSONResponse` would be
// counted as an "implemented" typed struct and could mask CH-06 orphan
// reports — the two rules together provide the closed-set guarantee.
var typedResponseStructPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*?(\d{3})(JSONResponse|NoContentResponse|ErrorResponse)$`)

// CheckHTTPTypedResponseEnvelope enforces CH-06: every HTTP contract that
// opts into codegen must have a typed response struct in its generated
// types_gen.go for every declared SuccessStatus + responses[] key, and no
// orphan structs may exist beyond the declared set.
//
// Skipped silently for:
//   - non-HTTP contracts (event/command/projection)
//   - codegen=false contracts (legacy hand-written handlers do not emit typed structs)
//   - missing types_gen.go (treated as codegen drift, surfaced by the verify pipeline)
func (v *Validator) CheckHTTPTypedResponseEnvelope(
	contracts []*metadata.ContractMeta, projectRoot string,
) []ValidationResult {
	var results []ValidationResult
	for _, c := range contracts {
		if c.Kind != "http" || !c.Codegen {
			continue
		}
		results = append(results, v.checkTypedEnvelopeForContract(c, projectRoot)...)
	}
	return results
}

func (v *Validator) checkTypedEnvelopeForContract(
	c *metadata.ContractMeta, projectRoot string,
) []ValidationResult {
	typesPath := typedEnvelopeTypesGenPath(projectRoot, c.ID)

	implemented, ok := scanTypedResponseStructs(typesPath)
	if !ok {
		// types_gen.go absent or unparseable — codegen drift is a separate
		// concern surfaced by `gocell generate --verify`. Stay silent here
		// rather than double-reporting; CH-06 only owns the alignment check
		// when both sides exist.
		return nil
	}

	declared := typedEnvelopeDeclaredStatuses(c)

	var results []ValidationResult
	for _, status := range diffStatuses(declared, implemented) {
		msg := fmt.Sprintf("%s: contract declares status %d but generated types_gen.go has no matching typed"+
			" response struct (regenerate via `gocell generate contract --all`)", c.ID, status)
		results = append(results, v.newResult(
			CodeContractHealthTypedEnvelope, SeverityError, IssueRequired,
			c.File, fmt.Sprintf("endpoints.http.responses[%d]", status), msg,
		))
	}
	for _, status := range diffStatuses(implemented, declared) {
		msg := fmt.Sprintf("%s: generated types_gen.go has typed response struct for status %d but contract.yaml"+
			" does not declare it (orphan struct — edit contract.yaml or rerun `gocell generate contract --all`)",
			c.ID, status)
		results = append(results, v.newResult(
			CodeContractHealthTypedEnvelope, SeverityError, IssueRequired,
			c.File, fmt.Sprintf("endpoints.http.responses[%d]", status), msg,
		))
	}
	return results
}

// typedEnvelopeDeclaredStatuses returns the union of SuccessStatus and
// responses[] keys declared on the HTTP endpoint. Auth.Responses (middleware-
// injected codes) are intentionally excluded — they are pre-service codes
// emitted by listener-mounted middleware and do not produce typed structs in
// types_gen.go (the generator only renders structs for entries in the IR
// Responses slice, which is built from SuccessStatus + responses[]).
func typedEnvelopeDeclaredStatuses(c *metadata.ContractMeta) map[int]struct{} {
	out := make(map[int]struct{})
	if c.Endpoints.HTTP == nil {
		return out
	}
	if c.Endpoints.HTTP.SuccessStatus > 0 {
		out[c.Endpoints.HTTP.SuccessStatus] = struct{}{}
	}
	for status := range c.Endpoints.HTTP.Responses {
		out[status] = struct{}{}
	}
	return out
}

// typedEnvelopeTypesGenPath resolves the absolute path to the generated
// types_gen.go file for the given contract. Mirrors
// tools/codegen/internal/pathx.ContractIDToPackagePath: every "internal"
// segment in the contract ID is rewritten to "internalapi" so generated
// packages remain importable from cells/ and examples/ (Go internal package
// rule).
//
// The mapping is duplicated here (10 lines) rather than imported from the
// contractgen-internal pathx package to keep kernel/governance free of any
// tools/codegen dependency. If a second consumer outside tools/ ever needs
// the same mapping, promote pathx to pkg/contractpath.
func typedEnvelopeTypesGenPath(projectRoot, contractID string) string {
	parts := strings.Split(contractID, ".")
	segments := make([]string, len(parts))
	for i, p := range parts {
		if p == "internal" {
			segments[i] = "internalapi"
		} else {
			segments[i] = p
		}
	}
	pkgParts := append([]string{projectRoot, "generated", "contracts"}, segments...)
	return filepath.Join(append(pkgParts, "types_gen.go")...)
}

// scanTypedResponseStructs parses types_gen.go and returns the set of HTTP
// status codes encoded in the typed response struct names declared at the
// package level. The second return is false when the file is absent or fails
// to parse, signaling that the alignment check should fall through silently
// (codegen drift is a separate concern owned by `gocell generate --verify`).
func scanTypedResponseStructs(typesPath string) (map[int]struct{}, bool) {
	if _, err := os.Stat(typesPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false
		}
		return nil, false
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, typesPath, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, false
	}
	out := map[int]struct{}{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		collectTypedResponseStatuses(gen.Specs, out)
	}
	return out, true
}

// collectTypedResponseStatuses appends every typed-response status code found
// in specs into out. Extracted from scanTypedResponseStructs to keep cognitive
// complexity below the package's 15-branch ceiling.
func collectTypedResponseStatuses(specs []ast.Spec, out map[int]struct{}) {
	for _, spec := range specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		m := typedResponseStructPattern.FindStringSubmatch(ts.Name.Name)
		if m == nil {
			continue
		}
		status, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			continue
		}
		out[status] = struct{}{}
	}
}
