package catalog

import "fmt"

// AllIncludeTokens lists every accepted ?include= / --include= value.
// Maintained as the single source of truth so CLI and HTTP cannot drift.
var AllIncludeTokens = []string{"cellDeps", "packageDeps", "relations", "statusBoard"}

// ParseIncludeTokens converts a sanitized token list into IncludeOptions.
// Caller is responsible for upstream allowlist validation (csvparam.ParseAllowed
// against AllIncludeTokens); this function only maps known tokens to fields and
// returns an error if any token is unrecognized (defensive double-check).
func ParseIncludeTokens(tokens []string) (IncludeOptions, error) {
	var opts IncludeOptions
	for _, t := range tokens {
		switch t {
		case "cellDeps":
			opts.CellDeps = true
		case "packageDeps":
			opts.PackageDeps = true
		case "relations":
			opts.Relations = true
		case "statusBoard":
			opts.StatusBoard = true
		default:
			return IncludeOptions{}, fmt.Errorf("catalog: unknown include token %q", t)
		}
	}
	return opts, nil
}
