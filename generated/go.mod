module github.com/ghbvf/gocell/generated

go 1.25.11

require (
	github.com/ghbvf/gocell v0.0.0
	google.golang.org/grpc v1.81.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Local monorepo replace: under GOWORK=on the go.work workspace overrides this
// and resolves the core module to the repo root; under GOWORK=off (the
// release-consistency build) this replace makes the unpublished core module
// resolve to the repo root instead of being fetched.
replace github.com/ghbvf/gocell => ../
