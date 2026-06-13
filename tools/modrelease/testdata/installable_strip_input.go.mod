module github.com/ghbvf/gocell/cmd/gocell

go 1.25.11

require (
	github.com/ghbvf/gocell v0.0.0
	github.com/ghbvf/gocell/tools v0.0.0
	golang.org/x/tools v0.45.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

// Local monorepo replace: under GOWORK=on the go.work workspace overrides this.
replace github.com/ghbvf/gocell => ../../

replace github.com/ghbvf/gocell/tools => ../../tools

replace github.com/ghbvf/gocell/adapters/postgres => ../../adapters/postgres

replace github.com/ghbvf/gocell/adapters/redis => ../../adapters/redis
