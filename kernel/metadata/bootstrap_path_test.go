package metadata

import "testing"

func TestIsBootstrapPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		// accept
		{"/api/v1/access/setup/admin", true},
		{"/api/v2/access/setup/admin", true},
		{"/api/v10/access/setup/admin", true},
		{"/api/v1/anycell/setup/admin", true},
		// reject — substring防护：缺 cell segment
		{"/api/v1/setup/admin/foo", false},
		// reject — substring防护
		{"/foo/setup/admin/bar", false},
		// reject — 尾部多 segment
		{"/api/v1/access/setup/admin/foo", false},
		// reject — 缺 vN 段
		{"/api/access/setup/admin", false},
		// reject — 大写 V，case-sensitive
		{"/api/V1/access/setup/admin", false},
		// reject — 空 cell 段
		{"/api/v1//setup/admin", false},
		// reject — 缺前导 /
		{"api/v1/access/setup/admin", false},
		// reject — 空字符串
		{"", false},
		// reject — 单斜杠
		{"/", false},
		// reject — 尾部 /
		{"/api/v1/access/setup/admin/", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if got := IsBootstrapPath(tc.path); got != tc.want {
				t.Errorf("IsBootstrapPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsPublicHTTPPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		// accept — exactly /api (root, no trailing slash)
		{"/api", true},
		// accept — /api/v1 prefix covers any path under v1
		{"/api/v1/config/{key}", true},
		// accept — /api/v2 is public surface, oracle is version-agnostic
		{"/api/v2/users", true},
		// accept — deep sub-path
		{"/api/v1/access/sessions/login", true},
		// reject — /apix is not a prefix match (/api/ required after /api)
		{"/apix/v1/foo", false},
		// reject — internal listener is not public
		{"/internal/v1/config/{key}", false},
		// reject — framework probe has no trust-boundary flag
		{"/healthz", false},
		// reject — empty string
		{"", false},
		// reject — missing leading slash
		{"api/v1/foo", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if got := IsPublicHTTPPath(tc.path); got != tc.want {
				t.Errorf("IsPublicHTTPPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsInternalHTTPPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"/internal/v1", true},
		{"/internal/v1/access/sessions", true},
		{"/internal/v1/", true},
		{"/api/v1/access/sessions", false},
		{"/internal/v10/access", false},
		{"/internal/v1foo", false},
		{"internal/v1/access", false},
		{"", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if got := IsInternalHTTPPath(tc.path); got != tc.want {
				t.Errorf("IsInternalHTTPPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
