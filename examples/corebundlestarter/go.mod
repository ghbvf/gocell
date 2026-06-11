module github.com/ghbvf/gocell/examples/corebundlestarter

go 1.25.11

require (
	github.com/ghbvf/gocell v0.0.0
	github.com/ghbvf/gocell/cellmodules v0.0.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/ghbvf/gocell/adapters/adapterutil v0.0.0 // indirect
	github.com/ghbvf/gocell/adapters/postgres v0.0.0 // indirect
	github.com/ghbvf/gocell/adapters/ratelimit v0.0.0 // indirect
	github.com/ghbvf/gocell/adapters/redis v0.0.0 // indirect
	github.com/ghbvf/gocell/corecells v0.0.0-00010101000000-000000000000 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/pressly/goose/v3 v3.27.1 // indirect
	github.com/redis/go-redis/v9 v9.20.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	go.opentelemetry.io/contrib/propagators/b3 v1.44.0 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Local monorepo replace: under GOWORK=on the go.work workspace overrides this
// and resolves the core module to the repo root; under GOWORK=off (the
// release-consistency build in hack/verify-workspace.sh) this replace makes the
// unpublished core module resolve to the repo root instead of being fetched.
replace github.com/ghbvf/gocell => ../../

replace github.com/ghbvf/gocell/cellmodules => ../../cellmodules

replace github.com/ghbvf/gocell/corecells => ../../corecells

replace github.com/ghbvf/gocell/adapters/adapterutil => ../../adapters/adapterutil

replace github.com/ghbvf/gocell/adapters/postgres => ../../adapters/postgres

replace github.com/ghbvf/gocell/adapters/ratelimit => ../../adapters/ratelimit

replace github.com/ghbvf/gocell/adapters/redis => ../../adapters/redis
