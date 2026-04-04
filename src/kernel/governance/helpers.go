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
	switch cell.ContractKind(c.Kind) {
	case cell.ContractHTTP:
		return c.Endpoints.Server
	case cell.ContractEvent:
		return c.Endpoints.Publisher
	case cell.ContractCommand:
		return c.Endpoints.Handler
	case cell.ContractProjection:
		return c.Endpoints.Provider
	default:
		return ""
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

// repositoryRoot returns the repository root from the project root.
// If root ends with "src", the repository root is the parent directory.
func repositoryRoot(root string) string {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	if filepath.Base(absRoot) == "src" {
		return filepath.Dir(absRoot)
	}
	return absRoot
}

// isWithinRoot checks that target resolves to a path inside root.
func isWithinRoot(root, target string) bool {
	cleanRoot := filepath.Clean(root) + string(os.PathSeparator)
	cleanTarget := filepath.Clean(target)
	return strings.HasPrefix(cleanTarget, cleanRoot) || cleanTarget == filepath.Clean(root)
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
