package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_YAMLOnly(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	content := `
server:
  host: localhost
  port: 8080
db:
  name: testdb
`
	require.NoError(t, os.WriteFile(yamlFile, []byte(content), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)

	assert.Equal(t, "localhost", cfg.Get("server.host"))
	assert.Equal(t, 8080, cfg.Get("server.port"))
	assert.Equal(t, "testdb", cfg.Get("db.name"))
	assert.Nil(t, cfg.Get("nonexistent"))
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	content := `
server:
  port: 8080
`
	require.NoError(t, os.WriteFile(yamlFile, []byte(content), 0o644))

	t.Setenv("MYAPP_SERVER_PORT", "9090")

	cfg, err := Load(yamlFile, "MYAPP")
	require.NoError(t, err)

	// Env override should take precedence.
	assert.Equal(t, "9090", cfg.Get("server.port"))
}

func TestLoad_NoYAML(t *testing.T) {
	t.Setenv("TESTCFG_DB_HOST", "pg.local")

	cfg, err := Load("", "TESTCFG")
	require.NoError(t, err)

	assert.Equal(t, "pg.local", cfg.Get("db.host"))
}

func TestNewFromMap(t *testing.T) {
	data := map[string]any{
		"a": map[string]any{
			"b": "val",
		},
		"c": 42,
	}
	cfg := NewFromMap(data)

	assert.Equal(t, "val", cfg.Get("a.b"))
	assert.Equal(t, 42, cfg.Get("c"))
}

func TestConfig_Keys(t *testing.T) {
	data := map[string]any{
		"z": 1,
		"a": map[string]any{
			"b": 2,
			"c": 3,
		},
	}
	cfg := NewFromMap(data)

	keys := cfg.Keys()
	assert.Equal(t, []string{"a.b", "a.c", "z"}, keys)
}

func TestConfig_Scan(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	content := `
server:
  host: localhost
  port: 8080
`
	require.NoError(t, os.WriteFile(yamlFile, []byte(content), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)

	var dest struct {
		Server struct {
			Host string `yaml:"host"`
			Port int    `yaml:"port"`
		} `yaml:"server"`
	}
	require.NoError(t, cfg.Scan(&dest))
	assert.Equal(t, "localhost", dest.Server.Host)
	assert.Equal(t, 8080, dest.Server.Port)
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte(":::invalid"), 0o644))

	_, err := Load(yamlFile, "")
	assert.Error(t, err)
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml", "")
	assert.Error(t, err)
}

func TestConfig_Reload(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: val1\n"), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)
	assert.Equal(t, "val1", cfg.Get("key"))

	// Modify file and reload.
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: val2\nnew_key: added\n"), 0o644))

	c := cfg.(*config)
	require.NoError(t, c.Reload(yamlFile, ""))

	assert.Equal(t, "val2", cfg.Get("key"))
	assert.Equal(t, "added", cfg.Get("new_key"))
}

func TestConfig_Reload_InvalidFile(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: val\n"), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)

	c := cfg.(*config)
	err = c.Reload("/nonexistent/file.yaml", "")
	assert.Error(t, err)

	// Original data should be unchanged after failed reload.
	assert.Equal(t, "val", cfg.Get("key"))
}

func TestConfig_Reload_WithEnvOverride(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("db:\n  host: localhost\n"), 0o644))

	t.Setenv("RL_DB_HOST", "override-host")

	cfg, err := Load(yamlFile, "RL")
	require.NoError(t, err)
	assert.Equal(t, "override-host", cfg.Get("db.host"))

	// Reload should also pick up env.
	c := cfg.(*config)
	require.NoError(t, c.Reload(yamlFile, "RL"))
	assert.Equal(t, "override-host", cfg.Get("db.host"))
}

func TestConfig_SetNested_OverwriteNonMap(t *testing.T) {
	// Test setNested when intermediate value is not a map.
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("flat: scalar\n"), 0o644))

	t.Setenv("SN_FLAT_DEEP", "nested-val")

	cfg, err := Load(yamlFile, "SN")
	require.NoError(t, err)
	assert.Equal(t, "nested-val", cfg.Get("flat.deep"))
}

func TestConfig_Generation(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: val1\n"), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)

	gen, ok := cfg.(Generationer)
	require.True(t, ok, "config must implement Generationer")

	// Initial generation is 0.
	assert.Equal(t, int64(0), gen.Generation())

	// Successful reload increments generation.
	c := cfg.(*config)
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: val2\n"), 0o644))
	require.NoError(t, c.Reload(yamlFile, ""))
	assert.Equal(t, int64(1), gen.Generation())

	// Second reload increments again.
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: val3\n"), 0o644))
	require.NoError(t, c.Reload(yamlFile, ""))
	assert.Equal(t, int64(2), gen.Generation())

	// Failed reload does NOT increment.
	err = c.Reload("/nonexistent/path.yaml", "")
	require.Error(t, err)
	assert.Equal(t, int64(2), gen.Generation(), "failed reload must not increment generation")
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name    string
		old     map[string]any
		new     map[string]any
		added   []string
		updated []string
		removed []string
	}{
		{
			name:    "added keys only",
			old:     map[string]any{},
			new:     map[string]any{"a": 1, "b": "two"},
			added:   []string{"a", "b"},
			updated: nil,
			removed: nil,
		},
		{
			name:    "removed keys only",
			old:     map[string]any{"a": 1, "b": "two"},
			new:     map[string]any{},
			added:   nil,
			updated: nil,
			removed: []string{"a", "b"},
		},
		{
			name:    "updated keys only",
			old:     map[string]any{"a": 1, "b": "old"},
			new:     map[string]any{"a": 2, "b": "new"},
			added:   nil,
			updated: []string{"a", "b"},
			removed: nil,
		},
		{
			name:    "mixed changes",
			old:     map[string]any{"keep": "same", "update": "old", "remove": "gone"},
			new:     map[string]any{"keep": "same", "update": "new", "add": "fresh"},
			added:   []string{"add"},
			updated: []string{"update"},
			removed: []string{"remove"},
		},
		{
			name:    "no changes",
			old:     map[string]any{"a": 1, "b": "two"},
			new:     map[string]any{"a": 1, "b": "two"},
			added:   nil,
			updated: nil,
			removed: nil,
		},
		{
			name:    "both nil",
			old:     nil,
			new:     nil,
			added:   nil,
			updated: nil,
			removed: nil,
		},
		{
			name:    "old nil new populated",
			old:     nil,
			new:     map[string]any{"a": 1},
			added:   []string{"a"},
			updated: nil,
			removed: nil,
		},
		{
			name:    "old populated new nil",
			old:     map[string]any{"a": 1},
			new:     nil,
			added:   nil,
			updated: nil,
			removed: []string{"a"},
		},
		{
			name:    "type change int to string detected as update",
			old:     map[string]any{"port": 8080},
			new:     map[string]any{"port": "8080"},
			added:   nil,
			updated: []string{"port"}, // reflect.DeepEqual distinguishes int from string
			removed: nil,
		},
		{
			name:    "value change across types",
			old:     map[string]any{"port": 8080},
			new:     map[string]any{"port": "9090"},
			added:   nil,
			updated: []string{"port"},
			removed: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, updated, removed := Diff(tt.old, tt.new)
			assert.Equal(t, tt.added, added, "added")
			assert.Equal(t, tt.updated, updated, "updated")
			assert.Equal(t, tt.removed, removed, "removed")
		})
	}
}

func TestConfig_Snapshot(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("a: 1\nb: two\n"), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)

	snap := cfg.(Snapshotter).Snapshot()
	assert.Equal(t, map[string]any{"a": 1, "b": "two"}, snap)

	// Mutations to the snapshot should not affect the config.
	snap["a"] = 999
	assert.Equal(t, 1, cfg.Get("a"))
}

func TestSnapshot_SliceIsolation(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("tags:\n  - alpha\n  - beta\n"), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)

	snap := cfg.(Snapshotter).Snapshot()

	// Mutate the slice in the snapshot.
	tags, ok := snap["tags"].([]any)
	require.True(t, ok, "tags should be []any")
	tags[0] = "CORRUPTED"

	// Original config must be unaffected.
	origTags, ok := cfg.Get("tags").([]any)
	require.True(t, ok)
	assert.Equal(t, "alpha", origTags[0], "snapshot mutation must not corrupt original config")
}

func TestDeepCloneValue(t *testing.T) {
	tests := []struct {
		name  string
		input any
	}{
		{"nil", nil},
		{"string", "hello"},
		{"int", 42},
		{"float64", 3.14},
		{"bool", true},
		{"empty_slice", []any{}},
		{"empty_map", map[string]any{}},
		{"slice", []any{"a", "b", "c"}},
		{"nested_map", map[string]any{"a": map[string]any{"b": 1}}},
		{"deeply_nested", map[string]any{
			"l1": []any{
				map[string]any{"l2": []any{1, 2, 3}},
			},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeepCloneValue(tt.input)
			assert.Equal(t, tt.input, got)

			// Verify mutation isolation for mutable types.
			switch v := got.(type) {
			case []any:
				if len(v) > 0 {
					v[0] = "MUTATED"
					if orig, ok := tt.input.([]any); ok && len(orig) > 0 {
						assert.NotEqual(t, "MUTATED", orig[0], "mutation leaked to original")
					}
				}
			case map[string]any:
				v["__injected"] = true
				if orig, ok := tt.input.(map[string]any); ok {
					_, found := orig["__injected"]
					assert.False(t, found, "mutation leaked to original")
				}
			}
		})
	}
}

func TestSnapshot_NestedMapIsolation(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("matrix:\n  - name: a\n    val: 1\n"), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)

	snap := cfg.(Snapshotter).Snapshot()

	// Mutate a nested map inside a list element in the snapshot.
	matrix, ok := snap["matrix"].([]any)
	require.True(t, ok)
	elem, ok := matrix[0].(map[string]any)
	require.True(t, ok)
	elem["name"] = "CORRUPTED"

	// Original config must be unaffected.
	origMatrix, ok := cfg.Get("matrix").([]any)
	require.True(t, ok)
	origElem, ok := origMatrix[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "a", origElem["name"], "snapshot nested map mutation must not corrupt original config")
}

// TestConfig_ConcurrentGetAndReload verifies that concurrent Get() and Reload()
// calls do not race. Run with -race to verify.
func TestConfig_ConcurrentGetAndReload(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: initial\ncount: 1\n"), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)

	c := cfg.(*config)

	const readers = 10
	const iterations = 100

	var wg sync.WaitGroup

	// Multiple goroutines reading concurrently.
	for i := range readers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := range iterations {
				_ = cfg.Get("key")
				_ = cfg.Get("count")
				_ = cfg.Keys()
				if j%10 == 0 {
					var dest map[string]any
					_ = cfg.Scan(&dest)
				}
				if j%5 == 0 {
					_ = cfg.(Snapshotter).Snapshot()
				}
				_ = id // prevent unused variable warning
			}
		}(i)
	}

	// One goroutine reloading concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range iterations {
			// Alternate between two config versions to exercise the swap.
			require.NoError(t, os.WriteFile(yamlFile, []byte("key: version_a\ncount: 2\n"), 0o644))
			_ = c.Reload(yamlFile, "")

			require.NoError(t, os.WriteFile(yamlFile, []byte("key: version_b\ncount: 3\n"), 0o644))
			_ = c.Reload(yamlFile, "")
		}
	}()

	wg.Wait()

	// After all reloads, config should still be readable without error.
	val := cfg.Get("key")
	assert.NotNil(t, val, "key should be present after concurrent reloads")
}

func TestConfig_ObservedGeneration(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: val\n"), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)

	og, ok := cfg.(ObservedGenerationer)
	require.True(t, ok, "*config must implement ObservedGenerationer")

	assert.Equal(t, int64(0), og.ObservedGeneration(), "initial observed generation must be 0")

	og.SetObservedGeneration(1)
	assert.Equal(t, int64(1), og.ObservedGeneration())

	og.SetObservedGeneration(42)
	assert.Equal(t, int64(42), og.ObservedGeneration())
}

func TestConfig_ObservedGeneration_IndependentOfGeneration(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: v1\n"), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)

	c := cfg.(*config)
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: v2\n"), 0o644))
	require.NoError(t, c.Reload(yamlFile, ""))
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: v3\n"), 0o644))
	require.NoError(t, c.Reload(yamlFile, ""))

	g := cfg.(Generationer)
	og := cfg.(ObservedGenerationer)

	assert.Equal(t, int64(2), g.Generation(), "two successful reloads → generation 2")
	assert.Equal(t, int64(0), og.ObservedGeneration(), "observed generation not yet set")

	og.SetObservedGeneration(1)
	assert.Equal(t, int64(2), g.Generation())
	assert.Equal(t, int64(1), og.ObservedGeneration(), "drift: generation 2 vs observed 1")
}

func TestConfig_HasDrift(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: val\n"), 0o644))

	cfg, err := Load(yamlFile, "")
	require.NoError(t, err)

	assert.False(t, HasDrift(cfg), "generation 0 == observed 0 → no drift")

	c := cfg.(*config)
	require.NoError(t, os.WriteFile(yamlFile, []byte("key: v2\n"), 0o644))
	require.NoError(t, c.Reload(yamlFile, ""))

	assert.True(t, HasDrift(cfg), "generation 1 != observed 0 → drift")

	cfg.(ObservedGenerationer).SetObservedGeneration(1)
	assert.False(t, HasDrift(cfg), "generation 1 == observed 1 → no drift")

	// NewFromMap does not implement ObservedGenerationer.
	mapCfg := NewFromMap(map[string]any{"a": 1})
	assert.False(t, HasDrift(mapCfg), "NewFromMap → no drift (interfaces not satisfied)")
}
