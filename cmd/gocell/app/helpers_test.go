package app

import (
	"flag"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestBuildLocatorOptions covers the flag → LocatorOption translation.
// buildLocatorOptions is called by `gocell validate` and `gocell check`
// after parsing the --layout and --manifest flag values.
func TestBuildLocatorOptions(t *testing.T) {
	cases := []struct {
		name         string
		layout       string
		manifest     string
		wantErr      string // non-empty → expect error containing this substring
		wantOptCount int    // expected number of LocatorOptions returned
	}{
		{
			name:         "empty layout and manifest → auto (0 opts)",
			layout:       "",
			manifest:     "",
			wantOptCount: 0,
		},
		{
			name:         "auto explicit → still 0 opts (auto is the nil default)",
			layout:       "auto",
			manifest:     "",
			wantOptCount: 0,
		},
		{
			name:         "conventional → 1 opt (WithLocatorMode)",
			layout:       "conventional",
			manifest:     "",
			wantOptCount: 1,
		},
		{
			name:         "manifest layout only → 1 opt (WithLocatorMode)",
			layout:       "manifest",
			manifest:     "",
			wantOptCount: 1,
		},
		{
			name:         "manifest layout + custom path → 2 opts",
			layout:       "manifest",
			manifest:     "path/to/manifest.yaml",
			wantOptCount: 2,
		},
		{
			name:         "conventional + manifest path → 2 opts",
			layout:       "conventional",
			manifest:     "some/manifest.yaml",
			wantOptCount: 2,
		},
		{
			name:    "invalid layout → error",
			layout:  "invalid-layout",
			wantErr: "unknown locator mode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := buildLocatorOptions(tc.layout, tc.manifest)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(opts) != tc.wantOptCount {
				t.Errorf("option count = %d, want %d", len(opts), tc.wantOptCount)
			}
		})
	}
}

// TestBuildLocatorOptions_ModeApplied verifies that the LocatorOptions
// returned by buildLocatorOptions actually drive the correct resolved mode
// when applied to NewLocatorFS.
func TestBuildLocatorOptions_ModeApplied(t *testing.T) {
	manifestContent := []byte("version: v1\nmodules:\n  - path: .\n")

	t.Run("conventional overrides manifest file auto-detect", func(t *testing.T) {
		opts, err := buildLocatorOptions("conventional", "")
		if err != nil {
			t.Fatalf("buildLocatorOptions: %v", err)
		}
		// fsys has .gocell/manifest.yaml — auto would pick Manifest;
		// WithLocatorMode(Conventional) must override that.
		fsys := fstest.MapFS{
			".gocell/manifest.yaml": &fstest.MapFile{Data: manifestContent},
		}
		loc, err := metadata.NewLocatorFS(fsys, opts...)
		if err != nil {
			t.Fatalf("NewLocatorFS: %v", err)
		}
		if loc.Mode() != metadata.LocatorConventional {
			t.Errorf("mode = %v, want LocatorConventional", loc.Mode())
		}
	})

	t.Run("manifest forces manifest mode", func(t *testing.T) {
		opts, err := buildLocatorOptions("manifest", "")
		if err != nil {
			t.Fatalf("buildLocatorOptions: %v", err)
		}
		fsys := fstest.MapFS{
			".gocell/manifest.yaml": &fstest.MapFile{Data: manifestContent},
		}
		loc, err := metadata.NewLocatorFS(fsys, opts...)
		if err != nil {
			t.Fatalf("NewLocatorFS: %v", err)
		}
		if loc.Mode() != metadata.LocatorManifest {
			t.Errorf("mode = %v, want LocatorManifest", loc.Mode())
		}
	})

	t.Run("auto without manifest → conventional", func(t *testing.T) {
		opts, err := buildLocatorOptions("", "")
		if err != nil {
			t.Fatalf("buildLocatorOptions: %v", err)
		}
		// No .gocell/manifest.yaml → auto-detect falls back to conventional.
		fsys := fstest.MapFS{}
		loc, err := metadata.NewLocatorFS(fsys, opts...)
		if err != nil {
			t.Fatalf("NewLocatorFS: %v", err)
		}
		if loc.Mode() != metadata.LocatorConventional {
			t.Errorf("mode = %v, want LocatorConventional", loc.Mode())
		}
	})

	t.Run("auto with manifest → manifest mode", func(t *testing.T) {
		opts, err := buildLocatorOptions("auto", "")
		if err != nil {
			t.Fatalf("buildLocatorOptions: %v", err)
		}
		fsys := fstest.MapFS{
			".gocell/manifest.yaml": &fstest.MapFile{Data: manifestContent},
		}
		loc, err := metadata.NewLocatorFS(fsys, opts...)
		if err != nil {
			t.Fatalf("NewLocatorFS: %v", err)
		}
		if loc.Mode() != metadata.LocatorManifest {
			t.Errorf("mode = %v, want LocatorManifest", loc.Mode())
		}
	})
}

// TestAddLocatorFlags verifies that addLocatorFlags registers --layout and
// --manifest with the expected default values and that values update after
// Parse.
func TestAddLocatorFlags(t *testing.T) {
	t.Run("defaults are empty strings", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		layout, manifestPath := addLocatorFlags(fs)

		if layout == nil {
			t.Fatal("layout pointer is nil")
		}
		if manifestPath == nil {
			t.Fatal("manifestPath pointer is nil")
		}
		if *layout != "" {
			t.Errorf("default layout = %q, want empty string", *layout)
		}
		if *manifestPath != "" {
			t.Errorf("default manifestPath = %q, want empty string", *manifestPath)
		}
	})

	t.Run("flags parse correctly", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		layout, manifestPath := addLocatorFlags(fs)

		if err := fs.Parse([]string{"--layout=manifest", "--manifest=custom/manifest.yaml"}); err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if *layout != "manifest" {
			t.Errorf("layout = %q after parse, want %q", *layout, "manifest")
		}
		if *manifestPath != "custom/manifest.yaml" {
			t.Errorf("manifestPath = %q after parse, want %q", *manifestPath, "custom/manifest.yaml")
		}
	})

	t.Run("conventional layout parses", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		layout, _ := addLocatorFlags(fs)
		if err := fs.Parse([]string{"--layout=conventional"}); err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if *layout != "conventional" {
			t.Errorf("layout = %q, want conventional", *layout)
		}
	})

	// Two-phase validation contract: flag.FlagSet accepts any string value for
	// --layout (phase 1 — the FlagSet is value-agnostic for string flags), but
	// buildLocatorOptions rejects unknown mode values (phase 2 — semantic
	// validation). This test documents the explicit split so callers do not
	// assume FlagSet.Parse is the sole gatekeeper.
	t.Run("invalid layout value: Parse accepts, buildLocatorOptions rejects", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		layout, manifestPath := addLocatorFlags(fs)

		// Phase 1: FlagSet accepts any string — no error expected here.
		if err := fs.Parse([]string{"--layout=bogus"}); err != nil {
			t.Fatalf("FlagSet.Parse unexpected error for unknown layout string: %v", err)
		}

		// Phase 2: semantic validation rejects the unknown mode.
		_, err := buildLocatorOptions(*layout, *manifestPath)
		if err == nil {
			t.Fatal("buildLocatorOptions: expected error for layout=bogus, got nil")
		}
		if want := "unknown locator mode"; !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	})
}
