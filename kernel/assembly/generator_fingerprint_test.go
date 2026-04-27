package assembly

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fingerprintProject builds a richer ProjectMeta than buildTestProject so that
// HTTP transport, event/command/projection endpoints, and lifecycle/consistency
// fields are all in scope. Each contract is exported by assembly "ssobff" so
// it appears in the fingerprint hashing loop.
func fingerprintProject() *metadata.ProjectMeta {
	tBool := true
	cs := func(c *metadata.ContractMeta) {
		c.Lifecycle = "active"
		c.ConsistencyLevel = "L1"
	}
	httpC := &metadata.ContractMeta{
		ID:        "http.auth.login.v1",
		Kind:      "http",
		OwnerCell: "accesscore",
		Endpoints: metadata.EndpointsMeta{
			Server:  "accesscore",
			Clients: []string{"edge-bff"},
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "POST",
				Path:          "/api/v1/auth/login",
				SuccessStatus: 200,
				Responses: map[int]metadata.HTTPResponseMeta{
					400: {Description: "bad", SchemaRef: "../err.schema.json"},
					401: {Description: "unauth", SchemaRef: "../err.schema.json"},
				},
			},
		},
	}
	cs(httpC)
	httpC.Triggers = []string{"event.session.created.v1"}

	// event/command/projection contracts must have at least one external
	// participant so computeBoundaryContracts classifies them as exported and
	// the fingerprint loop reaches their fields.
	eventC := &metadata.ContractMeta{
		ID:                "event.session.created.v1",
		Kind:              "event",
		OwnerCell:         "accesscore",
		IdempotencyKey:    "session_id",
		DeliverySemantics: "at-least-once",
		Replayable:        &tBool,
		Endpoints: metadata.EndpointsMeta{
			Publisher:   "accesscore",
			Subscribers: []string{"auditcore", "external-siem"},
		},
	}
	cs(eventC)

	commandC := &metadata.ContractMeta{
		ID:        "command.session.revoke.v1",
		Kind:      "command",
		OwnerCell: "accesscore",
		Endpoints: metadata.EndpointsMeta{
			Handler:  "accesscore",
			Invokers: []string{"edge-bff"},
		},
	}
	cs(commandC)

	projectionC := &metadata.ContractMeta{
		ID:        "projection.audit.summary.v1",
		Kind:      "projection",
		OwnerCell: "auditcore",
		Endpoints: metadata.EndpointsMeta{
			Provider: "auditcore",
			Readers:  []string{"edge-bff"},
		},
	}
	cs(projectionC)

	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID: "accesscore", Type: "core", ConsistencyLevel: "L1",
				Owner:  metadata.OwnerMeta{Team: "identity", Role: "maintainer"},
				Schema: metadata.SchemaMeta{Primary: "users"},
				Verify: metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore"}},
			},
			"auditcore": {
				ID: "auditcore", Type: "core", ConsistencyLevel: "L2",
				Owner:  metadata.OwnerMeta{Team: "compliance", Role: "maintainer"},
				Schema: metadata.SchemaMeta{Primary: "audit_logs"},
				Verify: metadata.CellVerifyMeta{Smoke: []string{"smoke.auditcore"}},
			},
		},
		Slices: make(map[string]*metadata.SliceMeta),
		Contracts: map[string]*metadata.ContractMeta{
			httpC.ID:       httpC,
			eventC.ID:      eventC,
			commandC.ID:    commandC,
			projectionC.ID: projectionC,
		},
		Journeys: make(map[string]*metadata.JourneyMeta),
		Assemblies: map[string]*metadata.AssemblyMeta{
			"ssobff": {
				ID:    "ssobff",
				Cells: []string{"accesscore", "auditcore"},
				Build: metadata.BuildMeta{
					Entrypoint: "cmd/ssobff/main.go",
					Binary:     "ssobff",
				},
			},
		},
	}
}

func computeFingerprint(t *testing.T, p *metadata.ProjectMeta) string {
	t.Helper()
	gen := NewGenerator(p, "github.com/ghbvf/gocell", "")
	out, err := gen.GenerateBoundary("ssobff")
	require.NoError(t, err)
	// boundary.yaml.tpl has a sourceFingerprint: <hex> line — extracting the
	// fingerprint is overkill; the rendered bytes change iff the fingerprint
	// changes (all fields are deterministic by sortedCopy). Use the rendered
	// bytes directly as proxy.
	return string(out)
}

// computeFingerprintWithRoot runs GenerateBoundary via a Generator that can
// read schema files from disk (needed for schema-content hashing tests).
func computeFingerprintWithRoot(t *testing.T, p *metadata.ProjectMeta, root string) string {
	t.Helper()
	gen := NewGenerator(p, "github.com/ghbvf/gocell", root)
	out, err := gen.GenerateBoundary("ssobff")
	require.NoError(t, err)
	return string(out)
}

func TestSourceFingerprint_StructuralChangesDetected(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*metadata.ProjectMeta)
	}{
		{"http method change", func(p *metadata.ProjectMeta) {
			p.Contracts["http.auth.login.v1"].Endpoints.HTTP.Method = "PUT"
		}},
		{"http path change", func(p *metadata.ProjectMeta) {
			p.Contracts["http.auth.login.v1"].Endpoints.HTTP.Path = "/api/v1/auth/login2"
		}},
		{"http listener kind change", func(p *metadata.ProjectMeta) {
			p.Contracts["http.auth.login.v1"].Endpoints.HTTP.Path = "/internal/v1/auth/login"
		}},
		{"http successStatus change", func(p *metadata.ProjectMeta) {
			p.Contracts["http.auth.login.v1"].Endpoints.HTTP.SuccessStatus = 201
		}},
		{"http response keys add", func(p *metadata.ProjectMeta) {
			p.Contracts["http.auth.login.v1"].Endpoints.HTTP.Responses[413] =
				metadata.HTTPResponseMeta{Description: "too big", SchemaRef: "../err.schema.json"}
		}},
		{"http response keys remove", func(p *metadata.ProjectMeta) {
			delete(p.Contracts["http.auth.login.v1"].Endpoints.HTTP.Responses, 400)
		}},
		{"http noContent toggle", func(p *metadata.ProjectMeta) {
			p.Contracts["http.auth.login.v1"].Endpoints.HTTP.NoContent = true
		}},
		{"http clients change", func(p *metadata.ProjectMeta) {
			p.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"edge-bff", "another"}
		}},
		{"contract kind change http→event", func(p *metadata.ProjectMeta) {
			c := p.Contracts["http.auth.login.v1"]
			c.Kind = "event"
			c.Endpoints.HTTP = nil
			c.Endpoints.Publisher = "accesscore"
			c.Endpoints.Subscribers = []string{"auditcore"}
		}},
		{"contract lifecycle change", func(p *metadata.ProjectMeta) {
			p.Contracts["http.auth.login.v1"].Lifecycle = "deprecated"
		}},
		{"contract consistency change", func(p *metadata.ProjectMeta) {
			p.Contracts["http.auth.login.v1"].ConsistencyLevel = "L2"
		}},
		{"contract triggers change", func(p *metadata.ProjectMeta) {
			p.Contracts["http.auth.login.v1"].Triggers = []string{"event.other.v1"}
		}},
		{"event delivery semantics change", func(p *metadata.ProjectMeta) {
			p.Contracts["event.session.created.v1"].DeliverySemantics = "exactly-once"
		}},
		{"event idempotency change", func(p *metadata.ProjectMeta) {
			p.Contracts["event.session.created.v1"].IdempotencyKey = "session_id_v2"
		}},
		{"event replayable toggle", func(p *metadata.ProjectMeta) {
			f := false
			p.Contracts["event.session.created.v1"].Replayable = &f
		}},
		{"event subscribers change", func(p *metadata.ProjectMeta) {
			p.Contracts["event.session.created.v1"].Endpoints.Subscribers = []string{"auditcore", "external-siem", "support"}
		}},
		{"command invokers change", func(p *metadata.ProjectMeta) {
			p.Contracts["command.session.revoke.v1"].Endpoints.Invokers = []string{"edge-bff", "support"}
		}},
		{"projection readers change", func(p *metadata.ProjectMeta) {
			p.Contracts["projection.audit.summary.v1"].Endpoints.Readers = []string{"edge-bff", "support"}
		}},
	}

	baseline := computeFingerprint(t, fingerprintProject())
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			p := fingerprintProject()
			tc.mutate(p)
			got := computeFingerprint(t, p)
			assert.NotEqual(t, baseline, got, "%s should change fingerprint", tc.name)
		})
	}
}

func TestSourceFingerprint_NonStructuralFieldsIgnored(t *testing.T) {
	baseline := computeFingerprint(t, fingerprintProject())

	p := fingerprintProject()
	p.Contracts["http.auth.login.v1"].Description = "annotated description"
	p.Contracts["http.auth.login.v1"].DeprecatedAt = "2099-01-01"
	got := computeFingerprint(t, p)
	assert.Equal(t, baseline, got, "Description/DeprecatedAt are documentation only and must not perturb the fingerprint")
}

func TestSourceFingerprint_SubscribersOrderNotSignificant(t *testing.T) {
	p1 := fingerprintProject()
	p2 := fingerprintProject()
	// Same set, different declaration order — sortedCopy must canonicalise so
	// the fingerprint stays stable.
	p2.Contracts["event.session.created.v1"].Endpoints.Subscribers = []string{"external-siem", "auditcore"}
	assert.Equal(t, computeFingerprint(t, p1), computeFingerprint(t, p2))
}

// ---------------------------------------------------------------------------
// New fingerprint cases (PR-CFG-M Lane 1 additions)
// ---------------------------------------------------------------------------

// TestSourceFingerprint_NilContractInMap verifies that a nil contract value in
// the project map does not panic and still produces a stable fingerprint.
func TestSourceFingerprint_NilContractInMap(t *testing.T) {
	p := fingerprintProject()
	// Insert a nil pointer under a new ID that is exported (server=accesscore, no clients).
	p.Contracts["http.nil.v1"] = nil

	// Should not panic; GenerateBoundary skips nil contracts when computing
	// provider/consumer classification (registry returns "" for nil).
	assert.NotPanics(t, func() {
		gen := NewGenerator(p, "github.com/ghbvf/gocell", "")
		out, err := gen.GenerateBoundary("ssobff")
		require.NoError(t, err)
		assert.NotEmpty(t, out)
	})
}

// TestSourceFingerprint_SchemaRefsPayloadPathChange verifies that modifying
// SchemaRefs.Payload triggers a fingerprint change.
func TestSourceFingerprint_SchemaRefsPayloadPathChange(t *testing.T) {
	baseline := computeFingerprint(t, fingerprintProject())

	p := fingerprintProject()
	p.Contracts["http.auth.login.v1"].SchemaRefs.Payload = "payload.schema.json"
	got := computeFingerprint(t, p)
	assert.NotEqual(t, baseline, got, "changing SchemaRefs.Payload path must change fingerprint")
}

// TestSourceFingerprint_SchemaFileContentChange verifies that modifying the
// content of a schema file (without changing its path) changes the fingerprint
// when projectRoot is provided.
func TestSourceFingerprint_SchemaFileContentChange(t *testing.T) {
	// Set up a temp project root with a real schema file.
	root := t.TempDir()
	contractDir := filepath.Join(root, "contracts", "http", "auth", "login", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	schemaPath := filepath.Join(contractDir, "request.schema.json")
	require.NoError(t, os.WriteFile(schemaPath, []byte(`{"type":"object"}`), 0o644))

	p := fingerprintProject()
	// Set Dir so the generator resolves the schema path correctly.
	p.Contracts["http.auth.login.v1"].Dir = filepath.ToSlash(filepath.Join("contracts", "http", "auth", "login", "v1"))
	p.Contracts["http.auth.login.v1"].SchemaRefs = contracts.SchemaRefs{Request: "request.schema.json"}

	baseline := computeFingerprintWithRoot(t, p, root)

	// Modify schema file content.
	require.NoError(t, os.WriteFile(schemaPath, []byte(`{"type":"object","required":["id"]}`), 0o644))
	got := computeFingerprintWithRoot(t, p, root)

	assert.NotEqual(t, baseline, got, "changing schema file content must change fingerprint")
}

// TestSourceFingerprint_PathParamsChange verifies that modifying PathParams
// changes the fingerprint.
func TestSourceFingerprint_PathParamsChange(t *testing.T) {
	baseline := computeFingerprint(t, fingerprintProject())

	p := fingerprintProject()
	p.Contracts["http.auth.login.v1"].Endpoints.HTTP.PathParams = map[string]contracts.ParamSchema{
		"userId": {Type: "string", Format: "uuid"},
	}
	got := computeFingerprint(t, p)
	assert.NotEqual(t, baseline, got, "adding PathParams must change fingerprint")
}

// TestSourceFingerprint_QueryParamsChange verifies that modifying QueryParams
// changes the fingerprint.
func TestSourceFingerprint_QueryParamsChange(t *testing.T) {
	baseline := computeFingerprint(t, fingerprintProject())

	p := fingerprintProject()
	p.Contracts["http.auth.login.v1"].Endpoints.HTTP.QueryParams = map[string]contracts.ParamSchema{
		"redirect": {Type: "string"},
	}
	got := computeFingerprint(t, p)
	assert.NotEqual(t, baseline, got, "adding QueryParams must change fingerprint")
}

// TestSourceFingerprint_ResponseValueChange verifies that modifying the value
// of an existing HTTP response (description or schemaRef) changes the fingerprint.
func TestSourceFingerprint_ResponseValueChange(t *testing.T) {
	baseline := computeFingerprint(t, fingerprintProject())

	p := fingerprintProject()
	// Change description of the 400 response (key already exists in fingerprintProject).
	p.Contracts["http.auth.login.v1"].Endpoints.HTTP.Responses[400] =
		metadata.HTTPResponseMeta{Description: "changed description", SchemaRef: "../err.schema.json"}
	got := computeFingerprint(t, p)
	assert.NotEqual(t, baseline, got, "changing Responses value must change fingerprint")
}

// ---------------------------------------------------------------------------
// canonicalEncode stability tests
// ---------------------------------------------------------------------------

// TestCanonicalEncode_DeterministicAcross_100Runs verifies that the same input
// always produces the same byte sequence across 100 independent calls.
func TestCanonicalEncode_DeterministicAcross_100Runs(t *testing.T) {
	input := fingerprintProject().Contracts["http.auth.login.v1"]
	require.NotNil(t, input)

	var first []byte
	for i := range 100 {
		var buf bytes.Buffer
		require.NoError(t, canonicalEncode(&buf, *input), "run %d", i)
		if i == 0 {
			first = buf.Bytes()
		} else {
			assert.Equal(t, first, buf.Bytes(), "canonicalEncode must be deterministic on run %d", i)
		}
	}
}

// TestCanonicalEncode_DistinguishesNilAndZero verifies that a nil pointer and a
// pointer to a zero-value struct produce different encodings.
func TestCanonicalEncode_DistinguishesNilAndZero(t *testing.T) {
	type Foo struct {
		X string
	}

	var nilPtr *Foo
	zeroPtr := &Foo{}

	var nilBuf, zeroBuf bytes.Buffer
	require.NoError(t, canonicalEncode(&nilBuf, nilPtr))
	require.NoError(t, canonicalEncode(&zeroBuf, zeroPtr))

	assert.NotEqual(t, nilBuf.Bytes(), zeroBuf.Bytes(),
		"nil pointer and pointer-to-zero must produce different encodings")
}

// ---------------------------------------------------------------------------
// AnyFieldChange: exhaustive structural coverage via reflection
// ---------------------------------------------------------------------------

// fingerprintExcludedFields lists ContractMeta fields that are intentionally
// excluded from the structural fingerprint (tagged fingerprint:"-"). Mutations
// to these fields must NOT change the fingerprint.
var fingerprintExcludedFields = map[string]bool{
	"Description":  true,
	"DeprecatedAt": true,
	"Dir":          true,
	"File":         true,
}

// TestSourceFingerprint_AnyFieldChange walks every exported field of
// *ContractMeta (excluding documentation-only fields) and verifies that a
// single mutation always changes the fingerprint. This test automatically
// catches new structural fields added to ContractMeta without requiring any
// manual update to the hashing logic or this test.
func TestSourceFingerprint_AnyFieldChange(t *testing.T) {
	t.Parallel()
	baseline := computeFingerprint(t, fingerprintProject())

	typ := reflect.TypeOf(metadata.ContractMeta{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() || fingerprintExcludedFields[f.Name] {
			continue
		}

		t.Run(f.Name, func(t *testing.T) {
			t.Parallel()
			p := fingerprintProject()
			// Pick the contract with the richest data to maximise coverage.
			c := *p.Contracts["http.auth.login.v1"]
			mutateContractField(&c, f)
			p.Contracts["http.auth.login.v1"] = &c
			got := computeFingerprint(t, p)
			assert.NotEqual(t, baseline, got,
				"mutating ContractMeta.%s must change the fingerprint", f.Name)
		})
	}
}

// mutateContractField sets a single exported field of *c to a non-zero / changed
// value so that canonicalEncode produces a different byte sequence.
func mutateContractField(c *metadata.ContractMeta, f reflect.StructField) { //nolint:cyclop
	v := reflect.ValueOf(c).Elem().Field(f.Index[0])
	switch v.Kind() {
	case reflect.String:
		// Kind must remain a valid contract kind so the registry can resolve the
		// provider/consumer; flip between valid kinds instead of appending a suffix.
		if f.Name == "Kind" {
			if v.String() == "http" {
				v.SetString("event")
			} else {
				v.SetString("http")
			}
			return
		}
		if v.String() == "" {
			v.SetString("mutated")
		} else {
			v.SetString(v.String() + "-mutated")
		}
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(v.Uint() + 1)
	case reflect.Slice:
		// Append a string element or replace with a non-empty slice.
		if v.Type().Elem().Kind() == reflect.String {
			v.Set(reflect.Append(v, reflect.ValueOf("mutated")))
		}
	case reflect.Ptr:
		// For *bool: flip; for other pointers: replace with non-nil.
		if v.Type() == reflect.TypeOf((*bool)(nil)) {
			b := true
			if !v.IsNil() {
				prev := v.Elem().Bool()
				b = !prev
			}
			v.Set(reflect.ValueOf(&b))
		}
	case reflect.Struct:
		// For EndpointsMeta: append a subscriber to trigger a structural change.
		if f.Name == "Endpoints" {
			ep := v.Interface().(metadata.EndpointsMeta)
			ep.Subscribers = append(ep.Subscribers, "mutated-cell")
			v.Set(reflect.ValueOf(ep))
		}
		// For SchemaRefsMeta: set Request to a new path.
		if f.Name == "SchemaRefs" {
			sr := v.Interface().(contracts.SchemaRefs)
			sr.Request = "mutated.schema.json"
			v.Set(reflect.ValueOf(sr))
		}
	}
}
