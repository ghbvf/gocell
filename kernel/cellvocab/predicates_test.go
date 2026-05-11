package cellvocab_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/cellvocab"
)

func TestValidRolesForKind(t *testing.T) {
	tests := []struct {
		kind cellvocab.ContractKind
		want []cellvocab.ContractRole
	}{
		{cellvocab.ContractHTTP, []cellvocab.ContractRole{cellvocab.RoleServe, cellvocab.RoleCall}},
		{cellvocab.ContractEvent, []cellvocab.ContractRole{cellvocab.RolePublish, cellvocab.RoleSubscribe}},
		{cellvocab.ContractCommand, []cellvocab.ContractRole{cellvocab.RoleHandle, cellvocab.RoleInvoke}},
		{cellvocab.ContractProjection, []cellvocab.ContractRole{cellvocab.RoleProvide, cellvocab.RoleRead}},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			got := cellvocab.ValidRolesForKind(tt.kind)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidRolesForKindUnknown(t *testing.T) {
	got := cellvocab.ValidRolesForKind(cellvocab.ContractKind("unknown"))
	assert.Nil(t, got)
}

func TestIsProviderRole(t *testing.T) {
	tests := []struct {
		role cellvocab.ContractRole
		want bool
	}{
		{cellvocab.RoleServe, true},
		{cellvocab.RolePublish, true},
		{cellvocab.RoleHandle, true},
		{cellvocab.RoleProvide, true},
		{cellvocab.RoleCall, false},
		{cellvocab.RoleSubscribe, false},
		{cellvocab.RoleInvoke, false},
		{cellvocab.RoleRead, false},
		{cellvocab.ContractRole("unknown"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			assert.Equal(t, tt.want, cellvocab.IsProviderRole(tt.role))
		})
	}
}

func TestIsConsumerRole(t *testing.T) {
	tests := []struct {
		role cellvocab.ContractRole
		want bool
	}{
		{cellvocab.RoleCall, true},
		{cellvocab.RoleSubscribe, true},
		{cellvocab.RoleInvoke, true},
		{cellvocab.RoleRead, true},
		{cellvocab.RoleServe, false},
		{cellvocab.RolePublish, false},
		{cellvocab.RoleHandle, false},
		{cellvocab.RoleProvide, false},
		{cellvocab.ContractRole("unknown"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			assert.Equal(t, tt.want, cellvocab.IsConsumerRole(tt.role))
		})
	}
}
