package meta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRepositoryPassesMinimalValidRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "actors.yaml"), `
- id: edge-bff
  type: external
  maxConsistencyLevel: L1
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L2
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
l0Dependencies: []
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "slices", "session-login", "slice.yaml"), `
id: session-login
belongsToCell: access-core
contractUsages:
  - contract: http.auth.login.v1
    role: serve
verify:
  unit:
    - unit.session-login.service
  contract:
    - contract.http.auth.login.v1.serve
  waivers: []
`)
	writeFile(t, filepath.Join(root, "contracts", "http", "auth", "login", "v1", "contract.yaml"), `
id: http.auth.login.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients:
    - edge-bff
schemaRefs:
  request: request.schema.json
  response: response.schema.json
`)
	writeFile(t, filepath.Join(root, "contracts", "http", "auth", "login", "v1", "request.schema.json"), `{}`)
	writeFile(t, filepath.Join(root, "contracts", "http", "auth", "login", "v1", "response.schema.json"), `{}`)
	writeFile(t, filepath.Join(root, "assemblies", "core-bundle", "assembly.yaml"), `
id: core-bundle
cells:
  - access-core
build:
  entrypoint: cmd/core-bundle/main.go
  binary: core-bundle
  deployTemplate: k8s
`)
	writeFile(t, filepath.Join(root, "cmd", "core-bundle", "main.go"), `package main`)
	writeFile(t, filepath.Join(root, "journeys", "J-login.yaml"), `
id: J-login
goal: login
cells:
  - access-core
contracts:
  - http.auth.login.v1
passCriteria:
  - text: ok
    mode: auto
    checkRef: journey.J-login.ok
`)
	writeFile(t, filepath.Join(root, "journeys", "status-board.yaml"), `
- journeyId: J-login
  state: doing
  risk: low
  blocker: ""
  updatedAt: 2026-04-04
`)

	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("ValidateRepository returned error: %v", err)
	}
	if result.HasErrors() {
		t.Fatalf("expected no errors, got %#v", result.Errors)
	}
}

func TestValidateRepositoryRejectsExpiredWaiver(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L2
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
l0Dependencies: []
`)
	writeFile(t, filepath.Join(root, "contracts", "http", "auth", "login", "v1", "contract.yaml"), `
id: http.auth.login.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients:
    - edge-bff
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "slices", "session-login", "slice.yaml"), `
id: session-login
belongsToCell: access-core
contractUsages:
  - contract: http.auth.login.v1
    role: serve
verify:
  unit:
    - unit.session-login.service
  contract: []
  waivers:
    - contract: http.auth.login.v1
      owner: platform-team
      reason: temp
      expiresAt: 2000-01-01
`)

	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("ValidateRepository returned error: %v", err)
	}
	if !result.HasErrors() {
		t.Fatal("expected expired waiver to fail validation")
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "expired") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected expired waiver error, got %#v", result.Errors)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimLeft(content, "\n")), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
