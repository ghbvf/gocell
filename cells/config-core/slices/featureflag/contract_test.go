package featureflag

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpConfigFlagsListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.flags.list.v1")

	c.ValidateResponse(t, []byte(`{"data":[{"id":"f-1","key":"dark-mode","type":"boolean","enabled":true,"rolloutPercentage":100}],"hasMore":false}`))
	c.MustRejectResponse(t, []byte(`{"data":"not-array","hasMore":false}`))
}

func TestHttpConfigFlagsGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.flags.get.v1")

	c.ValidateResponse(t, []byte(`{"data":{"id":"f-1","key":"dark-mode","type":"boolean","enabled":true,"rolloutPercentage":100}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpConfigFlagsEvaluateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.flags.evaluate.v1")

	c.ValidateRequest(t, []byte(`{"subject":"user-123"}`))
	c.ValidateResponse(t, []byte(`{"data":{"key":"dark-mode","enabled":true}}`))
	c.MustRejectRequest(t, []byte(`{"subject":"x","extra":"bad"}`))
}
