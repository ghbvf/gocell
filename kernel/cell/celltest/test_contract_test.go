package celltest

import "github.com/ghbvf/gocell/kernel/contractspec"

func testHTTPContract(method, path string) contractspec.ContractSpec {
	return contractspec.ContractSpec{ID: "test:" + method + ":" + path, Kind: "http", Transport: "http", Method: method, Path: path}
}
