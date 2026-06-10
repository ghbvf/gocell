// invariants asserted in this file:
//   - INVARIANT: SUBSCRIBERS-DERIVED-FIELD-FROZEN-01
//   - INVARIANT: CONTRACT-YAML-NO-SUBSCRIBERS-KEY-01
//   - INVARIANT: SUBSCRIBE-MARKER-RETIRED-01
//
// Package archtest — event subscriber single-source funnel (issue #856).
//
// Event consumer subscriptions are single-sourced from slice.yaml
// contractUsages[role=subscribe]. Two derived artifacts that used to be
// hand-maintained are now locked:
//
//  1. contract.yaml endpoints.subscribers — DERIVED. metadata.EndpointsMeta.
//     Subscribers carries yaml:"-", so the parser's KnownFields(true) strict
//     decode REJECTS any literal `subscribers:` key in contract.yaml. The cell
//     subscriber set is computed from slice contractUsages; external-actor
//     subscribers (no slice) live in the hand-written `actorSubscribers:`
//     field. deriveEventSubscribers merges both into Subscribers.
//  2. cell.go `// +slice:subscribe` markers — RETIRED. markergen's knownMarkers
//     no longer lists slice:subscribe, so a leftover marker is an unknown-marker
//     error at generate time; cellgen builds reg.Subscribe wiring from slice.yaml.
//
// AI-robust ratings (per .claude/rules/gocell/ai-robust.md):
//
//   - SUBSCRIBERS-DERIVED-FIELD-FROZEN-01 — Funnel dual-lock:
//     下游 Hard: yaml:"-" on EndpointsMeta.Subscribers + KnownFields strict
//     decode makes a hand-written `subscribers:` key in contract.yaml an
//     unrepresentable parse error (type-system gate; mirrors MaxConsistencyLevel
//     yaml:"-" precedent). This reflect-based test LOCKS that mechanism: if an
//     author re-adds a yaml key to Subscribers (re-opening the bypass), this
//     test fires. Type-level assertion, no string anchor.
//     上游 Medium: EndpointsMeta.Subscribers is an exported field on a public
//     struct — direct assignment by any package (cells/, examples/, cmd/) is
//     not currently guarded; package-internal AND package-external Go code can
//     assign it directly, bypassing deriveEventSubscribers. Archtest does not
//     yet lock the write path to the derive funnel. Upstream Hard-ization
//     tracked in gh issue #985.
//   - CONTRACT-YAML-NO-SUBSCRIBERS-KEY-01 — Medium (defense-in-depth +
//     documentation). KnownFields already rejects `subscribers:` at parse; this
//     content scan catches it statically across the tree and documents the
//     invariant. contract.yaml is a hand-authored text file, so Medium is the
//     ceiling for a YAML-level guard.
//   - SUBSCRIBE-MARKER-RETIRED-01 — Medium (defense-in-depth). The real
//     enforcement is markergen's grammar (unknown-marker error, Hard); this
//     scan catches a stray marker statically before generate runs.
//
// Blind spots (reverse self-checks below assert the scanners have teeth):
//   - The reflect lock keys on field NAMES "Subscribers"/"ActorSubscribers";
//     a rename surfaces as a test error (visible), not a silent pass.
//   - The contract.yaml scan parses YAML structurally (endpoints.subscribers
//     key presence), so it is not fooled by the word "subscribers" appearing
//     elsewhere; an aliased/merged YAML anchor for endpoints is a documented
//     blind spot (none exist in-tree).
//   - The marker scan matches the literal token "+slice:subscribe"; a marker
//     split across comment lines is a documented blind spot (markergen's
//     line-based parser would not recognize such a split as a marker either).
//   - Subscribers is an exported field on a public struct — direct assignment
//     by any package (cells/, examples/, cmd/) is not currently guarded;
//     upstream Hard-ization tracked in gh issue #985.
package archtest

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestSubscribersDerivedFieldFrozen01 locks the type-system mechanism that makes
// a hand-written contract.yaml `subscribers:` key unrepresentable: EndpointsMeta.
// Subscribers must carry yaml:"-" (derived; KnownFields rejects the literal key),
// and ActorSubscribers must remain the yaml-readable input for external actors.
func TestSubscribersDerivedFieldFrozen01(t *testing.T) {
	t.Parallel()
	et := reflect.TypeOf(metadata.EndpointsMeta{})

	subs, ok := et.FieldByName("Subscribers")
	if !ok {
		t.Fatal("SUBSCRIBERS-DERIVED-FIELD-FROZEN-01: EndpointsMeta has no Subscribers field (rename?)")
	}
	if got := subs.Tag.Get("yaml"); got != "-" {
		t.Errorf("SUBSCRIBERS-DERIVED-FIELD-FROZEN-01: EndpointsMeta.Subscribers yaml tag = %q, want %q "+
			"(derived field must be yaml:\"-\" so KnownFields rejects a hand-written subscribers: key)", got, "-")
	}

	actor, ok := et.FieldByName("ActorSubscribers")
	if !ok {
		t.Fatal("SUBSCRIBERS-DERIVED-FIELD-FROZEN-01: EndpointsMeta has no ActorSubscribers field (rename?)")
	}
	if got := actor.Tag.Get("yaml"); got != "actorSubscribers,omitempty" {
		t.Errorf("SUBSCRIBERS-DERIVED-FIELD-FROZEN-01: EndpointsMeta.ActorSubscribers yaml tag = %q, "+
			"want %q (the hand-written external-actor input)", got, "actorSubscribers,omitempty")
	}
}

// scanSubscribersKey reports a violation when an event contract.yaml declares a
// literal endpoints.subscribers key. Parses structurally (not a substring match)
// so the word "subscribers" elsewhere does not false-positive. data is parsed
// without KnownFields so a stray key (which the real parser would reject) is
// still observable here.
func scanSubscribersKey(rel string, data []byte) []string {
	var doc struct {
		Kind      string                 `yaml:"kind"`
		Endpoints map[string]interface{} `yaml:"endpoints"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil // malformed YAML is another rule's concern
	}
	if doc.Kind != "event" {
		return nil
	}
	if _, present := doc.Endpoints["subscribers"]; present {
		return []string{rel + ": event contract.yaml declares endpoints.subscribers; " +
			"cell subscribers are derived from slice contractUsages[role=subscribe] and " +
			"external actors go in actorSubscribers (a literal subscribers: key is rejected by KnownFields)"}
	}
	return nil
}

// TestContractYAMLNoSubscribersKey01 asserts no event contract.yaml under
// contracts/ or examples/ declares a literal endpoints.subscribers key.
func TestContractYAMLNoSubscribersKey01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := scanner.DirsScope(
		root, []string{"contracts", "examples"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "contract.yaml"
		}),
	)
	scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc scanner.ContentContext) {
		for _, v := range scanSubscribersKey(fc.Rel, fc.Bytes) {
			t.Errorf("CONTRACT-YAML-NO-SUBSCRIBERS-KEY-01: %s", v)
		}
	})
}

// TestContractYAMLNoSubscribersKey01_ScannerFires proves the scanner has teeth
// (reverse self-check): a synthetic event contract.yaml with endpoints.subscribers
// must be flagged, and one with only actorSubscribers must not.
func TestContractYAMLNoSubscribersKey01_ScannerFires(t *testing.T) {
	t.Parallel()
	violating := []byte("kind: event\nendpoints:\n  publisher: x\n  subscribers: [y]\n")
	if got := scanSubscribersKey("x/contract.yaml", violating); len(got) == 0 {
		t.Error("CONTRACT-YAML-NO-SUBSCRIBERS-KEY-01: scanner did not flag a literal subscribers: key")
	}
	clean := []byte("kind: event\nendpoints:\n  publisher: x\n  actorSubscribers: [ext]\n")
	if got := scanSubscribersKey("x/contract.yaml", clean); len(got) != 0 {
		t.Errorf("CONTRACT-YAML-NO-SUBSCRIBERS-KEY-01: scanner false-positive on actorSubscribers: %v", got)
	}
	httpKind := []byte("kind: http\nendpoints:\n  subscribers: [y]\n")
	if got := scanSubscribersKey("x/contract.yaml", httpKind); len(got) != 0 {
		t.Errorf("CONTRACT-YAML-NO-SUBSCRIBERS-KEY-01: scanner must only check event contracts: %v", got)
	}
}

// scanSubscribeMarker reports a violation when a cell.go retains a
// `// +slice:subscribe` marker (retired in favor of slice.yaml single-source).
func scanSubscribeMarker(rel string, data []byte) []string {
	if strings.Contains(string(data), "+slice:subscribe") {
		return []string{rel + ": retains a +slice:subscribe marker; subscriptions are single-sourced " +
			"from slice.yaml contractUsages[role=subscribe] — remove the marker"}
	}
	return nil
}

// TestSubscribeMarkerRetired01 asserts no cell.go under cells/ or examples/
// retains a +slice:subscribe marker.
func TestSubscribeMarkerRetired01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := scanner.DirsScope(
		root, platformAndExampleCellScanDirs(),
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "cell.go"
		}),
	)
	scanner.EachContentFile(t, scope, []string{".go"}, func(t *testing.T, fc scanner.ContentContext) {
		for _, v := range scanSubscribeMarker(fc.Rel, fc.Bytes) {
			t.Errorf("SUBSCRIBE-MARKER-RETIRED-01: %s", v)
		}
	})
}

// TestSubscribeMarkerRetired01_ScannerFires proves the scanner has teeth.
func TestSubscribeMarkerRetired01_ScannerFires(t *testing.T) {
	t.Parallel()
	violating := []byte("type Cell struct {\n\t// +slice:subscribe:slice=s,topic=t,handler=H\n\tsvc *s.Service\n}\n")
	if got := scanSubscribeMarker("cells/x/cell.go", violating); len(got) == 0 {
		t.Error("SUBSCRIBE-MARKER-RETIRED-01: scanner did not flag a +slice:subscribe marker")
	}
	clean := []byte("type Cell struct {\n\t// +slice:route:slice=s,subPath=/x\n\th *s.Handler\n}\n")
	if got := scanSubscribeMarker("cells/x/cell.go", clean); len(got) != 0 {
		t.Errorf("SUBSCRIBE-MARKER-RETIRED-01: scanner false-positive on +slice:route: %v", got)
	}
}
