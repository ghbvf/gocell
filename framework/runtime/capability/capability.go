package capability

import (
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// Kind names an assembly-level shared infrastructure capability. Cells declare
// what they consume via cell.yaml `requires`; the assembly's provisioned set is
// the derived union of its cells' requires (Design Y, #855 — see
// kernel/assembly.GenerateModulesGen). The closed value set below is mirrored by
// the cell.schema.json `requires` enum (kept in lockstep by kernel/metadata/schemas
// TestSchemaConstantsMatchSchemaLiterals). Unknown or duplicate values are
// rejected by `gocell validate` governance rule FMT-36 at validation time — not
// at parse time: ParseFS stays lenient, matching Kubernetes admission-layer enum
// validation rather than parse-time rejection.
type Kind string

const (
	// Postgres is the shared postgres pool (TxManager + OutboxWriter + DB).
	Postgres Kind = "postgres"
	// Redis is the shared redis client.
	Redis Kind = "redis"
	// RabbitMQ is recognized by the enum but has NO provider / provisioning path
	// yet: there is no RabbitMQProvider and cap_wiring's provisionCapabilities has
	// no rabbitmq case, so declaring `requires: [rabbitmq]` in a cell.yaml passes
	// FMT-36 + codegen but fails fast at provisionCapabilities' default branch. It stays in
	// the enum as recognized forward vocabulary; a real AMQP-shared-connection
	// assembly must land the RabbitMQProvider + provisioning atomically.
	RabbitMQ Kind = "rabbitmq"
)

// PGProvider is the sealed handle to the assembly's single postgres pool. It is
// implementable only inside package capability (unexported marker isPGProvider); the
// sole construction path is NewPGProvider. Consumers receive an injected
// PGProvider and must not construct adapter primitives themselves
// (CAPABILITY-PROVIDER-FUNNEL-01).
type PGProvider interface {
	// TxManager returns the shared transactional runner bound to the pool.
	TxManager() persistence.TxRunner
	// OutboxWriter returns the transactional outbox writer.
	OutboxWriter() outbox.Writer
	// DB returns the raw pool handle (*adapterpg.Pool.DB()) as any. The single
	// type-assertion lives in the cmd/* consumer; runtime/capability stays adapter-free.
	DB() any
	isPGProvider()
}

// PGSet is the per-cell postgres provider resolver injected on
// composition.SharedDeps (#2341). It replaces the former single
// SharedDeps.PG PGProvider so each cell module resolves ITS pool's provider via
// ForCell(cellID) — colocated assemblies map every cell to one shared provider
// (one pool); split assemblies map each cell to its own pool's provider (N pools,
// each driven by its own relay keyed by InfraInstanceKey).
//
// It is sealed (unexported marker isPGSet; sole constructor NewPGSet) so the
// per-instance fan-out cannot be forged outside the composition root, and ForCell
// fails closed on an unknown cell rather than returning a silent nil. memory
// topology leaves SharedDeps.PG nil — cell modules take their in-memory path.
type PGSet interface {
	// ForCell returns the postgres provider for cellID, or a fail-closed error if
	// the cell has no provisioned pool (never a silent nil).
	ForCell(cellID string) (PGProvider, error)
	// Sole returns (provider, true) iff the assembly is colocated (exactly one
	// distinct pool serving every cell). In split topology it returns (nil, false).
	// It is the SANCTIONED single accessor for assembly-wide consumers that have no
	// "cell" dimension — notably the CQRS projection harness, whose journal
	// global_seq is per-pool and therefore incomparable across a split fan-out, so
	// it MUST fail closed when Sole reports false.
	Sole() (PGProvider, bool)
	isPGSet()
}

// PGInstance pairs one pool's provider with the cells it serves. The composition
// root builds one per distinct-DSN pool (cellmodules/percellpg.Resolve groups the
// cells); cells across instances must be disjoint.
type PGInstance struct {
	Provider PGProvider
	Cells    []string
}

type pgSet struct {
	byCell map[string]PGProvider
	sole   PGProvider
	isSole bool
}

func (s pgSet) ForCell(cellID string) (PGProvider, error) {
	p, ok := s.byCell[cellID]
	if !ok {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"capability: no postgres provider provisioned for cell",
			errcode.WithInternal(errcode.InternalAttr("cell_id", cellID)))
	}
	return p, nil
}

func (s pgSet) Sole() (PGProvider, bool) { return s.sole, s.isSole }
func (pgSet) isPGSet() {
	// Sealed-interface marker: no behavior; blocks external PGSet impls.
}

// NewPGSet builds the sealed per-cell provider resolver from the per-instance
// providers. It fails closed on a wiring bug — a nil provider, an instance serving
// no cells, or a cell mapped to two pools — rather than building a half-wired set.
// Sole() reports true iff there is exactly one instance (colocated). Called only
// from the assembly wiring site (cmd/<id>/cap_wiring.go).
func NewPGSet(instances []PGInstance) (PGSet, error) {
	byCell := make(map[string]PGProvider)
	for _, inst := range instances {
		if inst.Provider == nil {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"capability: PGInstance has a nil provider")
		}
		if len(inst.Cells) == 0 {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"capability: PGInstance serves no cells")
		}
		for _, c := range inst.Cells {
			if _, dup := byCell[c]; dup {
				return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
					"capability: cell mapped to more than one postgres pool",
					errcode.WithInternal(errcode.InternalAttr("cell_id", c)))
			}
			byCell[c] = inst.Provider
		}
	}
	s := pgSet{byCell: byCell}
	if len(instances) == 1 {
		s.sole = instances[0].Provider
		s.isSole = true
	}
	return s, nil
}

// RedisProvider is the sealed handle to the assembly's shared redis client.
// Implementable only inside package capability; sole construction path NewRedisProvider.
type RedisProvider interface {
	// Client returns the raw redis client (*adapterredis.Client) as any. The
	// single type-assertion lives in the cmd/* consumer.
	Client() any
	isRedisProvider()
}

type pgProvider struct {
	tx     persistence.TxRunner
	writer outbox.Writer
	db     any
}

func (p pgProvider) TxManager() persistence.TxRunner { return p.tx }
func (p pgProvider) OutboxWriter() outbox.Writer     { return p.writer }
func (p pgProvider) DB() any                         { return p.db }
func (pgProvider) isPGProvider() {
	// Sealed-interface marker: no behavior; blocks external PGProvider impls.
}

// NewPGProvider wraps the composition-root-constructed postgres primitives into
// the sealed PGProvider. tx and writer are kernel-typed; db is the raw
// *adapterpg.Pool.DB() handle passed as any so this package never imports
// adapters/. Called only from the assembly wiring site (cmd/<id>/cap_wiring.go).
func NewPGProvider(tx persistence.TxRunner, writer outbox.Writer, db any) PGProvider {
	return pgProvider{tx: tx, writer: writer, db: db}
}

type redisProvider struct {
	client any
}

func (p redisProvider) Client() any { return p.client }
func (redisProvider) isRedisProvider() {
	// Sealed-interface marker: no behavior; blocks external RedisProvider impls.
}

// NewRedisProvider wraps the composition-root-constructed redis client into the
// sealed RedisProvider. client is the raw *adapterredis.Client passed as any.
func NewRedisProvider(client any) RedisProvider {
	return redisProvider{client: client}
}
