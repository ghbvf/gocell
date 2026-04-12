package devicecommand

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestCommandDeviceCommandEnqueueV1Handle(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "command.device-command.enqueue.v1")

	c.ValidateRequest(t, []byte(`{"payload":"reboot"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"cmd-1","deviceId":"d-1","payload":"reboot","status":"pending"}}`))
	c.MustRejectRequest(t, []byte(`{"payload":"x","extra":"bad"}`))
}

func TestCommandDeviceCommandListV1Handle(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "command.device-command.list.v1")

	c.ValidateResponse(t, []byte(`{"data":[{"id":"cmd-1","deviceId":"d-1","payload":"reboot","status":"pending","createdAt":"2026-01-01T00:00:00Z"}],"hasMore":false}`))
	c.MustRejectResponse(t, []byte(`{"data":"not-array","hasMore":false}`))
}

func TestCommandDeviceCommandAckV1Handle(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "command.device-command.ack.v1")

	c.ValidateResponse(t, []byte(`{"data":{"status":"acked"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}
