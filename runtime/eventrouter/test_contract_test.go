package eventrouter

import "github.com/ghbvf/gocell/kernel/wrapper"

func testEventSpec(topic string) wrapper.ContractSpec {
	return wrapper.ContractSpec{ID: "test:" + topic, Kind: "event", Transport: "amqp", Topic: topic}
}
