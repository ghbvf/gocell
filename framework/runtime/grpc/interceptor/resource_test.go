package interceptor

// resource_test.go — TDD coverage for #2207 per-message resource extraction
// (extractResourceFieldValue, extractResourceForUnary, resourceGatedStream).
//
// Cases:
//  1. extractResourceFieldValue: happy path (proto.Message with string UUID field)
//  2. extractResourceFieldValue: not a proto.Message → ("", false)
//  3. extractResourceFieldValue: field not found → ("", false)
//  4. extractResourceFieldValue: empty string value → ("", true) forwarded (HTTP parity; PDP rejects for
//     owner, admin still passes)
//  5. extractResourceFieldValue: non-canonical UUID value → (value, true) forwarded raw (HTTP parity;
//     non-UUID ids pass through for PDP comparison)
//  6. extractResourceFieldValue: uppercase UUID normalized to lowercase
//  7. extractResourceForUnary: no resource resolver → (fullMethod, nil)
//  8. extractResourceForUnary: resolver returns ok=false → (fullMethod, nil)
//  9. extractResourceForUnary: resolver ok, extraction succeeds → (uuid, nil)
// 10. extractResourceForUnary: resolver ok, extraction fails → ("", denyErr RESOURCE_UNRESOLVED)
// 11. UnaryAuth with resource extraction: happy path — PDP sees extracted UUID
// 12. UnaryAuth with resource extraction: extraction failure → PermissionDenied RESOURCE_UNRESOLVED
// 13. resourceGatedStream first RecvMsg: happy path — PDP sees extracted UUID
// 14. resourceGatedStream first RecvMsg: extraction failure → PermissionDenied RESOURCE_UNRESOLVED
// 15. resourceGatedStream: subsequent RecvMsg skips re-check (authorized=true)
// 16. WithResourceResolver nil is no-op

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

const (
	// testDeviceUUID is a valid lowercase canonical UUID used in resource extraction tests.
	testDeviceUUID = "550e8400-e29b-41d4-a716-446655440000"
)

// healthCheckReqWith returns a *grpc_health_v1.HealthCheckRequest with the
// given service value. The service field is a proto string field, making it
// suitable for UUID resource extraction tests.
func healthCheckReqWith(service string) *grpc_health_v1.HealthCheckRequest {
	return &grpc_health_v1.HealthCheckRequest{Service: service}
}

// --- extractResourceFieldValue -----------------------------------------------

// TestExtractResourceFieldValue_HappyPath verifies a valid proto.Message with a
// string field containing a canonical UUID returns the canonical value.
func TestExtractResourceFieldValue_HappyPath(t *testing.T) {
	t.Parallel()
	req := healthCheckReqWith(testDeviceUUID)
	got, ok := extractResourceFieldValue(req, "service")
	require.True(t, ok, "must succeed for a valid string UUID field")
	assert.Equal(t, testDeviceUUID, got)
}

// TestExtractResourceFieldValue_NotProtoMessage verifies a non-proto.Message
// argument returns ("", false).
func TestExtractResourceFieldValue_NotProtoMessage(t *testing.T) {
	t.Parallel()
	_, ok := extractResourceFieldValue("not-a-proto", "service")
	assert.False(t, ok, "non-proto.Message must return false")
}

// TestExtractResourceFieldValue_FieldNotFound verifies a non-existent field name
// returns ("", false).
func TestExtractResourceFieldValue_FieldNotFound(t *testing.T) {
	t.Parallel()
	req := healthCheckReqWith(testDeviceUUID)
	_, ok := extractResourceFieldValue(req, "nonexistent_field")
	assert.False(t, ok, "absent field must return false")
}

// TestExtractResourceFieldValue_EmptyValue verifies an empty string field is
// FORWARDED as "" (HTTP parity — the value is not a gate-level deny; the PDP rejects
// it for an owner but admin/operator still pass). Only structural failures deny.
func TestExtractResourceFieldValue_EmptyValue(t *testing.T) {
	t.Parallel()
	req := healthCheckReqWith("") // proto default = ""
	got, ok := extractResourceFieldValue(req, "service")
	require.True(t, ok, "empty field value is forwarded, not a structural failure")
	assert.Equal(t, "", got)
}

// TestExtractResourceFieldValue_NonCanonicalUUID verifies a non-UUID string is
// FORWARDED raw (HTTP parity — ParseCanonicalUUID only normalizes UUIDs; non-UUID ids
// pass through unchanged for the PDP to compare). Denying here would block admin.
func TestExtractResourceFieldValue_NonCanonicalUUID(t *testing.T) {
	t.Parallel()
	req := healthCheckReqWith("device-1")
	got, ok := extractResourceFieldValue(req, "service")
	require.True(t, ok, "non-UUID value is forwarded raw, not a structural failure")
	assert.Equal(t, "device-1", got)
}

// TestExtractResourceFieldValue_UppercaseUUID verifies that uppercase UUIDs are
// accepted and normalized to lowercase (ParseCanonicalUUID normalizes both forms).
func TestExtractResourceFieldValue_UppercaseUUID(t *testing.T) {
	t.Parallel()
	req := healthCheckReqWith("550E8400-E29B-41D4-A716-446655440000")
	got, ok := extractResourceFieldValue(req, "service")
	require.True(t, ok, "uppercase UUID must be accepted and normalized")
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", got, "must normalize to lowercase")
}

// --- extractResourceForUnary -------------------------------------------------

// TestExtractResourceForUnary_NoResolver verifies when cfg.resourceFor is nil
// the function returns (fullMethod, nil) — coarse behavior.
func TestExtractResourceForUnary_NoResolver(t *testing.T) {
	t.Parallel()
	cfg := authConfig{}
	req := healthCheckReqWith(testDeviceUUID)
	res, err := extractResourceForUnary(context.Background(), cfg, nil, "/svc/Method", req)
	require.NoError(t, err)
	assert.Equal(t, "/svc/Method", res, "no resolver → fullMethod as resource")
}

// TestExtractResourceForUnary_ResolverReturnsNotFound verifies when the resolver
// returns ok=false the function returns (fullMethod, nil) — coarse behavior.
func TestExtractResourceForUnary_ResolverReturnsNotFound(t *testing.T) {
	t.Parallel()
	cfg := authConfig{
		resourceFor: func(string) (string, bool) { return "", false },
	}
	req := healthCheckReqWith(testDeviceUUID)
	res, err := extractResourceForUnary(context.Background(), cfg, nil, "/svc/Method", req)
	require.NoError(t, err)
	assert.Equal(t, "/svc/Method", res, "resolver ok=false → fullMethod as resource")
}

// TestExtractResourceForUnary_SuccessfulExtraction verifies when extraction
// succeeds the canonical UUID is returned.
func TestExtractResourceForUnary_SuccessfulExtraction(t *testing.T) {
	t.Parallel()
	cfg := authConfig{
		resourceFor: func(string) (string, bool) { return "service", true },
	}
	req := healthCheckReqWith(testDeviceUUID)
	res, err := extractResourceForUnary(context.Background(), cfg, nil, "/svc/Method", req)
	require.NoError(t, err)
	assert.Equal(t, testDeviceUUID, res, "successful extraction must return the canonical UUID")
}

// TestExtractResourceForUnary_ExtractionFailure verifies F3 fail-closed:
// extraction failure → ("", RESOURCE_UNRESOLVED deny error).
func TestExtractResourceForUnary_ExtractionFailure(t *testing.T) {
	t.Parallel()
	// Structural failure: the declared resource field does not exist on the message.
	// (A non-UUID VALUE is forwarded, not denied — see extractResourceFieldValue.)
	cfg := authConfig{
		resourceFor: func(string) (string, bool) { return "nonexistent_field", true },
	}
	req := healthCheckReqWith(testDeviceUUID)
	res, denyErr := extractResourceForUnary(context.Background(), cfg, nil, "/svc/Method", req)
	assert.Equal(t, "", res)
	require.Error(t, denyErr)
	st, ok := status.FromError(denyErr)
	require.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st.Code())
	assert.Equal(t, msgGRPCResourceUnresolved, st.Message())
	ei := errorInfoOf(t, denyErr)
	assert.Equal(t, "RESOURCE_UNRESOLVED", ei.GetReason())
	// PII check: no resource value appears in metadata (only method + permission).
	assert.Equal(t, "/svc/Method", ei.GetMetadata()["method"])
	for _, v := range ei.GetMetadata() {
		assert.NotEqual(t, testDeviceUUID, v, "extracted value must not appear in ErrorInfo.Metadata")
	}
}

// --- UnaryAuth with resource extraction -------------------------------------

// TestUnaryAuth_ResourceExtraction_HappyPath verifies that an owner-scoped unary
// method forwards the extracted UUID to the PDP as the resource (not fullMethod).
func TestUnaryAuth_ResourceExtraction_HappyPath(t *testing.T) {
	t.Parallel()

	// Capture the resource argument received by Authorize.
	var capturedResource string
	authzDecision := mustAllow()
	authzCapture := captureResourceAuthorizer{decision: authzDecision, capture: &capturedResource}

	opts := []AuthOption{
		WithPermissionResolver(func(string) (authz.Permission, bool) {
			return authz.PermDeviceConsume(), true
		}),
		WithPDPAuthorizer(authzCapture),
		WithResourceResolver(func(string) (string, bool) { return "service", true }),
	}
	called := false
	handler := func(_ context.Context, _ any) (any, error) { called = true; return "ok", nil }
	req := healthCheckReqWith(testDeviceUUID)
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Watch"}

	iv := UnaryAuth(stubVerifier{claims: kauth.Claims{Subject: "sub-1"}}, opts...)
	_, err := iv(bearerCtx(), req, info, handler)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, testDeviceUUID, capturedResource,
		"PDP must receive the extracted UUID, not fullMethod")
}

// TestUnaryAuth_ResourceExtraction_Failure verifies that extraction failure
// returns RESOURCE_UNRESOLVED and the handler is not called.
func TestUnaryAuth_ResourceExtraction_Failure(t *testing.T) {
	t.Parallel()

	opts := []AuthOption{
		WithPermissionResolver(func(string) (authz.Permission, bool) {
			return authz.PermDeviceConsume(), true
		}),
		WithPDPAuthorizer(stubAuthorizer{dec: mustAllow()}),
		// Structural failure: declared resource field absent from the message.
		WithResourceResolver(func(string) (string, bool) { return "nonexistent_field", true }),
	}
	called := false
	handler := func(_ context.Context, _ any) (any, error) { called = true; return "ok", nil }
	req := healthCheckReqWith(testDeviceUUID)
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Watch"}

	iv := UnaryAuth(stubVerifier{claims: kauth.Claims{Subject: "sub-1"}}, opts...)
	_, err := iv(bearerCtx(), req, info, handler)
	require.Error(t, err)
	assert.False(t, called, "handler must not be called on extraction failure")
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st.Code())
	ei := errorInfoOf(t, err)
	assert.Equal(t, "RESOURCE_UNRESOLVED", ei.GetReason())
}

// captureResourceAuthorizer captures the resource argument passed to Authorize.
type captureResourceAuthorizer struct {
	decision authz.Decision
	capture  *string
}

func (a captureResourceAuthorizer) Authorize(_ context.Context, _, resource, _ string) (authz.Decision, error) {
	*a.capture = resource
	return a.decision, nil
}

// --- resourceGatedStream -----------------------------------------------------

// stubServerStream is a minimal grpc.ServerStream for unit tests.
type stubServerStream struct {
	grpc.ServerStream
	ctx      context.Context
	messages []any // messages returned by RecvMsg in sequence
	recvIdx  int
}

func (s *stubServerStream) Context() context.Context { return s.ctx }
func (s *stubServerStream) RecvMsg(m any) error {
	if s.recvIdx >= len(s.messages) {
		return io.EOF
	}
	if dst, ok := m.(*grpc_health_v1.HealthCheckRequest); ok {
		if src, ok2 := s.messages[s.recvIdx].(*grpc_health_v1.HealthCheckRequest); ok2 {
			// Field-wise copy (not *dst = *src) — the proto message embeds a
			// sync.Mutex via protoimpl.MessageState, so a value copy trips copylocks.
			dst.Service = src.Service
		}
	}
	s.recvIdx++
	return nil
}
func (s *stubServerStream) SendMsg(any) error { return nil }

// TestResourceGatedStream_FirstRecvMsg_HappyPath verifies the first RecvMsg
// extracts the resource, gates via PDP, marks authorized=true.
func TestResourceGatedStream_FirstRecvMsg_HappyPath(t *testing.T) {
	t.Parallel()

	var capturedResource string
	authzCapture := captureResourceAuthorizer{
		decision: mustAllow(),
		capture:  &capturedResource,
	}
	p := &auth.Principal{Subject: "user-1"}
	cfg := authConfig{
		permissionFor: func(string) (authz.Permission, bool) { return authz.PermDeviceConsume(), true },
		authorizer:    authzCapture,
		resourceFor:   func(string) (string, bool) { return "service", true },
	}

	inner := &stubServerStream{
		ctx:      bearerCtx(),
		messages: []any{healthCheckReqWith(testDeviceUUID)},
	}
	rgs := &resourceGatedStream{
		ServerStream: inner,
		ctx:          bearerCtx(),
		cfg:          cfg,
		p:            p,
		fullMethod:   "/pkg.Svc/Watch",
		fieldName:    "service",
	}

	msg := &grpc_health_v1.HealthCheckRequest{}
	err := rgs.RecvMsg(msg)
	require.NoError(t, err)
	assert.Equal(t, testDeviceUUID, capturedResource,
		"PDP must receive the extracted UUID on first RecvMsg")
	assert.True(t, rgs.authorized, "authorized must be true after successful first RecvMsg")
	assert.Equal(t, testDeviceUUID, msg.GetService(), "message must be populated")
}

// TestResourceGatedStream_FirstRecvMsg_ExtractionFailure verifies first RecvMsg
// returns RESOURCE_UNRESOLVED when extraction fails (F3 fail-closed).
func TestResourceGatedStream_FirstRecvMsg_ExtractionFailure(t *testing.T) {
	t.Parallel()

	p := &auth.Principal{Subject: "user-1"}
	// Structural failure: declared resource field absent from the message (a non-UUID
	// VALUE would be forwarded, not denied — see extractResourceFieldValue).
	cfg := authConfig{
		permissionFor: func(string) (authz.Permission, bool) { return authz.PermDeviceConsume(), true },
		authorizer:    stubAuthorizer{dec: mustAllow()},
		resourceFor:   func(string) (string, bool) { return "nonexistent_field", true },
	}

	inner := &stubServerStream{
		ctx:      bearerCtx(),
		messages: []any{healthCheckReqWith(testDeviceUUID)},
	}
	rgs := &resourceGatedStream{
		ServerStream: inner,
		ctx:          bearerCtx(),
		cfg:          cfg,
		p:            p,
		fullMethod:   "/pkg.Svc/Watch",
		fieldName:    "nonexistent_field",
	}

	msg := &grpc_health_v1.HealthCheckRequest{}
	err := rgs.RecvMsg(msg)
	require.Error(t, err)
	assert.False(t, rgs.authorized)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st.Code())
	ei := errorInfoOf(t, err)
	assert.Equal(t, "RESOURCE_UNRESOLVED", ei.GetReason())
}

// TestResourceGatedStream_SubsequentRecvMsg_SkipsRecheck verifies that after
// authorized=true, subsequent RecvMsg calls do not re-invoke the PDP.
func TestResourceGatedStream_SubsequentRecvMsg_SkipsRecheck(t *testing.T) {
	t.Parallel()

	pdpCallCount := 0
	authzCount := countingAuthorizer{decision: mustAllow(), count: &pdpCallCount}
	p := &auth.Principal{Subject: "user-1"}
	cfg := authConfig{
		permissionFor: func(string) (authz.Permission, bool) { return authz.PermDeviceConsume(), true },
		authorizer:    authzCount,
		resourceFor:   func(string) (string, bool) { return "service", true },
	}

	inner := &stubServerStream{
		ctx: bearerCtx(),
		messages: []any{
			healthCheckReqWith(testDeviceUUID),
			healthCheckReqWith(testDeviceUUID),
		},
	}
	rgs := &resourceGatedStream{
		ServerStream: inner,
		ctx:          bearerCtx(),
		cfg:          cfg,
		p:            p,
		fullMethod:   "/pkg.Svc/Watch",
		fieldName:    "service",
	}

	msg := &grpc_health_v1.HealthCheckRequest{}
	require.NoError(t, rgs.RecvMsg(msg)) // first: extracts + PDP
	require.NoError(t, rgs.RecvMsg(msg)) // second: passthrough
	assert.Equal(t, 1, pdpCallCount, "PDP must only be called once (on the first RecvMsg)")
}

// countingAuthorizer counts calls to Authorize.
type countingAuthorizer struct {
	decision authz.Decision
	count    *int
}

func (a countingAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	*a.count++
	return a.decision, nil
}

// TestWithResourceResolver_NilNoOp verifies that a nil resolver is a no-op (does
// not overwrite an existing resolver).
func TestWithResourceResolver_NilNoOp(t *testing.T) {
	t.Parallel()
	cfg := authConfig{resourceFor: func(string) (string, bool) { return "x", true }}
	WithResourceResolver(nil)(&cfg)
	_, ok := cfg.resourceFor("m")
	assert.True(t, ok, "nil resolver option must not overwrite existing resolver")
}

// Compile-time check: ResourceResolver function signature is stable.
var _ ResourceResolver = func(string) (string, bool) { return "", false }

// --- StreamAuth owner-scoped end-to-end (regression for the open-gate bug) ------

// ownershipAuthorizer mimics the iotdevice device authorizer's owner-scoped rule
// (subject == resource → allow; else deny). It is the discriminator that catches
// the #2207 double-gate regression: the coarse open-time call passes resource =
// fullMethod (≠ subject → DENY); only the deferred per-message call passes resource
// = the extracted device UUID (== subject → ALLOW).
type ownershipAuthorizer struct{}

func (ownershipAuthorizer) Authorize(_ context.Context, subject, resource, _ string) (authz.Decision, error) {
	if subject != "" && subject == resource {
		return authz.Allow(authz.Obligations{})
	}
	return authz.Deny("ownership: subject != resource"), nil
}

// TestStreamAuth_OwnerScoped_DefersGateToFirstRecvMsg guards the core #2207
// invariant: for an owner-scoped streaming method the coarse (fullMethod) gate MUST
// NOT run at stream open — it would deny the device (subject != fullMethod) before
// the per-message check. Only full deferral to the first RecvMsg (resource =
// extracted device UUID) lets the device through. With the open-time coarse gate
// present (the bug), this test fails: the device is denied before RecvMsg.
//
// Additionally asserts: PDP is called exactly ONCE and with resource == testDeviceUUID
// (the deferred gate forwards the extracted id, not fullMethod).
func TestStreamAuth_OwnerScoped_DefersGateToFirstRecvMsg(t *testing.T) {
	t.Parallel()

	var capturedResource string
	pdpCallCount := 0
	authzCapture := captureResourceAuthorizer{
		decision: mustAllow(),
		capture:  &capturedResource,
	}
	// Wrap captureResourceAuthorizer with a counting layer.
	authzCountCapture := countingCaptureAuthorizer{
		inner: authzCapture,
		count: &pdpCallCount,
	}

	var handlerReached bool
	handler := func(_ any, ss grpc.ServerStream) error {
		// The generated server-streaming handler Recvs the single request before
		// invoking the user handler; emulate that so the deferred gate runs.
		if err := ss.RecvMsg(&grpc_health_v1.HealthCheckRequest{}); err != nil {
			return err
		}
		handlerReached = true
		return nil
	}
	ss := &stubServerStream{ctx: bearerCtx(), messages: []any{healthCheckReqWith(testDeviceUUID)}}

	err := StreamAuth(
		stubVerifier{claims: kauth.Claims{Subject: testDeviceUUID}},
		WithPermissionResolver(permResolverFor(streamMethod)),
		WithResourceResolver(func(string) (string, bool) { return "service", true }),
		WithPDPAuthorizer(authzCountCapture),
	)(nil, ss, streamInfo(), handler)

	require.NoError(t, err, "owner-scoped stream must NOT be denied by an open-time coarse gate")
	assert.True(t, handlerReached, "handler must be reached after the deferred per-message gate allows")
	assert.Equal(t, 1, pdpCallCount, "PDP must be called exactly once (deferred to first RecvMsg)")
	assert.Equal(t, testDeviceUUID, capturedResource,
		"PDP resource must be the extracted device UUID, not fullMethod")
}

// countingCaptureAuthorizer counts calls and delegates to an inner
// captureResourceAuthorizer.
type countingCaptureAuthorizer struct {
	inner captureResourceAuthorizer
	count *int
}

func (a countingCaptureAuthorizer) Authorize(ctx context.Context, subject, resource, action string) (authz.Decision, error) {
	*a.count++
	return a.inner.Authorize(ctx, subject, resource, action)
}

// TestStreamAuth_OwnerScoped_CrossDeviceDenied verifies a device watching ANOTHER
// device's queue is denied at the first RecvMsg (subject != extracted resource),
// proving the deferred per-message gate is actually enforced (not merely skipped).
func TestStreamAuth_OwnerScoped_CrossDeviceDenied(t *testing.T) {
	t.Parallel()

	handler := func(_ any, ss grpc.ServerStream) error {
		return ss.RecvMsg(&grpc_health_v1.HealthCheckRequest{})
	}
	const otherSubject = "11111111-1111-1111-1111-111111111111"
	ss := &stubServerStream{ctx: bearerCtx(), messages: []any{healthCheckReqWith(testDeviceUUID)}}

	err := StreamAuth(
		stubVerifier{claims: kauth.Claims{Subject: otherSubject}},
		WithPermissionResolver(permResolverFor(streamMethod)),
		WithResourceResolver(func(string) (string, bool) { return "service", true }),
		WithPDPAuthorizer(ownershipAuthorizer{}),
	)(nil, ss, streamInfo(), handler)

	require.Error(t, err, "device watching another device's queue must be denied")
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st.Code())
}
