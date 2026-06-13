# GoCell CLI container image.
# Multi-arch: built by goreleaser (see .goreleaser.yaml dockers section).
# goreleaser places the pre-compiled binary in the build-context root as
# "gocell", so we can COPY it directly.
#
# ref: goreleaser/goreleaser Dockerfile (distroless static)
# ref: google/go-containerregistry Dockerfile

FROM gcr.io/distroless/static:nonroot

LABEL org.opencontainers.image.source="https://github.com/ghbvf/gocell" \
      org.opencontainers.image.description="GoCell CLI — governance and code-generation tool" \
      org.opencontainers.image.licenses="MIT"

# goreleaser copies the platform-matched binary into the build context root.
COPY gocell /gocell

# distroless:nonroot already defaults to the nonroot user (uid 65532), but make
# it explicit so the image never runs as root regardless of base-image drift and
# so static scanners (Semgrep dockerfile.security.missing-user-entrypoint, a
# blocking gate) can verify non-root from the Dockerfile alone.
USER nonroot:nonroot

ENTRYPOINT ["/gocell"]
