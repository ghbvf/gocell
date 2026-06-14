// Package config provides YAML + environment variable configuration loading
// for GoCell applications. Environment variables take precedence over YAML values.
// Nested keys use dot notation (e.g., "server.port").
//
// Example:
//
//	cfg, err := config.Load("config.yaml", "APP")
//	if err != nil { ... }
//	port := cfg.Get("server.http.port")
//
// For testing, use NewFromMap to create a Config from an existing map:
//
//	cfg := config.NewFromMap(map[string]any{"server.port": 8080})
package config
