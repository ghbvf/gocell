module fixturetest/webhook_allow_loopback_violate

go 1.25.11

require (
	github.com/ghbvf/gocell/framework v0.0.0
	github.com/google/uuid v1.6.0 // indirect
)

replace github.com/ghbvf/gocell/framework => ../../../../framework
