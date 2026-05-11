package eventrouter

import (
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
)

func testEventSpec(topic string) contractspec.ContractSpec {
	return contractspec.ContractSpec{ID: "test:" + topic, Kind: cellvocab.ContractEvent, Transport: "amqp", Topic: topic}
}
