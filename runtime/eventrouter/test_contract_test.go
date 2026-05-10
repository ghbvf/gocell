package eventrouter

import "github.com/ghbvf/gocell/kernel/contractspec"

func testEventSpec(topic string) contractspec.ContractSpec {
	return contractspec.ContractSpec{ID: "test:" + topic, Kind: "event", Transport: "amqp", Topic: topic}
}
