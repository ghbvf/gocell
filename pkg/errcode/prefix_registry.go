package errcode

// PrefixOwner is a snapshot entry from the prefix ownership registry.
// Prefix is the registered prefix string (e.g. "ERR_AUTH_" or "ERR_INTERNAL");
// Owner is the module path that owns that namespace (e.g. "github.com/ghbvf/gocell").
type PrefixOwner struct {
	Prefix string
	Owner  string
}

// RegisterPrefix is a stub — replaced in prefix_registry.go (GREEN commit).
func RegisterPrefix(prefix, owner string) {}

// RegisteredPrefixes is a stub — replaced in prefix_registry.go (GREEN commit).
func RegisteredPrefixes() []PrefixOwner { return nil }

// OwnerOfCode is a stub — replaced in prefix_registry.go (GREEN commit).
func OwnerOfCode(code Code) (string, bool) { return "", false }
