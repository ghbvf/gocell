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

func cellFile(cellID string) string {
	return fmt.Sprintf("cells/%s/cell.yaml", cellID)
}

func sliceFile(key string) string {
	// key is "cellID/sliceID"
	parts := strings.SplitN(key, "/", 2)
	if len(parts) == 2 {
		return fmt.Sprintf("cells/%s/slices/%s/slice.yaml", parts[0], parts[1])
	}
	return key
}

func contractFile(contractID string) string {
	// contract IDs are like "http.auth.login.v1"
	// directory: contracts/http/auth/login/v1/contract.yaml
	segments := strings.Split(contractID, ".")
	return fmt.Sprintf("contracts/%s/contract.yaml", strings.Join(segments, "/"))
}

func journeyFile(journeyID string) string {
	return fmt.Sprintf("journeys/%s.yaml", journeyID)
}

func assemblyFile(assemblyID string) string {
	return fmt.Sprintf("assemblies/%s/assembly.yaml", assemblyID)
}

// contractDirFromID converts a contract ID to its directory path.
// "http.auth.login.v1" -> "contracts/http/auth/login/v1"
func contractDirFromID(id string) string {
	segments := strings.Split(id, ".")
	return filepath.Join("contracts", filepath.Join(segments...))
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

// --- path helpers ---

// repositoryRoot returns the absolute repository root from the project root.
func repositoryRoot(root string) string {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	return absRoot
}

// isWithinRoot checks that target resolves to a path inside root.
// Both sides are normalized to absolute paths, and symlinks are resolved
// when possible, to prevent both relative-path and symlink-based bypasses.
func isWithinRoot(root, target string) bool {
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
		absTarget = evalExistingPrefix(absTarget)
	}
	cleanRoot := absRoot + string(os.PathSeparator)
	return strings.HasPrefix(absTarget, cleanRoot) || absTarget == absRoot
}

// evalExistingPrefix resolves symlinks on the longest existing ancestor of p,
// then appends the non-existent suffix. This handles platforms where
// intermediate directories are symlinks (e.g., macOS /tmp → /private/tmp).
func evalExistingPrefix(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p // filesystem root, stop recursion
	}
	return filepath.Join(evalExistingPrefix(parent), filepath.Base(p))
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
