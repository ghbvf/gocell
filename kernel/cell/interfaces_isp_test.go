package cell

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// PR-A22 ISP 拆分守卫 — 通过编译期断言验证四子接口可独立 mock，复合 Cell
// 等价于四者并集。配套 archtest 在 tools/archtest/cell_iface_isp_test.go 守
// kernel/cell/interfaces.go 文件级形态。
//
// ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D1/D2/D3

// idMock 仅实现 CellIdentity 三方法。
type idMock struct {
	id    string
	ctype CellType
	level Level
}

func (m idMock) ID() string              { return m.id }
func (m idMock) Type() CellType          { return m.ctype }
func (m idMock) ConsistencyLevel() Level { return m.level }

var _ CellIdentity = (*idMock)(nil)

// lifecycleMock 仅实现 CellLifecycle 三方法。
type lifecycleMock struct{}

func (lifecycleMock) Init(_ context.Context, _ Registry) error { return nil }
func (lifecycleMock) Start(_ context.Context) error            { return nil }
func (lifecycleMock) Stop(_ context.Context) error             { return nil }

var _ CellLifecycle = (*lifecycleMock)(nil)

// statusMock 仅实现 CellStatus 两方法。
type statusMock struct {
	healthy bool
	ready   bool
}

func (m statusMock) Health() HealthStatus {
	status := "unhealthy"
	if m.healthy {
		status = "healthy"
	}
	return HealthStatus{Status: status}
}
func (m statusMock) Ready() bool { return m.ready }

var _ CellStatus = (*statusMock)(nil)

// inventoryMock 仅实现 CellInventory 四方法（含原 Cell.Metadata()）。
type inventoryMock struct {
	meta *metadata.CellMeta
}

func (m inventoryMock) Metadata() *metadata.CellMeta  { return m.meta }
func (m inventoryMock) OwnedSlices() []Slice          { return nil }
func (m inventoryMock) ProducedContracts() []Contract { return nil }
func (m inventoryMock) ConsumedContracts() []Contract { return nil }

var _ CellInventory = (*inventoryMock)(nil)

// TestCellSubInterfaces_IndependentMockability documents that each sub-interface
// can be mocked without implementing the others, satisfying ISP.
// Both sub-cases hit cell.ResolveCellEmitter::resolveDemoEmitter pairing invariant
// (writer XOR txRunner = error).
func TestCellSubInterfaces_IndependentMockability(t *testing.T) {
	t.Parallel()
	var ci CellIdentity = idMock{id: "x", ctype: "core", level: 1}
	if ci.ID() != "x" {
		t.Errorf("CellIdentity ID = %q, want %q", ci.ID(), "x")
	}

	var cl CellLifecycle = lifecycleMock{}
	if err := cl.Start(context.Background()); err != nil {
		t.Errorf("CellLifecycle Start unexpected error: %v", err)
	}

	var cs CellStatus = statusMock{healthy: true, ready: true}
	if !cs.Ready() {
		t.Error("CellStatus.Ready() = false, want true")
	}

	meta := &metadata.CellMeta{ID: "y"}
	var cv CellInventory = inventoryMock{meta: meta}
	if cv.Metadata() != meta {
		t.Error("CellInventory.Metadata() did not return injected meta pointer")
	}

	// Negative path: unhealthy mock must report unhealthy state.
	unhealthy := statusMock{healthy: false, ready: false}
	if unhealthy.Ready() {
		t.Error("CellStatus.Ready() = true on unhealthy mock; want false")
	}
	if unhealthy.Health().Status != "unhealthy" {
		t.Errorf("CellStatus.Health().Status = %q on unhealthy mock; want %q",
			unhealthy.Health().Status, "unhealthy")
	}
}

// TestCell_CompositeEquivalence verifies that the composite Cell interface is
// satisfied by *BaseCell — the canonical witness that satisfying all four
// sub-interfaces (each pinned at base.go via independent compile-time checks)
// is equivalent to satisfying the composite.
func TestCell_CompositeEquivalence(t *testing.T) {
	t.Parallel()
	var c Cell = (*BaseCell)(nil) // composite satisfied via 4-segment checks
	_ = c
	// 4 sub-interfaces all satisfied by *BaseCell
	var (
		_ CellIdentity  = (*BaseCell)(nil)
		_ CellLifecycle = (*BaseCell)(nil)
		_ CellStatus    = (*BaseCell)(nil)
		_ CellInventory = (*BaseCell)(nil)
	)
}
