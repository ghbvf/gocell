package config

import (
	"os"
	"path/filepath"
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
