package identitymanage

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpAuthUserCreateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.create.v1")

	c.ValidateRequest(t, []byte(`{"username":"alice","email":"a@b.com","password":"secret"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"u-1","username":"alice","email":"a@b.com","status":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectRequest(t, []byte(`{"username":"alice","email":"a@b.com","password":"s","extra":"bad"}`))
}

func TestHttpAuthUserGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.get.v1")

	c.ValidateResponse(t, []byte(`{"data":{"id":"u-1","username":"alice","email":"a@b.com","status":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthUserUpdateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.update.v1")

	c.ValidateRequest(t, []byte(`{"email":"new@b.com"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"u-1","username":"alice","email":"new@b.com","status":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectRequest(t, []byte(`{"email":"a","extra":"bad"}`))
}

func TestHttpAuthUserPatchV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.patch.v1")

	c.ValidateRequest(t, []byte(`{"name":"Bob"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"u-1","username":"alice","email":"a@b.com","status":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthUserDeleteV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	_ = contracttest.LoadByID(t, root, "http.auth.user.delete.v1")
}

func TestHttpAuthUserLockV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.lock.v1")

	c.ValidateResponse(t, []byte(`{"data":{"status":"locked"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthUserUnlockV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.user.unlock.v1")

	c.ValidateResponse(t, []byte(`{"data":{"status":"active"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestEventUserCreatedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.user.created.v1")

	// Schema validation: payload requires user_id + username, headers requires event_id.
	c.ValidatePayload(t, []byte(`{"user_id":"usr-1","username":"alice"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-123"}`))
	c.MustRejectPayload(t, []byte(`{"user_id":"x"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventUserLockedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.user.locked.v1")

	// Schema validation: payload requires user_id, headers requires event_id.
	c.ValidatePayload(t, []byte(`{"user_id":"usr-1"}`))
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-456"}`))
	c.MustRejectPayload(t, []byte(`{}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
