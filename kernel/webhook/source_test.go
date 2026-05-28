package webhook

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestSourceRegistry(t *testing.T) {
	t.Parallel()
	reg := NewSourceRegistry()

	_, ok := reg.Lookup(MustSourceID("absent"))
	assert.False(t, ok, "empty registry has no sources")

	src, err := NewSource(MustSourceID("stripe"), minSecret())
	require.NoError(t, err)
	require.NoError(t, reg.Register(src))

	got, ok := reg.Lookup(MustSourceID("stripe"))
	require.True(t, ok)
	assert.Equal(t, SourceID("stripe"), got.ID())

	// Re-register replaces.
	src2, err := NewSource(MustSourceID("stripe"), append(minSecret(), 'z'))
	require.NoError(t, err)
	require.NoError(t, reg.Register(src2))
	got2, ok := reg.Lookup(MustSourceID("stripe"))
	require.True(t, ok)
	assert.Equal(t, SourceID("stripe"), got2.ID())
}

func TestSourceRegistry_RejectsEmptySecret(t *testing.T) {
	t.Parallel()
	reg := NewSourceRegistry()
	err := reg.Register(Source{})
	requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
}

// TestSourceRegistry_ConcurrentAccess surfaces data races under -race by
// running concurrent Register and Lookup calls on a shared registry.
func TestSourceRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	reg := NewSourceRegistry()
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("src-%d", i)
			src, err := NewSource(MustSourceID(id), minSecret())
			if err != nil {
				return
			}
			if i%2 == 0 {
				_ = reg.Register(src)
			} else {
				_, _ = reg.Lookup(MustSourceID(fmt.Sprintf("src-%d", i-1)))
			}
		}()
	}
	wg.Wait()
}
