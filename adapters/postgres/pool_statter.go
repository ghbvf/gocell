package postgres

import "github.com/ghbvf/gocell/runtime/observability/poolstats"

// Statter returns a poolstats.Statter bound to this pool with the supplied
// human-readable name (e.g. "postgres-main"). The OTel pool collector uses
// this adapter-neutral interface to emit gocell.pool.* metrics without
// knowing each adapter's internal stats type.
//
// ref: jackc/pgx pgxpool/stat.go — TotalConns/IdleConns/AcquiredConns/
// MaxConns/EmptyAcquireCount maps cleanly onto poolstats.Snapshot.
func (p *Pool) Statter(name string) poolstats.Statter {
	return &pgPoolStatter{pool: p, name: name}
}

type pgPoolStatter struct {
	pool *Pool
	name string
}

func (s *pgPoolStatter) PoolName() string { return s.name }

// Snapshot reads a fresh stat sample from pgxpool and projects it onto the
// neutral Snapshot fields. Returns a zero-value snapshot if the pool has
// not been initialised (defensive guard consistent with Pool.PoolStats).
func (s *pgPoolStatter) Snapshot() poolstats.Snapshot {
	if s.pool == nil || s.pool.inner == nil {
		return poolstats.Snapshot{}
	}
	st := s.pool.inner.Stat()
	return poolstats.Snapshot{
		TotalConns: int64(st.TotalConns()),
		IdleConns:  int64(st.IdleConns()),
		UsedConns:  int64(st.AcquiredConns()),
		MaxConns:   int64(st.MaxConns()),
		WaitCount:  st.EmptyAcquireCount(),
	}
}
