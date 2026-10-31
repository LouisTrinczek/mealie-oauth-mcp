# 12. Releases: release-please, GoReleaser binaries, signed image

Status: Accepted

Date: 2026-10-31

## Context

Users pull a container image and sometimes a binary. They need to know that
what they run came from this repository's workflow. Maintainers want versions
and changelogs to follow from commit messages with little manual work.

## Decision

- release-please reads Conventional Commits, opens a release pull request and,
  when it is merged, creates the tag and the GitHub release.
- GoReleaser builds only the binaries, for Linux and macOS on amd64 and arm64.
  It produces archives, checksums and SBOMs.
- The container image is not built by GoReleaser. A separate job builds it from
  the existing `Dockerfile` with `docker/build-push-action` for `linux/amd64`
  and `linux/arm64`, with an SBOM and provenance from the build.
- Signing is keyless with cosign through GitHub's OIDC identity: the checksum
  file and the image are signed. Build provenance is attested for both.
- Actions are pinned by full commit SHA.

## Consequences

- No long-lived signing keys exist to leak.
- Users verify images and binaries with `cosign` and `gh attestation`. See
  [Releases](../../README.md#releases).
- Two build paths exist, one for binaries and one for the image. Both start
  from the same tag and run in the same workflow.
- Release PRs opened with the default token do not start CI, so a maintainer
  merges them with the repository ruleset bypass.
