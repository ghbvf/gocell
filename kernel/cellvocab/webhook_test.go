package cellvocab_test

// webhook_test.go adds the webhook ContractKind + webhook-receive/webhook-dispatch
// ContractRole assertions. Extend the existing table-driven tests by mirroring the
// existing test style.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestContractKindWebhookValue asserts the new constant value is "webhook".
func TestContractKindWebhookValue(t *testing.T) {
	assert.Equal(t, cellvocab.ContractKind("webhook"), cellvocab.ContractWebhook)
}

// TestContractRoleWebhookValues asserts the new role constant values.
func TestContractRoleWebhookValues(t *testing.T) {
	assert.Equal(t, cellvocab.ContractRole("webhook-receive"), cellvocab.RoleWebhookReceive)
	assert.Equal(t, cellvocab.ContractRole("webhook-dispatch"), cellvocab.RoleWebhookDispatch)
}

// TestParseContractKindWebhookRoundTrip verifies that ParseContractKind("webhook")
// returns ContractWebhook with no error, and that string round-trips correctly.
func TestParseContractKindWebhookRoundTrip(t *testing.T) {
	got, err := cellvocab.ParseContractKind("webhook")
	require.NoError(t, err)
	assert.Equal(t, cellvocab.ContractWebhook, got)
	assert.Equal(t, "webhook", string(got))
}

// TestParseContractRoleWebhookRoundTrip verifies that ParseContractRole
// round-trips for both webhook roles.
func TestParseContractRoleWebhookRoundTrip(t *testing.T) {
	tests := []struct {
		input string
		want  cellvocab.ContractRole
	}{
		{"webhook-receive", cellvocab.RoleWebhookReceive},
		{"webhook-dispatch", cellvocab.RoleWebhookDispatch},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := cellvocab.ParseContractRole(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.input, string(got))
		})
	}
}

// TestParseContractKindRejectsUnknownIncludingWebhookSimilar verifies that
// ParseContractKind still rejects unrecognised input after the webhook kind is
// added. Tests both near-miss names and truly unknown values.
func TestParseContractKindRejectsUnknownIncludingWebhookSimilar(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"uppercase webhook", "Webhook"},
		{"grpc — unrelated unknown", "grpc"},
		{"websocket — near-miss", "websocket"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cellvocab.ParseContractKind(tt.input)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
		})
	}
}

// TestParseContractRoleRejectsUnknownIncludingWebhookSimilar verifies that
// ParseContractRole still rejects unrecognised input after the webhook roles
// are added.
func TestParseContractRoleRejectsUnknownIncludingWebhookSimilar(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"uppercase Webhook-Receive", "Webhook-Receive"},
		{"near-miss webhook-send", "webhook-send"},
		{"near-miss receive", "receive"},
		{"near-miss dispatch", "dispatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cellvocab.ParseContractRole(tt.input)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
		})
	}
}

// TestValidRolesForKindWebhook asserts that ValidRolesForKind(ContractWebhook)
// returns exactly [RoleWebhookReceive, RoleWebhookDispatch].
func TestValidRolesForKindWebhook(t *testing.T) {
	got := cellvocab.ValidRolesForKind(cellvocab.ContractWebhook)
	assert.Equal(t, []cellvocab.ContractRole{cellvocab.RoleWebhookReceive, cellvocab.RoleWebhookDispatch}, got)
}

// TestIsProviderRoleWebhookDispatch asserts that RoleWebhookDispatch is a provider
// role (outbound dispatch) and RoleWebhookReceive is not.
func TestIsProviderRoleWebhookDispatch(t *testing.T) {
	assert.True(t, cellvocab.IsProviderRole(cellvocab.RoleWebhookDispatch),
		"RoleWebhookDispatch must be a provider role")
	assert.False(t, cellvocab.IsProviderRole(cellvocab.RoleWebhookReceive),
		"RoleWebhookReceive must NOT be a provider role")
}

// TestIsConsumerRoleWebhookReceive asserts that RoleWebhookReceive is a consumer
// role (inbound receive) and RoleWebhookDispatch is not.
func TestIsConsumerRoleWebhookReceive(t *testing.T) {
	assert.True(t, cellvocab.IsConsumerRole(cellvocab.RoleWebhookReceive),
		"RoleWebhookReceive must be a consumer role")
	assert.False(t, cellvocab.IsConsumerRole(cellvocab.RoleWebhookDispatch),
		"RoleWebhookDispatch must NOT be a consumer role")
}

// TestValidRolesForKindAll is an extension of the existing predicates_test.go
// table, now including the webhook kind so the table exhaustively covers all kinds.
func TestValidRolesForKindAll(t *testing.T) {
	tests := []struct {
		kind cellvocab.ContractKind
		want []cellvocab.ContractRole
	}{
		// Existing kinds (mirrors predicates_test.go — included here for exhaustive coverage)
		{cellvocab.ContractHTTP, []cellvocab.ContractRole{cellvocab.RoleServe, cellvocab.RoleCall}},
		{cellvocab.ContractEvent, []cellvocab.ContractRole{cellvocab.RolePublish, cellvocab.RoleSubscribe}},
		{cellvocab.ContractCommand, []cellvocab.ContractRole{cellvocab.RoleHandle, cellvocab.RoleInvoke}},
		{cellvocab.ContractProjection, []cellvocab.ContractRole{cellvocab.RoleProvide, cellvocab.RoleRead}},
		// New webhook kind
		{cellvocab.ContractWebhook, []cellvocab.ContractRole{cellvocab.RoleWebhookReceive, cellvocab.RoleWebhookDispatch}},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			got := cellvocab.ValidRolesForKind(tt.kind)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestIsProviderRoleAllWebhook extends the existing IsProviderRole table with
// the two webhook roles so the classification is tested exhaustively.
func TestIsProviderRoleAllWebhook(t *testing.T) {
	tests := []struct {
		role cellvocab.ContractRole
		want bool
	}{
		// webhook roles
		{cellvocab.RoleWebhookDispatch, true},
		{cellvocab.RoleWebhookReceive, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			assert.Equal(t, tt.want, cellvocab.IsProviderRole(tt.role))
		})
	}
}

// TestIsConsumerRoleAllWebhook extends the existing IsConsumerRole table.
func TestIsConsumerRoleAllWebhook(t *testing.T) {
	tests := []struct {
		role cellvocab.ContractRole
		want bool
	}{
		// webhook roles
		{cellvocab.RoleWebhookReceive, true},
		{cellvocab.RoleWebhookDispatch, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			assert.Equal(t, tt.want, cellvocab.IsConsumerRole(tt.role))
		})
	}
}
