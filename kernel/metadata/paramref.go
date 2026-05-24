package metadata

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"path"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// mixinParamSchema is the subset of JSON Schema fields we back-fill from a
// shared mixin into a ParamSchema.  Only type/minimum/maximum/minLength/maxLength
// are recognized; unknown fields are silently ignored (the mixin may carry
// $schema/$id/title/description which are irrelevant here).
type mixinParamSchema struct {
	Type      string `json:"type"`
	Minimum   *int   `json:"minimum"`
	Maximum   *int   `json:"maximum"`
	MinLength *int   `json:"minLength"`
	MaxLength *int   `json:"maxLength"`
}

// resolveParamRefs resolves every queryParam and pathParam that carries a
// non-empty Ref field.  It is called from parseContract immediately after
// m.Dir is set and m.Endpoints.HTTP != nil.
//
// Resolution reads the mixin file through fsys (so tests can supply an
// fstest.MapFS without touching the real filesystem).  The mixin path must be
// a clean, forward-slash FS path that does not escape the FS root.
//
// Post-conditions for each resolved param:
//   - Type, Minimum, Maximum (and MinLength/MaxLength when present) are
//     back-filled from the mixin.
//   - Required is left untouched — it is an in-contract caller declaration.
//   - Ref is kept set for traceability.
//
// Errors:
//   - mutual exclusion: Ref set alongside any of type/minimum/maximum/
//     minLength/maxLength/format → errcode KindInvalid/ErrMetadataInvalid.
//   - path escape: the resolved path is not a valid fs.FS path → error.
//   - missing file: fs.ReadFile returns an error → wrapped error.
func resolveParamRefs(fsys fs.FS, m *ContractMeta) error {
	if m.Endpoints.HTTP == nil {
		return nil
	}
	h := m.Endpoints.HTTP
	if err := resolveParamMap(fsys, m.Dir, h.QueryParams); err != nil {
		return err
	}
	return resolveParamMap(fsys, m.Dir, h.PathParams)
}

func resolveParamMap(fsys fs.FS, contractDir string, params map[string]ParamSchema) error {
	read := func(dir, ref string) ([]byte, error) {
		target, err := resolveRefPath(dir, ref)
		if err != nil {
			return nil, err
		}
		return fs.ReadFile(fsys, target)
	}
	for name, param := range params {
		if param.Ref == "" {
			continue
		}
		resolved, err := ResolveParamRef(name, contractDir, param, read)
		if err != nil {
			return err
		}
		params[name] = resolved
	}
	return nil
}

// ResolveParamRef is the single source of param $ref resolution. It enforces
// $ref mutual-exclusion, fetches the referenced mixin bytes via readMixin (the
// caller's IO layer maps contractDir+ref → bytes and owns path-traversal
// guarding), then back-fills value-shape fields onto param. Both the metadata
// parser (fs.FS IO) and the contract test harness (fixtureload IO) call this so
// the resolution semantics live in exactly one place.
//
// Mutual-exclusion is checked BEFORE the read so an author error ($ref alongside
// inline type/min/max) is reported regardless of whether the mixin file exists.
func ResolveParamRef(
	paramName, contractDir string,
	param ParamSchema,
	readMixin func(contractDir, ref string) ([]byte, error),
) (ParamSchema, error) {
	if err := checkMutualExclusion(paramName, param); err != nil {
		return param, err
	}
	raw, err := readMixin(contractDir, param.Ref)
	if err != nil {
		return param, errcode.Wrap(
			errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"param $ref target not found or unreadable",
			err,
			errcode.WithDetails(slog.String("param", paramName), slog.String("ref", param.Ref)),
			errcode.WithInternal(fmt.Sprintf("param=%q ref=%q dir=%q", paramName, param.Ref, contractDir)),
		)
	}
	return backfillParamFromMixin(paramName, param, raw)
}

func backfillParamFromMixin(paramName string, param ParamSchema, raw []byte) (ParamSchema, error) {
	var mixin mixinParamSchema
	if err := json.Unmarshal(raw, &mixin); err != nil {
		return param, errcode.Wrap(
			errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"param $ref file is not valid JSON",
			err,
			errcode.WithInternal(fmt.Sprintf("param=%q ref=%q", paramName, param.Ref)),
		)
	}

	param.Type = mixin.Type
	if mixin.Minimum != nil {
		param.Minimum = mixin.Minimum
	}
	if mixin.Maximum != nil {
		param.Maximum = mixin.Maximum
	}
	if mixin.MinLength != nil {
		param.MinLength = mixin.MinLength
	}
	if mixin.MaxLength != nil {
		param.MaxLength = mixin.MaxLength
	}
	return param, nil
}

// checkMutualExclusion returns an error when $ref co-exists with any inline
// value-shape field.  required is intentionally allowed (it is a call-site
// declaration orthogonal to the value shape).
func checkMutualExclusion(paramName string, p ParamSchema) error {
	if p.Type != "" || p.Minimum != nil || p.Maximum != nil ||
		p.MinLength != nil || p.MaxLength != nil || p.Format != "" {
		return errcode.New(
			errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"param $ref and inline type/minimum/maximum/minLength/maxLength/format are mutually exclusive",
			errcode.WithInternal(fmt.Sprintf(
				"param=%q has both $ref=%q and one or more inline value-shape fields; "+
					"remove inline fields or remove $ref", paramName, p.Ref,
			)),
		)
	}
	return nil
}

// resolveRefPath resolves the ref string relative to contractDir, using
// forward-slash path arithmetic (fs.FS semantics), and validates the result
// is a valid fs.FS path (no ".." escape above root).
func resolveRefPath(contractDir, ref string) (string, error) {
	target := path.Clean(path.Join(contractDir, ref))
	if !fs.ValidPath(target) {
		return "", fmt.Errorf("resolved path %q is not a valid FS path (may escape root)", target)
	}
	return target, nil
}
