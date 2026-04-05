// Package config provides a Config interface with YAML + environment variable
// loading. Environment variables take precedence over YAML values. Nested keys
// use dot notation (e.g., "server.port").
//
// ref: go-micro/go-micro config/config.go — Config interface with Get/Scan pattern
// Adopted: Get/Scan/Keys interface shape.
// Deviated: simpler flat-map model with dot-separated keys instead of go-micro's
// source/reader/value abstraction layers.
package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Config provides read access to configuration values.
type Config interface {
	// Get returns the value for the given dot-separated key, or nil if absent.
	Get(key string) any
	// Scan unmarshals the entire configuration into dest.
	Scan(dest interface{}) error
	// Keys returns all available configuration keys sorted alphabetically.
	Keys() []string
}

// config is the default in-memory implementation of Config.
type config struct {
	mu   sync.RWMutex
	data map[string]any
	raw  map[string]any // original structured data for Scan
}

// Load reads a YAML file and overlays environment variable overrides.
// Environment variables override YAML values. The env prefix maps to nested
// keys by replacing underscores with dots and lowering the case.
// For example, APP_SERVER_PORT overrides the key "app.server.port".
func Load(yamlPath string, envPrefix string) (Config, error) {
	c := &config{
		data: make(map[string]any),
		raw:  make(map[string]any),
	}

	if yamlPath != "" {
		if err := c.loadYAML(yamlPath); err != nil {
			return nil, fmt.Errorf("config: load yaml: %w", err)
		}
	}

	c.loadEnv(envPrefix)
	return c, nil
}

// NewFromMap creates a Config from an existing map (useful for testing).
func NewFromMap(data map[string]any) Config {
	c := &config{
		data: make(map[string]any),
		raw:  data,
	}
	flatten("", data, c.data)
	return c
}

func (c *config) Get(key string) any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data[key]
}

func (c *config) Scan(dest interface{}) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Re-encode raw to YAML, then decode into dest.
	b, err := yaml.Marshal(c.raw)
	if err != nil {
		return fmt.Errorf("config: scan marshal: %w", err)
	}
	if err := yaml.Unmarshal(b, dest); err != nil {
		return fmt.Errorf("config: scan unmarshal: %w", err)
	}
	return nil
}

func (c *config) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]string, 0, len(c.data))
	for k := range c.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Reload re-reads the YAML file and overlays environment variables.
// Thread-safe for use from watcher callbacks.
func (c *config) Reload(yamlPath string, envPrefix string) error {
	newData := make(map[string]any)
	newRaw := make(map[string]any)

	if yamlPath != "" {
		raw, err := readYAML(yamlPath)
		if err != nil {
			return fmt.Errorf("config: reload yaml: %w", err)
		}
		newRaw = raw
		flatten("", raw, newData)
	}

	applyEnv(envPrefix, newData, newRaw)

	c.mu.Lock()
	c.data = newData
	c.raw = newRaw
	c.mu.Unlock()
	return nil
}

func (c *config) loadYAML(path string) error {
	raw, err := readYAML(path)
	if err != nil {
		return err
	}
	c.raw = raw
	flatten("", raw, c.data)
	return nil
}

func readYAML(path string) (map[string]any, error) {
	f, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(f, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *config) loadEnv(prefix string) {
	applyEnv(prefix, c.data, c.raw)
}

func applyEnv(prefix string, data map[string]any, raw map[string]any) {
	if prefix == "" {
		return
	}
	upperPrefix := strings.ToUpper(prefix) + "_"
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k, v := parts[0], parts[1]
		if !strings.HasPrefix(strings.ToUpper(k), upperPrefix) {
			continue
		}
		// Convert env var name to dot-separated key.
		// APP_SERVER_PORT → server.port (strip prefix, lowercase, underscores → dots)
		suffix := k[len(upperPrefix):]
		key := strings.ToLower(strings.ReplaceAll(suffix, "_", "."))
		data[key] = v
		// Also set in raw for Scan to pick up.
		setNested(raw, strings.Split(key, "."), v)
	}
}

// flatten recursively flattens a nested map into dot-separated keys.
func flatten(prefix string, m map[string]any, out map[string]any) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch vv := v.(type) {
		case map[string]any:
			flatten(key, vv, out)
		default:
			out[key] = v
		}
	}
}

// setNested sets a value in a nested map using a key path.
func setNested(m map[string]any, path []string, value any) {
	for i := 0; i < len(path)-1; i++ {
		next, ok := m[path[i]]
		if !ok {
			next = make(map[string]any)
			m[path[i]] = next
		}
		if nm, ok := next.(map[string]any); ok {
			m = nm
		} else {
			// Overwrite non-map value.
			nm := make(map[string]any)
			m[path[i]] = nm
			m = nm
		}
	}
	m[path[len(path)-1]] = value
}
