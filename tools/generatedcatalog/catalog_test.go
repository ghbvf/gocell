package generatedcatalog

import (
	"go/format"
	"strings"
	"testing"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmitFileDeterministicGo(t *testing.T) {
	t.Parallel()

	g := kerneldepgraph.FromNodes("github.com/example/mod", []*kerneldepgraph.Node{
		{
			ID:      "github.com/example/mod/pkg/a",
			Layer:   "pkg",
			Imports: []string{"github.com/example/mod/pkg/b", "fmt"},
		},
		{
			ID:      "github.com/example/mod/pkg/b",
			Layer:   "pkg",
			CellID:  "testcell",
			SliceID: "testslice",
		},
		{
			ID:       "github.com/example/mod/internal/x",
			Layer:    "kernel",
			TestOnly: true,
		},
	})

	first, err := EmitFile("main", "github.com/example/mod", g)
	require.NoError(t, err)
	second, err := EmitFile("main", "github.com/example/mod", g)
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.NotContains(t, string(first), "// generated:")
	assert.Contains(t, string(first), "// source: github.com/example/mod")
	assert.True(t, strings.HasPrefix(string(first), "// Code generated"))
	_, err = format.Source(first)
	require.NoError(t, err)
}
