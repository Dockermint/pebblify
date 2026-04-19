# Spec: CI Attestations + darwin Targets

Owner: `@software-architect`
Date: 2026-04-18
Feature branch: `feat/ci-attestations-arm64`
Implementation owner: `@devops`

---

## Objective

1. Add darwin/amd64 and darwin/arm64 to the release binary matrix (currently linux-only).
2. Publish SLSA provenance and SBOM attestations for every release binary and the multi-arch Docker image.
3. Enable `gh attestation verify` verification by consumers.

Reference: https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations

---

## Files to Touch

All files are owned by `@devops`. No other files change.

| File                                | Change type |
| :---------------------------------- | :---------- |
| `.github/workflows/release.yml`     | Modify      |
| `.github/workflows/ci.yml`          | Modify      |

---

## CI Workflow Changes (`ci.yml`)

### Build matrix expansion

Current matrix:
```yaml
strategy:
  matrix:
    goos: [linux]
    goarch: [amd64, arm64]
```

New matrix:
```yaml
strategy:
  matrix:
    include:
      - goos: linux
        goarch: amd64
        runner: ubuntu-latest
      - goos: linux
        goarch: arm64
        runner: ubuntu-latest
      - goos: darwin
        goarch: amd64
        runner: ubuntu-latest
      - goos: darwin
        goarch: arm64
        runner: ubuntu-latest
```

Cross-compilation works from ubuntu-latest because `CGO_ENABLED=0` requires no host toolchain for the target. No macOS runner is needed for the build job.

### Docker build job

The CI `docker` job currently builds `linux/amd64` only (no push). Extend to `linux/amd64,linux/arm64` using `docker/build-push-action` with `push: false`. No permission changes needed for CI (no push, no attestation in CI).

---

## Release Workflow Changes (`release.yml`)

### Required permissions block

Top-level permissions must include:

```yaml
permissions:
  contents: write
  packages: write
  id-token: write
  attestations: write
```

The existing `contents: write` and `packages: write` are present. Add `id-token: write` and `attestations: write`.

### Binary build matrix

Mirror the CI matrix expansion above. All four targets compile from `ubuntu-latest` with `CGO_ENABLED=0`.

Output filenames follow the existing convention:
`pebblify-<goos>-<goarch>`

### Binary attestation job

Add a job `attest-binaries` that runs after `build`:

```
Depends on: build
Runner: ubuntu-latest
Steps:
  1. actions/checkout@v4 (or latest pinned)
  2. actions/download-artifact@v4 — download all four pebblify-* artifacts
  3. actions/attest-build-provenance@v2
       subject-path: 'pebblify-*'
  4. actions/attest-sbom@v1 (if available as stable)
       subject-path: 'pebblify-*'
       sbom-format: spdx-json   (or cyclonedx-json — @devops chooses)
```

Note: verify exact action versions from https://github.com/actions/attest-build-provenance/releases and https://github.com/actions/attest-sbom/releases at implementation time. `@assistant` can look these up.

### Docker publish + attestation job

The existing `docker` job builds multi-arch and pushes to GHCR. Extend with attestation:

```
After docker push completes:
  - actions/attest-build-provenance@v2
      subject-name: ghcr.io/${{ github.repository }}
      subject-digest: ${{ steps.push.outputs.digest }}
      push-to-registry: true
```

The `docker/build-push-action` must be configured to output the image digest:

```yaml
- name: Build and push
  id: push
  uses: docker/build-push-action@v7
  with:
    push: true
    platforms: linux/amd64,linux/arm64
    ...
```

`steps.push.outputs.digest` provides the required subject-digest value.

### GitHub Release upload

The existing release workflow uses `actions/upload-artifact` + a release job. Ensure all four binaries are uploaded to the GitHub Release asset list. If the workflow currently uses `softprops/action-gh-release` or equivalent, update the `files` glob to include `pebblify-darwin-*`.

---

## Verification Steps

Consumers can verify an artifact with:

```bash
gh attestation verify pebblify-linux-amd64 \
  --repo Dockermint/Pebblify

gh attestation verify pebblify-darwin-arm64 \
  --repo Dockermint/Pebblify
```

For Docker:

```bash
gh attestation verify \
  oci://ghcr.io/dockermint/pebblify:v0.4.0 \
  --repo Dockermint/Pebblify
```

`@devops` must include these commands in the release workflow README section and in the release notes.

---

## Invariants

- `CGO_ENABLED=0` for all four targets. No cgo, no host toolchain dependency.
- Attestation jobs run only on tag pushes (`on: push: tags: ["v*"]`). Never on PR or branch push.
- Permissions are scoped to the jobs that need them, not promoted globally beyond what is listed.
- No secrets are embedded in workflow files. Docker login uses `${{ secrets.GITHUB_TOKEN }}` only.

---

## Hand-off

| Agent      | Scope files                                        | Action                         |
| :--------- | :------------------------------------------------- | :----------------------------- |
| `@devops`  | `.github/workflows/release.yml`, `.github/workflows/ci.yml` | Implement changes above |
| `@reviewer`| read-only                                          | Audit compliance, APPROVE/BLOCK|
| `@qa`      | no test files; CI validation is the test           | Verify CI green on test tag    |
| `@sysadmin`| git operations                                     | Branch, commit, PR             |
