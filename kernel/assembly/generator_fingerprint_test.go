package assembly

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
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
	gen := NewGenerator(p, "github.com/ghbvf/gocell")
	out, err := gen.GenerateBoundary("ssobff")
	require.NoError(t, err)
	// boundary.yaml.tpl has a sourceFingerprint: <hex> line — extracting the
	// fingerprint is overkill; the rendered bytes change iff the fingerprint
	// changes (everything else is deterministic by sortedCopy and stable
	// SOURCE_DATE_EPOCH=0). Use the rendered bytes directly as proxy.
	return string(out)
}

func TestSourceFingerprint_StructuralChangesDetected(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "0")
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
	t.Setenv("SOURCE_DATE_EPOCH", "0")
	baseline := computeFingerprint(t, fingerprintProject())

	p := fingerprintProject()
	p.Contracts["http.auth.login.v1"].Description = "annotated description"
	p.Contracts["http.auth.login.v1"].DeprecatedAt = "2099-01-01"
	got := computeFingerprint(t, p)
	assert.Equal(t, baseline, got, "Description/DeprecatedAt are documentation only and must not perturb the fingerprint")
}

func TestSourceFingerprint_SubscribersOrderNotSignificant(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "0")
	p1 := fingerprintProject()
	p2 := fingerprintProject()
	// Same set, different declaration order — sortedCopy must canonicalise so
	// the fingerprint stays stable.
	p2.Contracts["event.session.created.v1"].Endpoints.Subscribers = []string{"external-siem", "auditcore"}
	assert.Equal(t, computeFingerprint(t, p1), computeFingerprint(t, p2))
}
