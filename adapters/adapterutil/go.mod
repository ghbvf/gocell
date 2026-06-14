module github.com/ghbvf/gocell/adapters/adapterutil

go 1.25.11

require github.com/stretchr/testify v1.11.1

require github.com/kr/text v0.2.0 // indirect

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/ghbvf/gocell/framework v0.0.0
	github.com/google/uuid v1.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/ghbvf/gocell/framework => ../../framework
