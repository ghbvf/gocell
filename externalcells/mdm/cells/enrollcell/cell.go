// Package enrollcell is the MDM enrollment cell. It wires the cert-status
// framework-serving slice (status.Service / status.Repository) and exposes an
// mdm-owned PDP baseline (enrollAuthorizer) for the primary listener.
//
// framework-owned contract http.deviceidentity.status.v1 is served by the
// composition root via bootstrap.WithFrameworkHTTPServing (ADR-1939 D4);
// the route is NOT registered in cell.Init (framework-owned contracts are mounted
// at the composition-root level, not via cell contractUsage). The cell's Init
// remains a clean BaseCell.Init only.
//
// PR-1 scope: cert-status read (L0, coarse admin/operator gate) + cert-bottom-layer
// smoke proof via certdeps.Resolve in cmd/mdmd. Signer/RevStore injection and
// device-self gate land in PR-2.
package enrollcell

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/composition"

	"github.com/ghbvf/gocell-mdm/cells/enrollcell/slices/status"
)

// cellID is the single source for both the cell metadata ID and the module ID.
// composition.Build enforces cell.ID() == module.ID() (M12a closed-set identity).
const cellID = "enrollcell"

var _ cell.Cell = (*EnrollCell)(nil)

// Deps holds the injectable dependencies for EnrollCell. All fields are required
// for PR-1; the composition root constructs each dep and passes it via NewModule.
//
// PR-2 will extend Deps with certsigning.Signer and certsigning.RevocationStore
// when enroll and revoke are implemented.
type Deps struct {
	// StatusRepo is the CertRecord repository used by the status.Service.
	// The demo topology wires status/mem.New; the postgres topology (PR-15)
	// wires a PG-backed implementation.
	StatusRepo status.Repository
}

// EnrollCell is the MDM enrollment cell.
type EnrollCell struct {
	*cell.BaseCell
	statusRepo status.Repository
	authorizer auth.Authorizer
}

// cellMeta mirrors cells/enrollcell/cell.yaml. Hand-written because the cell has
// no codegen; cell.go's constructor passes a Clone so callers cannot mutate the literal.
var cellMeta = &metadata.CellMeta{
	ID:               cellID,
	Type:             "core",
	ConsistencyLevel: "L2",
	DurabilityMode:   "demo",
	Lifecycle:        "experimental",
	Owner:            metadata.OwnerMeta{Team: "mdm", Role: "enrollcell-owner"},
	Schema:           metadata.SchemaMeta{Primary: "mdm_enroll"},
	Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.enrollcell.startup"}},
}

// NewEnrollCell constructs the enrollment cell with its dependencies.
func NewEnrollCell(deps Deps) *EnrollCell {
	return &EnrollCell{
		BaseCell:   cell.MustNewBaseCell(cellMeta.Clone()),
		statusRepo: deps.StatusRepo,
		authorizer: enrollAuthorizer{},
	}
}

// Authorizer returns the mdm-owned PDP for the primary listener.
// bootstrap.PrimaryAuthorizerOption discovers this via duck-type interface
// and wires it into bootstrap.WithPrimaryAuthorizer.
func (c *EnrollCell) Authorizer() auth.Authorizer {
	return c.authorizer
}

// Init is a clean no-op beyond BaseCell.Init: the framework-owned status contract
// is NOT registered here (composition root mounts it via WithFrameworkHTTPServing).
// /healthz and /readyz turn green from BaseCell's own Health()/Ready() once
// bootstrap transitions the cell to the started state.
func (c *EnrollCell) Init(ctx context.Context, reg cell.Registrar) error {
	return c.BaseCell.Init(ctx, reg)
}

// NewModule constructs the composition.CellModule for the enrollment cell.
// It replaces the old zero-arg Module() (deleted, no shim per not-backward-compat policy).
func NewModule(deps Deps) composition.CellModule {
	return enrollModule{deps: deps}
}

type enrollModule struct {
	deps Deps
}

func (m enrollModule) ID() string { return cellID }

// Provide constructs the enrollment cell with its deps. The composition root
// passes SharedDeps but enrollment currently uses none of them directly —
// the status repo and authorizer are wired from Deps.
func (m enrollModule) Provide(_ context.Context, _ *composition.SharedDeps) (composition.ModuleResult, error) {
	c := NewEnrollCell(m.deps)
	return composition.ModuleResult{Cell: c}, nil
}
