package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// HANDLER-PATH-QUERY-LENGTH-VALIDATION-01 ensures that whenever a contract.yaml
// declares minLength/maxLength on a path or query parameter, the generated
// handler enforces it at runtime. This guards the regression surfaced after
// PR-V1-CODEGEN-FULL-MIGRATION-FU B4 had initially deleted these inline checks
// when migrating body validation to santhosh-tekuri/jsonschema/v6.
//
// Body validation is delegated to the schema validator and is NOT scanned here;
// only path/query string-length constraints are checked because schemavalidate
// only consumes the request body schema.
//
// Error message contract: handlers must use the generic "{name}: invalid"
// format (no "value too short" / "value too long") to avoid length oracle
// attacks (cf. F-SEC-001 in docs/reviews/202605051730-PR376/).
func TestHANDLER_PATH_QUERY_LENGTH_VALIDATION_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	contractsDir := filepath.Join(root, "contracts")

	type expectation struct {
		yamlRel   string
		paramName string
		isPath    bool
		minLen    *int
		maxLen    *int
		generated string
	}

	var expects []expectation
	walkErr := filepath.Walk(contractsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Base(path) != "contract.yaml" {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // archtest scans repo paths it discovered itself
		if readErr != nil {
			return readErr
		}
		var doc struct {
			ID        string `yaml:"id"`
			Kind      string `yaml:"kind"`
			Codegen   bool   `yaml:"codegen"`
			Endpoints struct {
				HTTP *struct {
					PathParams  map[string]paramSchema `yaml:"pathParams"`
					QueryParams map[string]paramSchema `yaml:"queryParams"`
				} `yaml:"http"`
			} `yaml:"endpoints"`
		}
		//nolint:nilerr // unstructured/non-contract YAMLs in tree are skipped silently
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil
		}
		if !doc.Codegen || doc.Kind != "http" || doc.Endpoints.HTTP == nil {
			return nil
		}
		gen := contractIDToExpectedPkgPath(doc.ID)
		genPath := filepath.Join(root, gen, "handler_gen.go")
		yamlRel, _ := filepath.Rel(root, path)

		// Pagination endpoints (queryParams = {cursor, limit}) delegate cursor /
		// limit length+range checks to httputil.ParsePageParams (single source of
		// truth: query.MaxCursorTokenBytes / query.MaxPageSize). contract.yaml
		// length constraints on cursor/limit are documentation-only here.
		isPagination := false
		if qp := doc.Endpoints.HTTP.QueryParams; qp != nil {
			_, hasCursor := qp["cursor"]
			_, hasLimit := qp["limit"]
			isPagination = hasCursor && hasLimit && len(qp) == 2
		}

		for name, p := range doc.Endpoints.HTTP.PathParams {
			if p.Type == "string" && (p.MinLength != nil || p.MaxLength != nil) {
				expects = append(expects, expectation{yamlRel, name, true, p.MinLength, p.MaxLength, genPath})
			}
		}
		if !isPagination {
			for name, p := range doc.Endpoints.HTTP.QueryParams {
				if p.Type == "string" && (p.MinLength != nil || p.MaxLength != nil) {
					expects = append(expects, expectation{yamlRel, name, false, p.MinLength, p.MaxLength, genPath})
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk contracts: %v", walkErr)
	}
	if len(expects) == 0 {
		t.Fatal("HANDLER-PATH-QUERY-LENGTH-VALIDATION-01: no contract with " +
			"path/query length constraint found — survey expected ~22 contracts " +
			"with pathParams.minLength/maxLength; check survey logic")
	}

	for _, e := range expects {
		body, err := os.ReadFile(e.generated)
		if err != nil {
			t.Errorf("%s: cannot read generated handler %s: %v", e.yamlRel, e.generated, err)
			continue
		}
		text := string(body)
		// Generic "{name}: invalid" message must be present when constraint exists;
		// "value too short" / "value too long" must NOT be present (oracle guard).
		if strings.Contains(text, e.paramName+`: value too short`) || strings.Contains(text, e.paramName+`: value too long`) {
			t.Errorf("%s param %q: handler exposes length oracle in error message; use \"{name}: invalid\" form", e.yamlRel, e.paramName)
		}
		if e.minLen != nil || e.maxLen != nil {
			if !strings.Contains(text, e.paramName+`: invalid`) {
				t.Errorf("%s param %q: contract declares min/maxLength but handler %s "+
					"lacks `%s: invalid` validation message",
					e.yamlRel, e.paramName, e.generated, e.paramName)
			}
		}
	}
}

type paramSchema struct {
	Type      string `yaml:"type"`
	MinLength *int   `yaml:"minLength"`
	MaxLength *int   `yaml:"maxLength"`
}
