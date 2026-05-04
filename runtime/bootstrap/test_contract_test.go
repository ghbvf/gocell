package bootstrap

import (
	"strings"

	"github.com/ghbvf/gocell/kernel/wrapper"
)

func testHTTPContract(method, path string) wrapper.ContractSpec {
	spec := wrapper.ContractSpec{ID: "test:" + method + ":" + path, Kind: "http", Transport: "http", Method: method, Path: path}
	// Internal paths require non-empty Clients (ContractSpec.Validate enforced by wrapper.HTTPHandler).
	// Bootstrap integration tests use "accesscore" as the service-token caller cell.
	if strings.HasPrefix(path, "/internal/v1/") {
		spec.Clients = []string{"accesscore"}
	}
	return spec
}

func testEventSpec(topic string) wrapper.ContractSpec {
	return wrapper.ContractSpec{ID: "test:" + topic, Kind: "event", Transport: "amqp", Topic: topic}
}
