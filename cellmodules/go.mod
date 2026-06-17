module github.com/ghbvf/gocell/cellmodules

go 1.25.11

require (
	github.com/ghbvf/gocell/adapters/grpc v0.0.0-00010101000000-000000000000
	github.com/ghbvf/gocell/adapters/rabbitmq v0.0.0
	github.com/ghbvf/gocell/adapters/vault v0.0.0
	github.com/ghbvf/gocell/corecells v0.0.0-00010101000000-000000000000
	github.com/jackc/pgx/v5 v5.10.0
	github.com/rabbitmq/amqp091-go v1.11.0
	github.com/redis/go-redis/v9 v9.20.1
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-retryablehttp v0.7.8 // indirect
	github.com/hashicorp/go-rootcerts v1.0.2 // indirect
	github.com/hashicorp/go-secure-stdlib/parseutil v0.2.0 // indirect
	github.com/hashicorp/go-secure-stdlib/strutil v0.1.2 // indirect
	github.com/hashicorp/go-sockaddr v1.0.7 // indirect
	github.com/hashicorp/hcl v1.0.1-vault-7 // indirect
	github.com/hashicorp/vault/api v1.23.0 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/ryanuber/go-glob v1.0.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.81.1 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/ghbvf/gocell/adapters/adapterutil v0.0.0
	github.com/ghbvf/gocell/adapters/postgres v0.0.0
	github.com/ghbvf/gocell/adapters/ratelimit v0.0.0
	github.com/ghbvf/gocell/adapters/redis v0.0.0
	github.com/ghbvf/gocell/framework v0.0.0
	github.com/ghbvf/gocell/generated v0.0.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/pressly/goose/v3 v3.27.1 // indirect
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

replace github.com/ghbvf/gocell/corecells => ../corecells

replace github.com/ghbvf/gocell/adapters/adapterutil => ../adapters/adapterutil

replace github.com/ghbvf/gocell/adapters/grpc => ../adapters/grpc

replace github.com/ghbvf/gocell/adapters/postgres => ../adapters/postgres

replace github.com/ghbvf/gocell/adapters/rabbitmq => ../adapters/rabbitmq

replace github.com/ghbvf/gocell/adapters/ratelimit => ../adapters/ratelimit

replace github.com/ghbvf/gocell/adapters/redis => ../adapters/redis

replace github.com/ghbvf/gocell/adapters/vault => ../adapters/vault

replace github.com/ghbvf/gocell/adapters/prometheus => ../adapters/prometheus

replace github.com/ghbvf/gocell/generated => ../generated

replace github.com/ghbvf/gocell/framework => ../framework

replace github.com/ghbvf/gocell/tests => ../tests
