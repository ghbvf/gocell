module github.com/ghbvf/gocell/adapters/oidc

go 1.25.11

require (
	github.com/coreos/go-oidc/v3 v3.18.0
	github.com/ghbvf/gocell/adapters/adapterutil v0.0.0
	github.com/stretchr/testify v1.11.1
	golang.org/x/oauth2 v0.36.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/ghbvf/gocell/framework v0.0.0
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/ghbvf/gocell/adapters/adapterutil => ../adapterutil

replace github.com/ghbvf/gocell/framework => ../../framework
