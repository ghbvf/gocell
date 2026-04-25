package bootstrap

import "github.com/ghbvf/gocell/kernel/wrapper"

func testHTTPContract(method, path string) wrapper.ContractSpec {
	return wrapper.ContractSpec{ID: "test:" + method + ":" + path, Kind: "http", Transport: "http", Method: method, Path: path}
}

func testEventSpec(topic string) wrapper.ContractSpec {
	return wrapper.ContractSpec{ID: "test:" + topic, Kind: "event", Transport: "amqp", Topic: topic}
}
