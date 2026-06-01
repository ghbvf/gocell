package testutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// imagePinnedByDigest enforces "digest mandatory" — the @sha256:<64-hex> suffix
// is the immutable content reference and the sole pinning mechanism. The tag
// segment is informational (any printable tag is accepted) because not every
// image uses SemVer (e.g. MinIO ships ISO timestamp tags like
// RELEASE.YYYY-MM-DDTHH-MM-SSZ). Pinning strength derives from the digest, not
// the tag — equivalent to k8s ImagePullPolicy + digest pin best practice.
var imagePinnedByDigest = regexp.MustCompile(`^[a-z0-9./-]+:[A-Za-z0-9._\-]+@sha256:[a-f0-9]{64}$`)

// TestContainerImagesPinned verifies that every testcontainer image constant
// uses tag+digest pinning, not floating tags like "postgres:15-alpine".
//
// Coverage is AST-derived (not a hand-maintained map): the test parses
// images.go, collects every package-level const whose name ends with "Image"
// and whose value is a string literal, and asserts each matches
// imagePinnedByDigest. A new image constant (e.g. K3sImage) is therefore
// auto-enrolled — adding a string-literal const without a digest fails this
// test without any edit here. (Coverage is scoped to string-literal consts; a
// const defined indirectly, e.g. K3sImage = otherpkg.Const, would not be a
// BasicLit and is intentionally out of scope — image pins are always literals.)
//
// AI-robust grade: Medium (AST-derived coverage of the declaration set). A Hard
// form is unreachable: Go cannot force a string const literal to carry a digest
// at its declaration site, so the value check stays an archtest assertion. This
// upgrade replaces the prior hand-maintained `images` map, which was Soft (a new
// const silently escaped coverage — e.g. MosquittoImage was absent from it).
func TestContainerImagesPinned(t *testing.T) {
	images := imageConstsFromSource(t)
	require.NotEmpty(t, images, "no *Image string consts discovered in images.go")
	for name, image := range images {
		t.Run(name, func(t *testing.T) {
			assert.Regexp(t, imagePinnedByDigest, image,
				"%s = %q must be tag+digest pinned (name:tag@sha256:digest)", name, image)
		})
	}
}

// imageConstsFromSource parses images.go and returns every package-level string
// const whose name ends with "Image", mapped to its unquoted literal value.
// `go test` runs with the working directory set to the package dir, so the
// relative path resolves without runtime.Caller (which would break under
// -trimpath).
func imageConstsFromSource(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "images.go", nil, 0)
	require.NoError(t, err, "parse images.go")

	out := make(map[string]string)
	for _, decl := range file.Decls {
		gd, isGen := decl.(*ast.GenDecl)
		if !isGen || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, isVal := spec.(*ast.ValueSpec)
			if !isVal {
				continue
			}
			for i, ident := range vs.Names {
				if !strings.HasSuffix(ident.Name, "Image") || i >= len(vs.Values) {
					continue
				}
				lit, isLit := vs.Values[i].(*ast.BasicLit)
				if !isLit || lit.Kind != token.STRING {
					continue
				}
				val, uerr := strconv.Unquote(lit.Value)
				require.NoError(t, uerr, "unquote %s", ident.Name)
				out[ident.Name] = val
			}
		}
	}
	return out
}

// TestContainerImagesPinned_RejectsFloating verifies that floating tags (no
// digest) are caught by the pinned regex. These are negative examples.
func TestContainerImagesPinned_RejectsFloating(t *testing.T) {
	floating := []string{
		"postgres:15-alpine",
		"postgres:15.13-alpine",
		"redis:7-alpine",
		"redis:7.4.2-alpine",
		"rabbitmq:3-management-alpine",
		"rabbitmq:3.12.14-management-alpine",
		"hashicorp/vault:1.17",
		"otel/opentelemetry-collector:0.123.0",
		"minio/minio:latest",
		"minio/minio:RELEASE.2024-10-13T13-34-11Z",
	}

	for _, img := range floating {
		assert.NotRegexp(t, imagePinnedByDigest, img,
			"%q lacks a digest and must NOT match the pinned pattern", img)
	}
}

// TestContainerImagesPinned_RejectsMalformedDigest verifies that malformed
// digest suffixes (wrong algorithm, short hex, non-hex chars) are rejected.
func TestContainerImagesPinned_RejectsMalformedDigest(t *testing.T) {
	malformed := []string{
		"minio/minio:latest@sha256:short",
		"minio/minio:latest@sha512:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e",
		"minio/minio:latest@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936Z",
	}
	for _, img := range malformed {
		assert.NotRegexp(t, imagePinnedByDigest, img,
			"%q has a malformed digest and must NOT match the pinned pattern", img)
	}
}
