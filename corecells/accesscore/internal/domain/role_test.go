package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRole_HasPermission(t *testing.T) {
	role := &Role{
		ID:   "r-1",
		Name: "admin",
		Permissions: []Permission{
			{Resource: "users", Action: "read"},
			{Resource: "users", Action: "write"},
			{Resource: "config", Action: "read"},
		},
	}

	tests := []struct {
		name     string
		resource string
		action   string
		want     bool
	}{
		{
			name:     "has exact permission",
			resource: "users",
			action:   "read",
			want:     true,
		},
		{
			name:     "has another permission",
			resource: "users",
			action:   "write",
			want:     true,
		},
		{
			name:     "wrong action",
			resource: "users",
			action:   "delete",
			want:     false,
		},
		{
			name:     "wrong resource",
			resource: "audit",
			action:   "read",
			want:     false,
		},
		{
			name:     "both wrong",
			resource: "audit",
			action:   "delete",
			want:     false,
		},
		{
			name:     "cross-resource match",
			resource: "config",
			action:   "read",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, role.HasPermission(tt.resource, tt.action))
		})
	}
}

func TestRole_HasPermission_EmptyPermissions(t *testing.T) {
	role := &Role{
		ID:          "r-2",
		Name:        "viewer",
		Permissions: nil,
	}
	assert.False(t, role.HasPermission("any", "read"))
}
