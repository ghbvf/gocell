module github.com/ghbvf/gocell/cmd/gocell

go 1.25.11

require (
	github.com/ghbvf/gocell v0.0.0
	github.com/ghbvf/gocell/tools v0.0.0
	github.com/stretchr/testify v1.11.1
	golang.org/x/tools v0.46.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	mvdan.cc/gofumpt v0.9.2 // indirect
)

// Local monorepo replace: under GOWORK=on the go.work workspace overrides this
// and resolves the core module to the repo root; under GOWORK=off (the
// release-consistency build in hack/verify-workspace.sh) this replace makes the
// unpublished core module resolve to the repo root instead of being fetched.
replace github.com/ghbvf/gocell => ../../

replace github.com/ghbvf/gocell/tools => ../../tools

// Transitive replace closure: `tools` requires the unpublished adapter modules
// (adapterutil/mqtt/postgres/redis) at v0.0.0. Go `replace` is NOT transitive, so
// `tools`' own replaces do not propagate to this main module — without these,
// GOWORK=off full module-graph commands (`go list -m all`) fail to resolve v0.0.0
// even though lazy `go build ./...` passes. Guarded by hack/verify-workspace.sh.
replace github.com/ghbvf/gocell/adapters/adapterutil => ../../adapters/adapterutil

replace github.com/ghbvf/gocell/adapters/mqtt => ../../adapters/mqtt

replace github.com/ghbvf/gocell/adapters/postgres => ../../adapters/postgres

replace github.com/ghbvf/gocell/adapters/redis => ../../adapters/redis

replace github.com/ghbvf/gocell/generated => ../../generated
