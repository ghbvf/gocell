package domain

// Permission represents a single allowed action on a resource.
type Permission struct {
	Resource string
	Action   string
}

// Role groups a set of permissions under a named identity.
type Role struct {
	ID          string
	Name        string
	Permissions []Permission
}

// HasPermission returns true if the role contains the exact resource+action pair.
func (r *Role) HasPermission(resource, action string) bool {
	for _, p := range r.Permissions {
		if p.Resource == resource && p.Action == action {
			return true
		}
	}
	return false
}
