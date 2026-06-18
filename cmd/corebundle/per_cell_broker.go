// per_cell_broker.go: per-cell broker-URL env-loading helper.
//
// ref: Kratos config/env prefix-strip convention — each module reads its own namespace.
// ref: cellmodules/percellpg + per_cell_adapter.go (LoadPGConfig) — the per-cell DSN twin.
package main

import "os"

// LoadBrokerURL resolves a broker cell's AMQP URL from cell-namespaced env, with
// an assembly-wide fallback:
//
//	GOCELL_<CELLID>_AMQP_URL   (per-cell override)
//	GOCELL_AMQP_URL            (assembly-wide fallback when the per-cell var is unset)
//
// The fallback keeps the common colocated deployment (every broker cell shares one
// broker) a single GOCELL_AMQP_URL: every cell resolves to the same value, so
// eventtransport.dedupBrokerURL collapses them to one connection (behavior-
// preserving). A per-cell override is only meaningful once distinct brokers are
// supported end-to-end (egress-only today rejects distinct URLs — see
// cellmodules/eventtransport.dedupBrokerURL and #2152 / #2341).
//
// Returns "" when neither var is set; the eventtransport resolver fail-closes on
// an empty per-cell URL in postgres topology (naming GOCELL_<CELLID>_AMQP_URL).
//
// ref: Kratos config/env prefix-strip convention.
func LoadBrokerURL(cellEnvPrefix string) string {
	if v := os.Getenv("GOCELL_" + cellEnvPrefix + "_AMQP_URL"); v != "" {
		return v
	}
	return os.Getenv("GOCELL_AMQP_URL")
}
