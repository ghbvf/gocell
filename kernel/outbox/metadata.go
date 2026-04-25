package outbox

// cloneMetadata returns an independent copy of metadata so callers can
// mutate the result without affecting the source. Nil input returns a
// freshly allocated empty map, which lets callers write unconditionally
// (no nil guard at every write site).
//
// The result has capacity for three extra keys so the common pattern of
// merging extra keys on top does not reallocate.
//
// Concurrency: cloneMetadata is safe for concurrent use. The returned map
// is not — callers own it fully and are responsible for any further
// synchronization.
func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return make(map[string]string, 3)
	}
	cloned := make(map[string]string, len(metadata)+3)
	for k, v := range metadata {
		cloned[k] = v
	}
	return cloned
}
