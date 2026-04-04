package cell

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidRolesForKind(t *testing.T) {
	tests := []struct {
		kind ContractKind
		want []ContractRole
	}{
		{ContractHTTP, []ContractRole{RoleServe, RoleCall}},
		{ContractEvent, []ContractRole{RolePublish, RoleSubscribe}},
		{ContractCommand, []ContractRole{RoleHandle, RoleInvoke}},
		{ContractProjection, []ContractRole{RoleProvide, RoleRead}},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			got := ValidRolesForKind(tt.kind)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidRolesForKindUnknown(t *testing.T) {
	got := ValidRolesForKind(ContractKind("unknown"))
	assert.Nil(t, got)
}

func TestIsProviderRole(t *testing.T) {
	tests := []struct {
		role ContractRole
		want bool
	}{
		{RoleServe, true},
		{RolePublish, true},
		{RoleHandle, true},
		{RoleProvide, true},
		{RoleCall, false},
		{RoleSubscribe, false},
		{RoleInvoke, false},
		{RoleRead, false},
		{ContractRole("unknown"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			assert.Equal(t, tt.want, IsProviderRole(tt.role))
		})
	}
}

func TestIsConsumerRole(t *testing.T) {
	tests := []struct {
		role ContractRole
		want bool
	}{
		{RoleCall, true},
		{RoleSubscribe, true},
		{RoleInvoke, true},
		{RoleRead, true},
		{RoleServe, false},
		{RolePublish, false},
		{RoleHandle, false},
		{RoleProvide, false},
		{ContractRole("unknown"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			assert.Equal(t, tt.want, IsConsumerRole(tt.role))
		})
	}
}
