package capability

import (
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// Kind names an assembly-level shared infrastructure capability declared in
// assembly.yaml `capabilities` and (in #855) consumed via cell.yaml `requires`.
// The closed value set below is the single source mirrored by the
// assembly.schema.json enum (kept in lockstep by kernel/metadata/schemas
// TestSchemaConstantsMatchSchemaLiterals). Unknown or duplicate values are
// rejected by `gocell validate` governance rule FMT-35 at validation time — not
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
	// no rabbitmq case, so declaring `capabilities: [rabbitmq]` passes FMT-35 +
	// codegen but fails fast at provisionCapabilities' default branch. It stays in
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
func (pgProvider) isPGProvider()                     {}

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

func (p redisProvider) Client() any    { return p.client }
func (redisProvider) isRedisProvider() {}

// NewRedisProvider wraps the composition-root-constructed redis client into the
// sealed RedisProvider. client is the raw *adapterredis.Client passed as any.
func NewRedisProvider(client any) RedisProvider {
	return redisProvider{client: client}
}
