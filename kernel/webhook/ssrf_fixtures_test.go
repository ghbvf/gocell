package webhook

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// ssrfDenyFixture mirrors testdata/webhook-ssrf-deny.yaml. The fixture is the
// human-auditable source of truth; ssrfBlockedCIDRStrings (ssrf.go) is the
// machine source. TestSSRFBlocklistFixtureNoDrift asserts the two are the same
// set, so a CIDR cannot be added/removed in one without the other.
type ssrfDenyFixture struct {
	Blocked struct {
		IPv4 []ssrfDenyEntry `yaml:"ipv4"`
		IPv6 []ssrfDenyEntry `yaml:"ipv6"`
	} `yaml:"blocked"`
}

type ssrfDenyEntry struct {
	CIDR   string `yaml:"cidr"`
	Reason string `yaml:"reason"`
}

func loadSSRFDenyFixture(t *testing.T) ssrfDenyFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "webhook-ssrf-deny.yaml"))
	require.NoError(t, err)
	var doc ssrfDenyFixture
	require.NoError(t, yaml.Unmarshal(data, &doc))
	require.NotEmpty(t, doc.Blocked.IPv4)
	require.NotEmpty(t, doc.Blocked.IPv6)
	return doc
}

// TestSSRFBlocklistFixtureNoDrift is the drift guard: the fixture CIDR set and
// the code CIDR set (ssrfBlockedCIDRStrings) must be exactly equal. The
// symmetric difference is reported on failure so the offending side is obvious.
func TestSSRFBlocklistFixtureNoDrift(t *testing.T) {
	t.Parallel()
	doc := loadSSRFDenyFixture(t)

	fixtureSet := map[string]struct{}{}
	for _, e := range append(append([]ssrfDenyEntry{}, doc.Blocked.IPv4...), doc.Blocked.IPv6...) {
		require.NotContains(t, fixtureSet, e.CIDR, "fixture has duplicate CIDR %q", e.CIDR)
		fixtureSet[e.CIDR] = struct{}{}
		require.NotEmpty(t, e.Reason, "fixture CIDR %q missing reason", e.CIDR)
	}

	codeSet := map[string]struct{}{}
	for _, c := range ssrfBlockedCIDRStrings {
		require.NotContains(t, codeSet, c, "ssrfBlockedCIDRStrings has duplicate CIDR %q", c)
		codeSet[c] = struct{}{}
	}

	var onlyFixture, onlyCode []string
	for c := range fixtureSet {
		if _, ok := codeSet[c]; !ok {
			onlyFixture = append(onlyFixture, c)
		}
	}
	for c := range codeSet {
		if _, ok := fixtureSet[c]; !ok {
			onlyCode = append(onlyCode, c)
		}
	}
	sort.Strings(onlyFixture)
	sort.Strings(onlyCode)
	require.Empty(t, onlyFixture, "CIDRs only in fixture (missing from ssrfBlockedCIDRStrings): %v", onlyFixture)
	require.Empty(t, onlyCode, "CIDRs only in code (missing from webhook-ssrf-deny.yaml): %v", onlyCode)
}

// TestSSRFBlocklistFixtureParses asserts every fixture CIDR is a valid CIDR
// literal (so the fixture itself cannot drift into an unparseable form).
func TestSSRFBlocklistFixtureParses(t *testing.T) {
	t.Parallel()
	doc := loadSSRFDenyFixture(t)
	for _, e := range append(append([]ssrfDenyEntry{}, doc.Blocked.IPv4...), doc.Blocked.IPv6...) {
		_, _, err := net.ParseCIDR(e.CIDR)
		require.NoErrorf(t, err, "fixture CIDR %q does not parse", e.CIDR)
	}
}

// TestSSRFBlocklistExcludesMappedPrefix locks the deliberate exclusion of
// ::ffff:0:0/96 (the IPv4-mapped IPv6 prefix). normalizeIP (To4 dewrap) is the
// single mapped-IPv4 defense; listing the prefix would be a dead/over-blocking
// double path. See the fixture header + ADR.
func TestSSRFBlocklistExcludesMappedPrefix(t *testing.T) {
	t.Parallel()
	for _, c := range ssrfBlockedCIDRStrings {
		require.NotEqual(t, "::ffff:0:0/96", c,
			"::ffff:0:0/96 must NOT be in the blocklist; normalizeIP handles IPv4-mapped")
	}
}
