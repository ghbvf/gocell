package config

import "strings"

// KeyFilter checks whether any of a set of changed keys matches registered
// prefixes. This is a utility type for bootstrap-level filtering — the watcher
// itself does not apply key filtering (it watches files, not config keys).
type KeyFilter struct {
	prefixes []string
}

// NewKeyFilter creates a KeyFilter for the given key prefixes.
func NewKeyFilter(prefixes ...string) *KeyFilter {
	return &KeyFilter{prefixes: prefixes}
}

// Matches returns true if any key in keys has any of the registered prefixes.
// An empty filter (no prefixes) matches everything. An empty keys list matches
// nothing (unless the filter is also empty).
func (f *KeyFilter) Matches(keys []string) bool {
	if len(f.prefixes) == 0 {
		return true
	}
	for _, key := range keys {
		for _, prefix := range f.prefixes {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		}
	}
	return false
}
