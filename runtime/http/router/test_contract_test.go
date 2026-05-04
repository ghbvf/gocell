package router

import (
	"strings"

	"github.com/ghbvf/gocell/kernel/wrapper"
)

func testHTTPContract(method, path string) wrapper.ContractSpec {
	spec := wrapper.ContractSpec{ID: "test:" + method + ":" + path, Kind: "http", Transport: "http", Method: method, Path: path}
	// Internal paths require non-empty Clients (Wave 2: caller-cell identity).
	// Use a test sentinel caller so router unit tests remain self-contained.
	if strings.HasPrefix(path, "/internal/v1/") {
		spec.Clients = []string{"test-caller"}
	}
	return spec
}
