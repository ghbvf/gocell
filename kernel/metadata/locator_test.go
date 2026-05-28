package metadata

import (
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// TestLocator_AutoDetect verifies that a fresh root without .gocell/manifest.yaml
// resolves to Conventional, while a root with the manifest resolves to Manifest.
func TestLocator_AutoDetect(t *testing.T) {
	t.Run("conventional when no manifest", func(t *testing.T) {
		fsys := fstest.MapFS{
			"cells/foo/cell.yaml": &fstest.MapFile{Data: []byte("id: foo\n")},
		}
		l, err := NewLocatorFS(fsys)
		if err != nil {
			t.Fatalf("NewLocatorFS: %v", err)
		}
		if got := l.Mode(); got != LocatorConventional {
			t.Errorf("Mode = %v, want %v", got, LocatorConventional)
		}
	})
	t.Run("manifest when present", func(t *testing.T) {
		fsys := fstest.MapFS{
			".gocell/manifest.yaml": &fstest.MapFile{Data: []byte("version: v1\nmodules:\n  - path: .\n")},
		}
		l, err := NewLocatorFS(fsys)
		if err != nil {
			t.Fatalf("NewLocatorFS: %v", err)
		}
		if got := l.Mode(); got != LocatorManifest {
			t.Errorf("Mode = %v, want %v", got, LocatorManifest)
		}
	})
}

// TestLocator_ConventionalDiscover validates that all 5 patterns + 2 singletons
// are emitted for a conventional layout.
func TestLocator_ConventionalDiscover(t *testing.T) {
	fsys := fstest.MapFS{
		"cells/accesscore/cell.yaml":                          &fstest.MapFile{Data: []byte("id: accesscore\n")},
		"cells/accesscore/slices/login/slice.yaml":            &fstest.MapFile{Data: []byte("id: login\n")},
		"cells/auditcore/cell.yaml":                           &fstest.MapFile{Data: []byte("id: auditcore\n")},
		"contracts/http/auth/login/v1/contract.yaml":          &fstest.MapFile{Data: []byte("id: http.auth.login.v1\n")},
		"contracts/event/session/created/v1/contract.yaml":    &fstest.MapFile{Data: []byte("id: event.session.created.v1\n")},
		"journeys/J-ssologin.yaml":                            &fstest.MapFile{Data: []byte("id: J-ssologin\n")},
		"journeys/status-board.yaml":                          &fstest.MapFile{Data: []byte("- journey: J-ssologin\n")},
		"assemblies/platform/assembly.yaml":                   &fstest.MapFile{Data: []byte("id: platform\n")},
		"actors.yaml":                                         &fstest.MapFile{Data: []byte("- id: external\n")},
		"examples/ssobff/cells/foo/cell.yaml":                 &fstest.MapFile{Data: []byte("id: foo\n")},
		"examples/ssobff/cells/foo/slices/bar/slice.yaml":     &fstest.MapFile{Data: []byte("id: bar\n")},
		"examples/ssobff/contracts/http/x/y/v1/contract.yaml": &fstest.MapFile{Data: []byte("id: http.x.y.v1\n")},
		"examples/ssobff/journeys/J-flow.yaml":                &fstest.MapFile{Data: []byte("id: J-flow\n")},
		"examples/ssobff/assembly.yaml":                       &fstest.MapFile{Data: []byte("id: ssobff\n")},
		"README.md":                                           &fstest.MapFile{Data: []byte("ignored\n")},
		"cells/accesscore/slices/login/handler.go":            &fstest.MapFile{Data: []byte("// ignored\n")},
	}
	l, err := NewLocatorFS(fsys, WithLocatorMode(LocatorConventional))
	if err != nil {
		t.Fatalf("NewLocatorFS: %v", err)
	}
	sources, err := l.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got := summariseSources(sources)
	want := []string{
		"actors:actors.yaml:",
		"assembly:assemblies/platform/assembly.yaml:",
		"assembly:examples/ssobff/assembly.yaml:",
		"cell:cells/accesscore/cell.yaml:accesscore",
		"cell:cells/auditcore/cell.yaml:auditcore",
		"cell:examples/ssobff/cells/foo/cell.yaml:foo",
		"contract:contracts/event/session/created/v1/contract.yaml:",
		"contract:contracts/http/auth/login/v1/contract.yaml:",
		"contract:examples/ssobff/contracts/http/x/y/v1/contract.yaml:",
		"journey:examples/ssobff/journeys/J-flow.yaml:",
		"journey:journeys/J-ssologin.yaml:",
		"slice:cells/accesscore/slices/login/slice.yaml:accesscore",
		"slice:examples/ssobff/cells/foo/slices/bar/slice.yaml:foo",
		"status-board:journeys/status-board.yaml:",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Discover output mismatch.\ngot:  %v\nwant: %v", got, want)
	}
}

// TestLocator_ManifestSingleModuleDefaults exercises the Operator-SDK case:
// a single-module manifest with no includes: omits — defaults apply.
func TestLocator_ManifestSingleModuleDefaults(t *testing.T) {
	manifest := `version: v1
modules:
  - path: .
`
	fsys := fstest.MapFS{
		".gocell/manifest.yaml":                          &fstest.MapFile{Data: []byte(manifest)},
		"cells/payment/cell.yaml":                        &fstest.MapFile{Data: []byte("id: payment\n")},
		"cells/payment/slices/charge/slice.yaml":         &fstest.MapFile{Data: []byte("id: charge\n")},
		"contracts/http/payment/charge/v1/contract.yaml": &fstest.MapFile{Data: []byte("id: http.payment.charge.v1\n")},
		"journeys/J-payment.yaml":                        &fstest.MapFile{Data: []byte("id: J-payment\n")},
		"assemblies/payment-svc/assembly.yaml":           &fstest.MapFile{Data: []byte("id: payment-svc\n")},
		"actors.yaml":                                    &fstest.MapFile{Data: []byte("- id: bank\n")},
		"journeys/status-board.yaml":                     &fstest.MapFile{Data: []byte("- journey: J-payment\n")},
	}
	l, err := NewLocatorFS(fsys)
	if err != nil {
		t.Fatalf("NewLocatorFS: %v", err)
	}
	if l.Mode() != LocatorManifest {
		t.Fatalf("auto-detect failed: got %v want %v", l.Mode(), LocatorManifest)
	}
	sources, err := l.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got := summariseSources(sources)
	want := []string{
		"actors:actors.yaml:",
		"assembly:assemblies/payment-svc/assembly.yaml:",
		"cell:cells/payment/cell.yaml:payment",
		"contract:contracts/http/payment/charge/v1/contract.yaml:",
		"journey:journeys/J-payment.yaml:",
		"slice:cells/payment/slices/charge/slice.yaml:payment",
		"status-board:journeys/status-board.yaml:",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Discover output mismatch.\ngot:  %v\nwant: %v", got, want)
	}
}

// TestLocator_ManifestWorkspaceMultiModule exercises the Workspace case:
// two modules, second module aggregates a subdirectory. actors.yaml and
// status-board.yaml are workspace-level singletons attached to modules[0].
func TestLocator_ManifestWorkspaceMultiModule(t *testing.T) {
	manifest := `version: v1
modules:
  - path: .
  - path: vendor-cells/payment
`
	fsys := fstest.MapFS{
		".gocell/manifest.yaml":                                  &fstest.MapFile{Data: []byte(manifest)},
		"cells/platform/cell.yaml":                               &fstest.MapFile{Data: []byte("id: platform\n")},
		"actors.yaml":                                            &fstest.MapFile{Data: []byte("- id: bank\n")},
		"vendor-cells/payment/cells/payment/cell.yaml":           &fstest.MapFile{Data: []byte("id: payment\n")},
		"vendor-cells/payment/cells/payment/slices/c/slice.yaml": &fstest.MapFile{Data: []byte("id: c\n")},
	}
	l, err := NewLocatorFS(fsys)
	if err != nil {
		t.Fatalf("NewLocatorFS: %v", err)
	}
	sources, err := l.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got := summariseSources(sources)
	want := []string{
		"actors:actors.yaml:",
		"cell:cells/platform/cell.yaml:platform",
		"cell:vendor-cells/payment/cells/payment/cell.yaml:payment",
		"slice:vendor-cells/payment/cells/payment/slices/c/slice.yaml:payment",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Discover output mismatch.\ngot:  %v\nwant: %v", got, want)
	}
}

// TestLocator_ManifestRejectsBadPaths verifies the path safety boundary:
// absolute paths and ".." segments are refused; duplicate module paths are
// refused; singleton declarations on modules[i>0] are refused.
func TestLocator_ManifestRejectsBadPaths(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		extraFS  fstest.MapFS // optional extra files beyond .gocell/manifest.yaml
		wantSub  string
	}{
		{
			name:     "absolute path",
			manifest: "version: v1\nmodules:\n  - path: /etc\n",
			wantSub:  "absolute path not allowed",
		},
		{
			name:     "parent escape",
			manifest: "version: v1\nmodules:\n  - path: ../outside\n",
			wantSub:  "path escape not allowed",
		},
		{
			name:     "duplicate path",
			manifest: "version: v1\nmodules:\n  - path: .\n  - path: .\n",
			wantSub:  "duplicates modules[0]",
		},
		{
			// secondary singleton: "sub" dir must exist so dir-existence check
			// (F1) passes and the singleton check fires as expected.
			name: "secondary singleton actors",
			manifest: "version: v1\nmodules:\n  - path: .\n  - path: sub\n" +
				"    includes:\n      actors: actors.yaml\n",
			extraFS: fstest.MapFS{
				"sub/.keep": &fstest.MapFile{Data: []byte("")},
			},
			wantSub: "actors.yaml is a workspace-level singleton",
		},
		{
			// secondary singleton: "sub" dir must exist so dir-existence check
			// (F1) passes and the singleton check fires as expected.
			name: "secondary singleton statusBoard",
			manifest: "version: v1\nmodules:\n  - path: .\n  - path: sub\n" +
				"    includes:\n      statusBoard: journeys/status-board.yaml\n",
			extraFS: fstest.MapFS{
				"sub/.keep": &fstest.MapFile{Data: []byte("")},
			},
			wantSub: "status-board.yaml is a workspace-level singleton",
		},
		{
			name: "excludes with parent escape",
			manifest: "version: v1\nmodules:\n  - path: .\n" +
				"    excludes:\n      - ../../secret/**\n",
			wantSub: "path escape not allowed",
		},
		{
			name: "include glob with parent escape",
			manifest: "version: v1\nmodules:\n  - path: .\n" +
				"    includes:\n      cells:\n        - ../../secret/cell.yaml\n",
			wantSub: "path escape not allowed",
		},
		{
			name: "manifest too large rejected",
			// Repeat a valid module line until the YAML exceeds the 64 KiB cap.
			manifest: largeManifestExceedingSizeLimit(),
			wantSub:  "exceeds size limit",
		},
		{
			name:     "too many modules rejected",
			manifest: tooManyModulesManifest(),
			wantSub:  "too many modules",
		},
		{
			name:     "unsupported version",
			manifest: "version: v2\nmodules:\n  - path: .\n",
			wantSub:  "unsupported version",
		},
		{
			name:     "empty modules",
			manifest: "version: v1\nmodules: []\n",
			wantSub:  "at least one module entry required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				".gocell/manifest.yaml": &fstest.MapFile{Data: []byte(tc.manifest)},
			}
			for k, v := range tc.extraFS {
				fsys[k] = v
			}
			_, err := NewLocatorFS(fsys, WithLocatorMode(LocatorManifest))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestLocator_ManifestExcludes verifies that exclude patterns filter out
// matched paths even when an include matched them.
func TestLocator_ManifestExcludes(t *testing.T) {
	manifest := `version: v1
modules:
  - path: .
    includes:
      cells:
        - "cells/*/cell.yaml"
    excludes:
      - "cells/skip/**"
`
	fsys := fstest.MapFS{
		".gocell/manifest.yaml": &fstest.MapFile{Data: []byte(manifest)},
		"cells/keep/cell.yaml":  &fstest.MapFile{Data: []byte("id: keep\n")},
		"cells/skip/cell.yaml":  &fstest.MapFile{Data: []byte("id: skip\n")},
	}
	l, err := NewLocatorFS(fsys)
	if err != nil {
		t.Fatalf("NewLocatorFS: %v", err)
	}
	sources, err := l.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := summariseSources(sources); !reflect.DeepEqual(got, []string{"cell:cells/keep/cell.yaml:keep"}) {
		t.Errorf("Discover output mismatch: %v", got)
	}
}

// TestLocator_ManifestGlobNoMatch verifies that a custom include glob
// that explicitly declares a pattern but matches zero files returns an
// error (F2: fail-closed for explicit patterns — typo guard).
// Nil/empty patterns are not affected.
func TestLocator_ManifestGlobNoMatch(t *testing.T) {
	manifest := `version: v1
modules:
  - path: .
    includes:
      cells:
        - "cells/bar/cell.yaml"
`
	fsys := fstest.MapFS{
		".gocell/manifest.yaml": &fstest.MapFile{Data: []byte(manifest)},
		"cells/foo/cell.yaml":   &fstest.MapFile{Data: []byte("id: foo\n")},
	}
	l, err := NewLocatorFS(fsys)
	if err != nil {
		t.Fatalf("NewLocatorFS: %v", err)
	}
	_, err = l.Discover()
	if err == nil {
		t.Fatal("expected error for zero-match explicit pattern, got nil")
	}
	if !strings.Contains(err.Error(), "matched zero files") {
		t.Errorf("error %q does not contain %q", err.Error(), "matched zero files")
	}
	if !strings.Contains(err.Error(), "cells/bar/cell.yaml") {
		t.Errorf("error %q does not contain pattern %q", err.Error(), "cells/bar/cell.yaml")
	}
}

// TestLocator_GeneratedExcludeDefault verifies that a manifest module
// without an explicit excludes list still excludes generated/** so
// codegen output doesn't re-enter the metadata scan (ADR threat
// matrix promise).
func TestLocator_GeneratedExcludeDefault(t *testing.T) {
	manifest := "version: v1\nmodules:\n  - path: .\n"
	fsys := fstest.MapFS{
		".gocell/manifest.yaml":             &fstest.MapFile{Data: []byte(manifest)},
		"cells/real/cell.yaml":              &fstest.MapFile{Data: []byte("id: real\n")},
		"generated/contracts/foo/cell.yaml": &fstest.MapFile{Data: []byte("id: shouldnotappear\n")},
		"generated/cells/foo/cell.yaml":     &fstest.MapFile{Data: []byte("id: shouldnotappear\n")},
	}
	l, err := NewLocatorFS(fsys)
	if err != nil {
		t.Fatalf("NewLocatorFS: %v", err)
	}
	sources, err := l.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := []string{"cell:cells/real/cell.yaml:real"}
	if got := summariseSources(sources); !reflect.DeepEqual(got, want) {
		t.Errorf("Discover output mismatch.\ngot:  %v\nwant: %v", got, want)
	}
}

// TestParseLocatorMode covers the CLI flag string round trip.
func TestParseLocatorMode(t *testing.T) {
	cases := []struct {
		in   string
		want LocatorMode
		err  bool
	}{
		{"", LocatorAuto, false},
		{"auto", LocatorAuto, false},
		{"conventional", LocatorConventional, false},
		{"manifest", LocatorManifest, false},
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseLocatorMode(tc.in)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLocatorMode(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseLocatorMode(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestLocator_ManifestModulePathExistence verifies that a manifest module.path
// that does not exist as a directory in fsys returns an error (F1: fail-closed
// existence + IsDir check).
func TestLocator_ManifestModulePathExistence(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		fsys     fstest.MapFS
		wantSub  string
	}{
		{
			name:     "module path does not exist",
			manifest: "version: v1\nmodules:\n  - path: nonexistent\n",
			fsys: fstest.MapFS{
				".gocell/manifest.yaml": &fstest.MapFile{Data: []byte("version: v1\nmodules:\n  - path: nonexistent\n")},
			},
			wantSub: `does not exist (or is not a directory)`,
		},
		{
			name:     "module path is a file not a directory",
			manifest: "version: v1\nmodules:\n  - path: iam-a-file\n",
			fsys: fstest.MapFS{
				".gocell/manifest.yaml": &fstest.MapFile{Data: []byte("version: v1\nmodules:\n  - path: iam-a-file\n")},
				"iam-a-file":            &fstest.MapFile{Data: []byte("not a dir\n")},
			},
			wantSub: `does not exist (or is not a directory)`,
		},
		{
			name: "dot path (workspace root) always valid",
			fsys: fstest.MapFS{
				".gocell/manifest.yaml": &fstest.MapFile{Data: []byte("version: v1\nmodules:\n  - path: .\n")},
			},
			wantSub: "", // no error expected
		},
		{
			name: "valid subdirectory path exists",
			fsys: fstest.MapFS{
				".gocell/manifest.yaml":  &fstest.MapFile{Data: []byte("version: v1\nmodules:\n  - path: modules/core\n")},
				"modules/core/cell.yaml": &fstest.MapFile{Data: []byte("id: core\n")},
			},
			wantSub: "", // no error expected
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLocatorFS(tc.fsys, WithLocatorMode(LocatorManifest))
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestLocator_ManifestGlobWalkPrefix verifies that matchManifestGlob starts
// the WalkDir from the fixed prefix before the first wildcard, not from root.
// This is F3: ReadDir should not be called on "." or "other" when the
// pattern prefix is "cells".
func TestLocator_ManifestGlobWalkPrefix(t *testing.T) {
	// Build a filesystem with files in two top-level dirs.
	fsys := fstest.MapFS{
		"cells/foo/cell.yaml": &fstest.MapFile{Data: []byte("id: foo\n")},
		"other/bar/file.txt":  &fstest.MapFile{Data: []byte("other\n")},
	}
	var readDirCalls []string
	traceFS := &walkTraceFS{MapFS: fsys, visited: &readDirCalls}

	matches, err := matchManifestGlob(traceFS, "cells/*/cell.yaml")
	if err != nil {
		t.Fatalf("matchManifestGlob: %v", err)
	}
	if len(matches) != 1 || matches[0] != "cells/foo/cell.yaml" {
		t.Errorf("matches = %v, want [cells/foo/cell.yaml]", matches)
	}
	// ReadDir should not be called on "other" or any path under "other/".
	for _, dir := range readDirCalls {
		if dir == "other" || strings.HasPrefix(dir, "other/") {
			t.Errorf("ReadDir called on %q — should not scan outside prefix", dir)
		}
	}
	// ReadDir should not be called on "." (the root) when prefix is "cells".
	for _, dir := range readDirCalls {
		if dir == "." {
			t.Errorf("ReadDir called on root '.' — expected prefix-scoped walk starting from 'cells'")
		}
	}
	// ReadDir should have been called on "cells" (the walk root).
	found := false
	for _, dir := range readDirCalls {
		if dir == "cells" {
			found = true
		}
	}
	if !found {
		t.Errorf("ReadDir was never called on 'cells' — prefix walk did not start from expected root; got: %v", readDirCalls)
	}
}

// TestLocator_ManifestGlobWalkPrefixDoubleStarFromRoot verifies that a pattern
// starting with "**" still walks from root (no prefix optimization possible).
func TestLocator_ManifestGlobWalkPrefixDoubleStarFromRoot(t *testing.T) {
	fsys := fstest.MapFS{
		"cells/foo/cell.yaml": &fstest.MapFile{Data: []byte("id: foo\n")},
	}
	var readDirCalls []string
	traceFS := &walkTraceFS{MapFS: fsys, visited: &readDirCalls}
	_, err := matchManifestGlob(traceFS, "**/cell.yaml")
	if err != nil {
		t.Fatalf("matchManifestGlob: %v", err)
	}
	// Root "." should be visited when pattern starts with **.
	found := false
	for _, dir := range readDirCalls {
		if dir == "." {
			found = true
		}
	}
	if !found {
		t.Errorf("ReadDir was never called on '.' for **-pattern; got: %v", readDirCalls)
	}
}

// TestLocator_WithManifestPathRejectsUnsafe verifies that WithManifestPath
// with an absolute path or parent-escape path is rejected at construction
// (F12: fail-fast in NewLocatorFS/NewLocator).
func TestLocator_WithManifestPathRejectsUnsafe(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantSub string
	}{
		{
			name:    "absolute path",
			path:    "/etc/manifest.yaml",
			wantSub: "absolute path not allowed",
		},
		{
			name:    "parent escape",
			path:    "../outside/manifest.yaml",
			wantSub: "path escape not allowed",
		},
		{
			name:    "double parent escape",
			path:    "../../manifest.yaml",
			wantSub: "path escape not allowed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				".gocell/manifest.yaml": &fstest.MapFile{Data: []byte("version: v1\nmodules:\n  - path: .\n")},
			}
			_, err := NewLocatorFS(fsys, WithManifestPath(tc.path), WithLocatorMode(LocatorConventional))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestLocator_GeneratedExcludeAdditive verifies that a manifest module with
// explicit excludes still gets "generated/**" appended (F13: additive, not
// replacing). The user-declared excludes must also be honored.
func TestLocator_GeneratedExcludeAdditive(t *testing.T) {
	manifest := `version: v1
modules:
  - path: .
    excludes:
      - "fixtures/**"
`
	fsys := fstest.MapFS{
		".gocell/manifest.yaml":             &fstest.MapFile{Data: []byte(manifest)},
		"cells/real/cell.yaml":              &fstest.MapFile{Data: []byte("id: real\n")},
		"fixtures/cell.yaml":                &fstest.MapFile{Data: []byte("id: fixture\n")},
		"generated/contracts/foo/cell.yaml": &fstest.MapFile{Data: []byte("id: gen\n")},
		"generated/cells/bar/cell.yaml":     &fstest.MapFile{Data: []byte("id: gen2\n")},
	}
	l, err := NewLocatorFS(fsys)
	if err != nil {
		t.Fatalf("NewLocatorFS: %v", err)
	}
	sources, err := l.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// Only "real" should appear; fixtures/** and generated/** both excluded.
	want := []string{"cell:cells/real/cell.yaml:real"}
	if got := summariseSources(sources); !reflect.DeepEqual(got, want) {
		t.Errorf("Discover output mismatch.\ngot:  %v\nwant: %v", got, want)
	}
}

// TestLocator_AutoDetectStatErrorPropagates ensures probe failures other than
// fs.ErrNotExist surface up rather than being mis-treated as "manifest absent".
func TestLocator_AutoDetectStatErrorPropagates(t *testing.T) {
	fsys := errFS{err: errors.New("synthetic stat failure")}
	_, err := NewLocatorFS(fsys)
	if err == nil {
		t.Fatalf("expected propagated stat error, got nil")
	}
	if !strings.Contains(err.Error(), "probe manifest") {
		t.Errorf("error does not mention probe: %v", err)
	}
}

// largeManifestExceedingSizeLimit builds a manifest whose size exceeds the
// 64 KiB cap by repeating a comment line.
func largeManifestExceedingSizeLimit() string {
	var b strings.Builder
	b.WriteString("version: v1\nmodules:\n  - path: .\n")
	// Filler comment lines until we exceed the cap.
	filler := "# " + strings.Repeat("x", 200) + "\n"
	for b.Len() < (65 << 10) {
		b.WriteString(filler)
	}
	return b.String()
}

// tooManyModulesManifest builds a manifest with more than maxManifestModules
// module entries to exercise the WalkDir-amplification guard.
func tooManyModulesManifest() string {
	var b strings.Builder
	b.WriteString("version: v1\nmodules:\n")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "  - path: m%d\n", i)
	}
	return b.String()
}

// summariseSources renders a deterministic sorted "kind:path:cellID" view of
// the discovered sources for table-driven comparison.
func summariseSources(sources []MetadataSource) []string {
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.Kind.String()+":"+s.Path+":"+s.CellID)
	}
	sort.Strings(out)
	return out
}

// errFS is a minimal fs.FS that returns a synthetic stat error for every
// path, used to verify Locator propagates probe failures.
type errFS struct{ err error }

func (e errFS) Open(name string) (fs.File, error)     { return nil, e.err }
func (e errFS) Stat(name string) (fs.FileInfo, error) { return nil, e.err }

// walkTraceFS wraps a MapFS and records every directory path for which ReadDir
// is called by fs.WalkDir, so tests can assert which subtrees were scanned.
// fstest.MapFS implements ReadDirFS, so fs.WalkDir uses ReadDir (not Open) for
// directory listing. We intercept ReadDir to capture the walked roots.
type walkTraceFS struct {
	MapFS   fstest.MapFS
	visited *[]string
}

func (w *walkTraceFS) Open(name string) (fs.File, error) {
	return w.MapFS.Open(name)
}

// ReadDir is the primary interception point: fs.WalkDir calls ReadDir on each
// directory it descends into (including the walk root itself).
func (w *walkTraceFS) ReadDir(name string) ([]fs.DirEntry, error) {
	*w.visited = append(*w.visited, name)
	return w.MapFS.ReadDir(name)
}

func (w *walkTraceFS) Stat(name string) (fs.FileInfo, error) {
	return fs.Stat(w.MapFS, name)
}
