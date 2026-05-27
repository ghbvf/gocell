package metadata

import (
	"errors"
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
		"cells/accesscore/cell.yaml":                                       &fstest.MapFile{Data: []byte("id: accesscore\n")},
		"cells/accesscore/slices/login/slice.yaml":                         &fstest.MapFile{Data: []byte("id: login\n")},
		"cells/auditcore/cell.yaml":                                        &fstest.MapFile{Data: []byte("id: auditcore\n")},
		"contracts/http/auth/login/v1/contract.yaml":                       &fstest.MapFile{Data: []byte("id: http.auth.login.v1\n")},
		"contracts/event/session/created/v1/contract.yaml":                 &fstest.MapFile{Data: []byte("id: event.session.created.v1\n")},
		"journeys/J-ssologin.yaml":                                         &fstest.MapFile{Data: []byte("id: J-ssologin\n")},
		"journeys/status-board.yaml":                                       &fstest.MapFile{Data: []byte("- journey: J-ssologin\n")},
		"assemblies/platform/assembly.yaml":                                &fstest.MapFile{Data: []byte("id: platform\n")},
		"actors.yaml":                                                      &fstest.MapFile{Data: []byte("- id: external\n")},
		"examples/ssobff/cells/foo/cell.yaml":                              &fstest.MapFile{Data: []byte("id: foo\n")},
		"examples/ssobff/cells/foo/slices/bar/slice.yaml":                  &fstest.MapFile{Data: []byte("id: bar\n")},
		"examples/ssobff/contracts/http/x/y/v1/contract.yaml":              &fstest.MapFile{Data: []byte("id: http.x.y.v1\n")},
		"examples/ssobff/journeys/J-flow.yaml":                             &fstest.MapFile{Data: []byte("id: J-flow\n")},
		"examples/ssobff/assembly.yaml":                                    &fstest.MapFile{Data: []byte("id: ssobff\n")},
		"README.md":                                                        &fstest.MapFile{Data: []byte("ignored\n")},
		"cells/accesscore/slices/login/handler.go":                         &fstest.MapFile{Data: []byte("// ignored\n")},
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
		".gocell/manifest.yaml":                    &fstest.MapFile{Data: []byte(manifest)},
		"cells/payment/cell.yaml":                  &fstest.MapFile{Data: []byte("id: payment\n")},
		"cells/payment/slices/charge/slice.yaml":   &fstest.MapFile{Data: []byte("id: charge\n")},
		"contracts/http/payment/charge/v1/contract.yaml": &fstest.MapFile{Data: []byte("id: http.payment.charge.v1\n")},
		"journeys/J-payment.yaml":                  &fstest.MapFile{Data: []byte("id: J-payment\n")},
		"assemblies/payment-svc/assembly.yaml":     &fstest.MapFile{Data: []byte("id: payment-svc\n")},
		"actors.yaml":                              &fstest.MapFile{Data: []byte("- id: bank\n")},
		"journeys/status-board.yaml":               &fstest.MapFile{Data: []byte("- journey: J-payment\n")},
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
			name: "secondary singleton actors",
			manifest: "version: v1\nmodules:\n  - path: .\n  - path: sub\n" +
				"    includes:\n      actors: actors.yaml\n",
			wantSub: "actors.yaml is a workspace-level singleton",
		},
		{
			name: "unsupported version",
			manifest: "version: v2\nmodules:\n  - path: .\n",
			wantSub: "unsupported version",
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
		".gocell/manifest.yaml":    &fstest.MapFile{Data: []byte(manifest)},
		"cells/keep/cell.yaml":     &fstest.MapFile{Data: []byte("id: keep\n")},
		"cells/skip/cell.yaml":     &fstest.MapFile{Data: []byte("id: skip\n")},
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

func (e errFS) Open(name string) (fs.File, error)    { return nil, e.err }
func (e errFS) Stat(name string) (fs.FileInfo, error) { return nil, e.err }
