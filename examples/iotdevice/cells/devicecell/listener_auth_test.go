package devicecell

// listener_auth_test.go — contract-driven auth round-trip tests for devicecell.
//
// Gate: B2 "restore contract-driven authz"
// These tests verify policy semantics end-to-end using the real router and
// auth.TestContext (principal injection). No JWT middleware is required because
// policy checks run on the injected principal, not on a JWT token.
//
// PR-10d: gates migrated from auth.AnyRole / auth.SelfOr to
// auth.RequirePermission / auth.RequirePermissionForResource. These policies
// now read the Authorizer from context (set by bootstrap.WithPrimaryAuthorizer
// in production; injected via auth.WithAuthorizer in tests).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dto "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// testCtx creates a request context carrying both a Principal (for authn) and
// the iotdevice example PDP (for authz). RequirePermission / RequirePermissionForResource
// require both to be present; this helper satisfies that contract for handler tests.
func testCtx(subject string, roles []string) context.Context {
	return auth.WithAuthorizer(auth.TestContext(subject, roles), deviceAuthorizer{})
}

// TestRegister_NoAuth_Returns201 verifies that device registration requires no
// authentication (public endpoint). This test passes even before the B2 fix
// because nil policy in the test router never blocks, but after the B2 fix it
// is correctly backed by Public:true in the contract.
func TestRegister_NoAuth_Returns201(t *testing.T) {
	r := initCellWithRouter(t)

	body := `{"name":"new-sensor"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No auth context — public endpoint must not require authentication.
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("register (public): want 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestEnqueue_NonAdmin_ShouldBe403 asserts that a caller without RoleAdmin or
// RoleOperator is rejected with 403 when enqueueing a command.
// Gate: RequirePermission(PermDeviceCommand()) + deviceAuthorizer deny for non-admin/operator.
func TestEnqueue_NonAdmin_ShouldBe403(t *testing.T) {
	r := initCellWithRouter(t)

	// Register a device first.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"auth-test-device"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device: got %d", rec.Code)
	}
	deviceID := extractData(t, rec.Body.Bytes())["id"].(string)

	// Caller has only "viewer" role — must be rejected.
	rec = httptest.NewRecorder()
	cmdBody := `{"payload":"reboot","commandType":"reboot"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+deviceID+"/commands", strings.NewReader(cmdBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(testCtx("viewer-1", []string{"viewer"}))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("enqueue non-admin: want 403, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestEnqueue_Admin_Returns201 verifies that RoleAdmin can enqueue commands.
// Gate: RequirePermission(PermDeviceCommand()) + deviceAuthorizer allow for admin.
func TestEnqueue_Admin_Returns201(t *testing.T) {
	r := initCellWithRouter(t)

	// Register a device.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"admin-device"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device: got %d", rec.Code)
	}
	deviceID := extractData(t, rec.Body.Bytes())["id"].(string)

	// Admin enqueues — must succeed.
	rec = httptest.NewRecorder()
	cmdBody := `{"payload":"reboot","commandType":"reboot"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+deviceID+"/commands", strings.NewReader(cmdBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(testCtx("admin-1", []string{dto.RoleAdmin}))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("enqueue admin: want 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestEnqueue_Operator_Returns201 verifies that RoleOperator can enqueue commands.
func TestEnqueue_Operator_Returns201(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"op-device"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device: got %d", rec.Code)
	}
	deviceID := extractData(t, rec.Body.Bytes())["id"].(string)

	rec = httptest.NewRecorder()
	cmdBody := `{"payload":"ping","commandType":"ping"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+deviceID+"/commands", strings.NewReader(cmdBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(testCtx("op-1", []string{dto.RoleOperator}))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("enqueue operator: want 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestStatus_NoRole_ShouldBe403 asserts that a caller with no recognized role
// cannot retrieve device status.
// Gate: RequirePermissionForResource("id", PermDeviceRead()) + deviceAuthorizer deny for viewer.
func TestStatus_NoRole_ShouldBe403(t *testing.T) {
	r := initCellWithRouter(t)

	// Register a device.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"status-device"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device: got %d", rec.Code)
	}
	deviceID := extractData(t, rec.Body.Bytes())["id"].(string)

	// Caller has no recognized role — must be rejected.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceID+"/status", nil)
	req = req.WithContext(testCtx("intruder-1", []string{"viewer"}))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status no-role: want 403, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestStatus_DeviceRole_Returns200 verifies that a caller with RoleDevice can
// read status (device polling its own health).
func TestStatus_DeviceRole_Returns200(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"device-role-sensor"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device: got %d", rec.Code)
	}
	deviceID := extractData(t, rec.Body.Bytes())["id"].(string)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceID+"/status", nil)
	req = req.WithContext(testCtx(deviceID, []string{dto.RoleDevice}))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status device-role: want 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestStatus_OperatorRole_Returns200 verifies that RoleOperator can read status.
func TestStatus_OperatorRole_Returns200(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"op-status-sensor"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device: got %d", rec.Code)
	}
	deviceID := extractData(t, rec.Body.Bytes())["id"].(string)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceID+"/status", nil)
	req = req.WithContext(testCtx("operator-x", []string{dto.RoleOperator}))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status operator-role: want 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestDequeue_DeviceSelf_Returns200 verifies that a device can dequeue its own
// commands (subject == path {id}) via RequirePermissionForResource + deviceAuthorizer.
func TestDequeue_DeviceSelf_Returns200(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"dequeue-self-device"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device: got %d", rec.Code)
	}
	deviceID := extractData(t, rec.Body.Bytes())["id"].(string)

	// Device subject == path {id} → deviceAuthorizer device:consume owner rule passes.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceID+"/commands", nil)
	req = req.WithContext(testCtx(deviceID, nil))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("dequeue self: want 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestDequeue_NonOwner_ShouldBe403 asserts that a caller who is neither the
// device owner nor an admin/operator cannot dequeue commands.
// Gate: RequirePermissionForResource("id", PermDeviceConsume()) + deviceAuthorizer deny for non-owner/non-admin.
func TestDequeue_NonOwner_ShouldBe403(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"dequeue-other-device"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device: got %d", rec.Code)
	}
	deviceID := extractData(t, rec.Body.Bytes())["id"].(string)

	// Caller is "intruder-1", not the device and not admin/operator.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceID+"/commands", nil)
	req = req.WithContext(testCtx("intruder-1", []string{"viewer"}))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("dequeue non-owner: want 403, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestStatus_DeviceCrossRead_403 asserts that device-a's token cannot read
// device-b's status. RequirePermissionForResource("id", PermDeviceRead()) +
// deviceAuthorizer denies when subject != path {id} and caller lacks admin/operator role.
func TestStatus_DeviceCrossRead_403(t *testing.T) {
	r := initCellWithRouter(t)

	// Register device-b (the victim device whose status device-a should NOT read).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"device-b"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device-b: got %d", rec.Code)
	}
	deviceBID := extractData(t, rec.Body.Bytes())["id"].(string)

	// device-a token (subject="device-a") reads device-b's status — must be 403.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceBID+"/status", nil)
	req = req.WithContext(testCtx("device-a", []string{dto.RoleDevice}))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status cross-device read: want 403, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestStatus_DeviceSelf_200 asserts that a device can read its own status
// when subject == path {id} (deviceAuthorizer owner rule for device:read).
func TestStatus_DeviceSelf_200(t *testing.T) {
	r := initCellWithRouter(t)

	// Register the device.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"device-a"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device-a: got %d", rec.Code)
	}
	deviceAID := extractData(t, rec.Body.Bytes())["id"].(string)

	// device-a reads its own status — subject == path {id}, must be 200.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceAID+"/status", nil)
	req = req.WithContext(testCtx(deviceAID, []string{dto.RoleDevice}))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status self-read: want 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestStatus_Admin_AnyDevice_200 asserts that RoleAdmin can read any device's
// status regardless of path {id} (deviceAuthorizer admin bypass for device:read).
func TestStatus_Admin_AnyDevice_200(t *testing.T) {
	r := initCellWithRouter(t)

	// Register an arbitrary device.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"some-device"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device: got %d", rec.Code)
	}
	deviceID := extractData(t, rec.Body.Bytes())["id"].(string)

	// Admin reads any device's status — must be 200 regardless of subject.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceID+"/status", nil)
	req = req.WithContext(testCtx("admin-user", []string{dto.RoleAdmin}))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status admin-any-device: want 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestNoAuthorizer_FailClosed verifies that a principal-carrying request without
// an injected Authorizer is fail-closed (403) on protected endpoints.
// This mirrors the todoorder equivalent: absent Authorizer → PDP call fails →
// RequirePermission / RequirePermissionForResource return 403.
func TestNoAuthorizer_FailClosed(t *testing.T) {
	r := initCellWithRouter(t)

	// Register a device (public endpoint — no auth required).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"name":"failclosed-device"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: register device: got %d", rec.Code)
	}
	deviceID := extractData(t, rec.Body.Bytes())["id"].(string)

	// ctx has a principal but NO Authorizer injected — fail-closed on enqueue.
	recEnqueue := httptest.NewRecorder()
	reqEnqueue := httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+deviceID+"/commands",
		strings.NewReader(`{"payload":"reboot","commandType":"reboot"}`))
	reqEnqueue.Header.Set("Content-Type", "application/json")
	reqEnqueue = reqEnqueue.WithContext(auth.TestContext("admin-1", []string{dto.RoleAdmin}))
	r.ServeHTTP(recEnqueue, reqEnqueue)
	if recEnqueue.Code != http.StatusForbidden {
		t.Errorf("enqueue without authorizer: want 403 (fail-closed), got %d — body: %s",
			recEnqueue.Code, recEnqueue.Body.String())
	}

	// ctx has a principal but NO Authorizer injected — fail-closed on status.
	recStatus := httptest.NewRecorder()
	reqStatus := httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceID+"/status", nil)
	reqStatus = reqStatus.WithContext(auth.TestContext("admin-1", []string{dto.RoleAdmin}))
	r.ServeHTTP(recStatus, reqStatus)
	if recStatus.Code != http.StatusForbidden {
		t.Errorf("status without authorizer: want 403 (fail-closed), got %d — body: %s",
			recStatus.Code, recStatus.Body.String())
	}
}
