package governance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// --- contract endpoint helpers ---

// contractProvider returns the provider cell/actor for a contract based on its kind.
func contractProvider(c *metadata.ContractMeta) string {
	return c.ProviderEndpoint()
}

// consumerFieldName returns the YAML field name for the consumer endpoint
// based on contract kind (clients, subscribers, invokers, readers).
func consumerFieldName(kind string) string {
	switch cell.ContractKind(kind) {
	case cell.ContractHTTP:
		return "clients"
	case cell.ContractEvent:
		return "subscribers"
	case cell.ContractCommand:
		return "invokers"
	case cell.ContractProjection:
		return "readers"
	default:
		return "consumers"
	}
}

// actorFieldPath returns the locator field path for a property of the given
// actor ID inside actors.yaml. The root of actors.yaml is a YAML sequence,
// so the path uses the "[i].field" form so that metadata.Locate can descend
// into the matching element. Returns "" when the actor is not registered,
// in which case the caller falls back to Line/Column zero.
func actorFieldPath(actors []metadata.ActorMeta, actorID, field string) string {
	for i, a := range actors {
		if a.ID == actorID {
			return fmt.Sprintf("[%d].%s", i, field)
		}
	}
	return ""
}

// contractConsumers returns the consumer cell/actor list for a contract based on its kind.
func contractConsumers(c *metadata.ContractMeta) []string {
	switch cell.ContractKind(c.Kind) {
	case cell.ContractHTTP:
		return c.Endpoints.Clients
	case cell.ContractEvent:
		return c.Endpoints.Subscribers
	case cell.ContractCommand:
		return c.Endpoints.Invokers
	case cell.ContractProjection:
		return c.Endpoints.Readers
	default:
		return nil
	}
}

// --- file path helpers ---

// cellFile returns the YAML file path for the cell metadata entity.
// Returns "" if the input is nil; callers are responsible for checking nil if
// a file path is required for the violation report.
func cellFile(c *metadata.CellMeta) string {
	if c == nil {
		return ""
	}
	return c.File
}

// sliceFile returns the YAML file path for the slice metadata entity.
// Returns "" if the input is nil; callers are responsible for checking nil if
// a file path is required for the violation report.
func sliceFile(s *metadata.SliceMeta) string {
	if s == nil {
		return ""
	}
	return s.File
}

// contractFile returns the YAML file path for the contract metadata entity.
// Returns "" if the input is nil; callers are responsible for checking nil if
// a file path is required for the violation report.
func contractFile(c *metadata.ContractMeta) string {
	if c == nil {
		return ""
	}
	return c.File
}

// journeyFile returns the YAML file path for the journey metadata entity.
// Returns "" if the input is nil; callers are responsible for checking nil if
// a file path is required for the violation report.
func journeyFile(j *metadata.JourneyMeta) string {
	if j == nil {
		return ""
	}
	return j.File
}

// assemblyFile returns the YAML file path for the assembly metadata entity.
// Returns "" if the input is nil; callers are responsible for checking nil if
// a file path is required for the violation report.
func assemblyFile(a *metadata.AssemblyMeta) string {
	if a == nil {
		return ""
	}
	return a.File
}

// contractFileFromID returns the expected contract.yaml path derived from the
// contract ID, used when the contract entity is absent from ProjectMeta (i.e.
// the ID is a dangling reference). Use contractFile(c) when the entity exists;
// the two may differ for example projects where contracts live under
// examples/<X>/contracts/.
// The returned path always uses forward slashes so error messages are
// cross-platform consistent (Windows filepath.Join would produce backslashes).
func contractFileFromID(id string) string {
	return filepath.ToSlash(filepath.Join(contractDirFromID(id), "contract.yaml"))
}

// contractDirFromID converts a contract ID to its directory path.
// "http.auth.login.v1" -> "contracts/http/auth/login/v1"
// The returned path always uses forward slashes (cross-platform safe).
func contractDirFromID(id string) string {
	segments := strings.Split(id, ".")
	return filepath.ToSlash(filepath.Join("contracts", filepath.Join(segments...)))
}

func contractDirFromMeta(c *metadata.ContractMeta) string {
	if c == nil {
		return ""
	}
	if c.Dir != "" {
		return c.Dir
	}
	return contractDirFromID(c.ID)
}

// --- collection helpers ---

func containsRole(roles []cell.ContractRole, target cell.ContractRole) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}

func containsString(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

// --- consumer membership helpers ---

// wildcardConsumer marks a consumer endpoint that accepts any cell. Treated
// as a routing-level wildcard by membership checks (TOPO-03 / ADV-06) and
// skipped by per-actor checks (REF-14 / TOPO-07 maxConsistencyLevel) since
// "*" is not a real actor ID.
const wildcardConsumer = "*"

// cellMatchesConsumer reports whether cellID is included in the consumers list,
// honoring the wildcardConsumer ("*") which matches any cell. Used by every
// rule that compares a slice's owning cell against a contract's consumer
// endpoint (clients/subscribers/invokers/readers).
//
// All governance rules that perform consumer membership checks must use this
// helper so the wildcard semantics stay aligned across TOPO-03, ADV-06, and
// any future consumer-direction rule. Divergence here causes the same metadata
// to be accepted by one rule and rejected by another, leading to contradictory
// findings on the same project.
func cellMatchesConsumer(consumers []string, cellID string) bool {
	return containsString(consumers, wildcardConsumer) || containsString(consumers, cellID)
}

// isWildcardConsumer reports whether the consumer entry is the wildcard "*".
// Used by per-actor checks (REF-14 existence, TOPO-07 maxConsistencyLevel) to
// skip the wildcard entry, which is a routing marker rather than an actor ID.
func isWildcardConsumer(actor string) bool {
	return actor == wildcardConsumer
}

// --- path helpers ---

// repositoryRoot returns the absolute repository root from the project root.
func repositoryRoot(root string) string {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	return absRoot
}

// IsWithinRoot checks that target resolves to a path inside root.
// Both sides are normalized to absolute paths, and symlinks are resolved
// when possible, to prevent both relative-path and symlink-based bypasses.
//
// Exported so cmd/gocell and other callers share a single implementation
// rather than carrying a duplicate with a hand-maintained `// SYNC:` note.
func IsWithinRoot(root, target string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	// Resolve symlinks on root (which should exist).
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	}
	// For target: resolve symlinks if possible. If the target doesn't exist
	// (common — we're checking *whether* a file exists), resolve the longest
	// existing ancestor to handle platforms where intermediate dirs are
	// symlinks (e.g., macOS /tmp → /private/tmp).
	if resolved, err := filepath.EvalSymlinks(absTarget); err == nil {
		absTarget = resolved
	} else {
		absTarget = EvalExistingPrefix(absTarget)
	}
	cleanRoot := absRoot + string(os.PathSeparator)
	return strings.HasPrefix(absTarget, cleanRoot) || absTarget == absRoot
}

// EvalExistingPrefix resolves symlinks on the longest existing ancestor of p,
// then appends the non-existent suffix. This handles platforms where
// intermediate directories are symlinks (e.g., macOS /tmp → /private/tmp).
func EvalExistingPrefix(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p // filesystem root, stop recursion
	}
	return filepath.Join(EvalExistingPrefix(parent), filepath.Base(p))
}

// --- actor helpers ---

// actorExists checks if an actor ID is a known cell or external actor.
// It uses the pre-built actorSet for O(1) external actor lookup.
func (v *Validator) actorExists(id string) bool {
	if _, ok := v.project.Cells[id]; ok {
		return true
	}
	return v.actorSet[id]
}

// isExternalActor reports whether the ID is registered in actors.yaml.
// actors.yaml exclusively registers external systems — membership is the
// type declaration (see ActorMeta godoc). Cell IDs never satisfy this
// predicate; they are checked separately by actorExists when needed.
func (v *Validator) isExternalActor(id string) bool {
	return v.actorSet[id]
}
