// HANDLER-NO-SCHEMA-FOR-NOBODY-01 — forward gate that no-body HTTP endpoints
// (GET / DELETE) must NOT embed requestSchemaJSON or wire a request validator.
//
// # Background
//
// PR-V1-CODEGEN-FULL-MIGRATION (PR #376) introduced runtime/http/schemavalidate
// to validate request bodies via santhosh-tekuri/jsonschema. Initially the
// generator embedded the request schema for every contract that declared
// schemaRefs.request, including GET endpoints whose schema is just a
// "no body" placeholder ({"type":"object","additionalProperties":false}).
// That wired a validator into the handler that no code path ever called —
// init-time compile cost + binary bloat for zero benefit, and a contract
// semantics drift (GET says "no body" but runtime carries body-validation
// machinery).
//
// Builder fix in W4 F5 only embeds the schema when endpointSpec.HasBody is
// true (i.e. POST/PUT/PATCH with a declared request schema). This gate locks
// that invariant so future template/builder changes can't silently
// re-introduce the dead code.
//
// # What this gate checks
//
// For every generated HTTP handler_gen.go in generated/contracts/http/**, if
// the corresponding contract.yaml declares a method other than POST / PUT /
// PATCH (i.e. GET / DELETE / HEAD / OPTIONS), the handler file must NOT
// contain `requestSchemaJSON` literal or `schemavalidate.NewValidator` call.
//
// ref: deepmap/oapi-codegen — request validator emitted only for operations
// declaring requestBody.
package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const handlerNoSchemaForNobodyRule = "HANDLER-NO-SCHEMA-FOR-NOBODY-01"

// methodsWithBody lists HTTP methods that may legitimately read a request body.
// Any method outside this set is treated as no-body for this gate.
var methodsWithBody = map[string]bool{
	"POST":  true,
	"PUT":   true,
	"PATCH": true,
}

func TestHandlerNoSchemaForNobody01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	for _, contract := range project.Contracts {
		if contract.Kind != "http" || !contract.Codegen {
			continue
		}
		if contract.Endpoints.HTTP == nil {
			continue
		}
		method := strings.ToUpper(contract.Endpoints.HTTP.Method)
		if methodsWithBody[method] {
			continue
		}
		// This is a GET / DELETE / HEAD / OPTIONS endpoint — handler must
		// not embed schema or wire validator.
		pkgPath := contractIDToExpectedPkgPath(contract.ID)
		handlerPath := filepath.Join(root, pkgPath, "handler_gen.go")
		body, err := os.ReadFile(handlerPath) //nolint:gosec // archtest scans repo paths it discovered itself
		if err != nil {
			// Some handler shapes (e.g. event-only contracts) have no handler_gen.go.
			continue
		}
		text := string(body)
		if strings.Contains(text, "requestSchemaJSON") {
			t.Errorf("%s: %s (method %s) handler embeds requestSchemaJSON literal — "+
				"no-body endpoints must skip schema embed (rebuild with W4 F5 builder)",
				handlerNoSchemaForNobodyRule, contract.ID, method)
		}
		if strings.Contains(text, "schemavalidate.NewValidator") {
			t.Errorf("%s: %s (method %s) handler wires schemavalidate.NewValidator — "+
				"no-body endpoints must not compile a request validator",
				handlerNoSchemaForNobodyRule, contract.ID, method)
		}
	}
}
