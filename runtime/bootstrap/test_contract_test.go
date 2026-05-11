package bootstrap

import (
	"strings"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
)

func testHTTPContract(method, path string) contractspec.ContractSpec {
	spec := contractspec.ContractSpec{
		ID:        "test:" + method + ":" + path,
		Kind:      cellvocab.ContractHTTP,
		Transport: "http",
		Method:    method,
		Path:      path,
	}
	// Internal paths require non-empty Clients (ContractSpec.Validate enforced by wrapper.HTTPHandler).
	// Bootstrap integration tests use "accesscore" as the service-token caller cell.
	if strings.HasPrefix(path, "/internal/v1/") {
		spec.Clients = []string{"accesscore"}
	}
	return spec
}

func testEventSpec(topic string) contractspec.ContractSpec {
	return contractspec.ContractSpec{ID: "test:" + topic, Kind: cellvocab.ContractEvent, Transport: "amqp", Topic: topic}
}
