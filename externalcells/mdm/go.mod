module github.com/ghbvf/gocell-mdm

go 1.25.11

require (
	github.com/ghbvf/gocell/cellmodules v0.0.0
	github.com/ghbvf/gocell/framework v0.0.0
	github.com/ghbvf/gocell/generated v0.0.0
	github.com/ghbvf/gocell/tools v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/ghbvf/gocell/adapters/softca v0.0.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	go.opentelemetry.io/contrib/propagators/b3 v1.44.0 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/tools v0.46.0 // indirect
)

replace github.com/ghbvf/gocell/framework => ../../framework

replace github.com/ghbvf/gocell/tools => ../../tools

replace github.com/ghbvf/gocell/cellmodules => ../../cellmodules

replace github.com/ghbvf/gocell/generated => ../../generated

replace github.com/ghbvf/gocell/adapters/softca => ../../adapters/softca

replace github.com/ghbvf/gocell/corecells => ../../corecells

replace github.com/ghbvf/gocell/adapters/adapterutil => ../../adapters/adapterutil

replace github.com/ghbvf/gocell/adapters/grpc => ../../adapters/grpc

replace github.com/ghbvf/gocell/adapters/postgres => ../../adapters/postgres

replace github.com/ghbvf/gocell/adapters/ratelimit => ../../adapters/ratelimit

replace github.com/ghbvf/gocell/adapters/redis => ../../adapters/redis

replace github.com/ghbvf/gocell/adapters/vault => ../../adapters/vault

replace github.com/ghbvf/gocell/adapters/rabbitmq => ../../adapters/rabbitmq

replace github.com/ghbvf/gocell/adapters/prometheus => ../../adapters/prometheus

replace github.com/ghbvf/gocell/tests => ../../tests

replace github.com/ghbvf/gocell/adapters/mqtt => ../../adapters/mqtt

replace github.com/ghbvf/gocell/adapters/oidc => ../../adapters/oidc

replace github.com/ghbvf/gocell/adapters/otel => ../../adapters/otel

replace github.com/ghbvf/gocell/adapters/s3 => ../../adapters/s3

replace github.com/ghbvf/gocell/adapters/websocket => ../../adapters/websocket
