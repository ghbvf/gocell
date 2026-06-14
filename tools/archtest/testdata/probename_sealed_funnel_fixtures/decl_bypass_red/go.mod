module fixturetest/probename_sealed_funnel/decl_bypass_red

go 1.25.11

require (
	github.com/ghbvf/gocell/framework v0.0.0
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/ghbvf/gocell/framework => ../../../../../framework
