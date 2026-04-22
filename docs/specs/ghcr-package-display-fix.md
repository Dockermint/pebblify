# Feature: GHCR Package Page Display Fixes

## Context

The GHCR package page at `https://github.com/Dockermint/pebblify/pkgs/container/pebblify`
presents two user-facing defects after the v0.4.0 release:

1. The **first tag displayed** is `sha256-630823013281d130ddf078bbb79b2af388062ec494dd24e88e8219fae1f62518`,
   not the semver `0.4.0`. This is confusing to consumers who `docker pull`
   and scrape the UI for the "current" tag.
2. The package page shows **"No description provided"** despite the Dockerfile
   carrying a full `org.opencontainers.image.description` OCI label and the
   workflow re-setting the same label via `docker/metadata-action`.

Roadmap entry: `v0.4.1` — patch release for GHCR UX polish (see `docs/ROADMAP.md`).

## Requirements

1. After the next tagged release, the GHCR package page **MUST** list the semver
   tag (e.g. `0.4.1`, `0.4`) **before** any cosign/Sigstore attestation tag. [confirmed]
2. The GHCR package page **MUST** display a non-empty description matching the
   OCI `description` label. [confirmed]
3. The link "Package → Repository" on the GHCR UI **MUST** resolve to
   `github.com/Dockermint/pebblify`. [confirmed]
4. No regression on existing release artifacts: binaries, SBOM, SLSA provenance,
   image attestations MUST continue to publish successfully. [confirmed]
5. Fix MUST be validated on an `-rc` tag before applying to a final semver tag. [confirmed]

## Root Cause Analysis

### Issue 1 — Digest-style tag appears first

`actions/attest-build-provenance@v4` with `push-to-registry: true` pushes a
Sigstore-standard cosign artifact to the same repository, tagged
`sha256-<digest>.sig` / `sha256-<digest>` (OCI referrer convention). That push
happens **after** the `docker/build-push-action` step that publishes the semver
tags. GHCR sorts the tag list by **last-pushed timestamp (descending)**, so the
attestation tag surfaces at the top of the UI. This is expected Sigstore
behaviour, not a bug in the action.

### Issue 2 — "No description provided"

GHCR does **not** automatically propagate `org.opencontainers.image.description`
from the OCI manifest to the package-page description field. The package-page
description is populated from one of:

- the **linked GitHub repository's** description (when the package is linked
  to a repo via `org.opencontainers.image.source` **or** via GHCR package
  settings "Inherit access from repository"), **or**
- an **explicit** `PATCH /orgs/{org}/packages/container/{name}` API call
  with `{"description": "..."}` using a PAT that has `write:packages` scope.

Pebblify's current wiring breaks the repository link:

- `Dockerfile` sets `org.opencontainers.image.source=https://github.com/Dockermint/Pebblify`
  (capital `P`).
- `docker/metadata-action` sets the same label via `steps.repo.outputs.name`,
  which lowercases `${{ github.repository }}` → `dockermint/pebblify`.
- The Dockerfile `LABEL` is baked into the image at build time; the
  metadata-action label is applied to the manifest. **The manifest label wins
  for annotations, but the image-config label wins for the repo-link heuristic
  on GHCR.** GHCR requires an **exact case-sensitive match** to
  `github.com/<org>/<repo>` as known by the registry (lowercase). The
  mismatch (capital `P`) breaks the repo link → no description inheritance.

## Proposed Fixes

### Fix A — Semver tag appears first

**Option A1 (preferred).** Add a `latest` tag via `docker/metadata-action` with
`type=raw,value=latest,enable={{is_default_branch}}`, and add a second
`docker/build-push-action` step (or a trailing `crane tag` / `oras tag`
invocation) that **re-pushes** `latest` **after** the `attest-build-provenance`
step. Result: `latest` becomes the most-recent push, appears first in the GHCR
UI, with the semver tag adjacent and the attestation tag pushed down.

Tradeoff: introduces a `latest` floating tag — consumers pinning to `latest`
get moving-target behaviour. Document in README under "Pinning recommendations".

**Option A2.** Document the current Sigstore behaviour in README and accept
the digest-first UI. Zero workflow change. Consumers learn to read past the
attestation row.

Tradeoff: perception problem persists. Discoverability suffers.

**Option A3.** Remove `push-to-registry: true` from
`attest-build-provenance` — keep attestations off-registry (default sigstore
transparency log only). Tradeoff: breaks the Step-17 acceptance criterion in
CLAUDE.md (`gh attestation verify oci://<image>` requires registry-side
attestation). **Rejected.**

### Fix B — Description on package page

**Option B1 (preferred, zero-secret).** Link the container package to the
repository using GHCR package settings → **"Inherit access from repository"**
(manual one-time CEO action via GHCR UI). Combined with Fix C below (correct
`source` label casing), this causes GHCR to inherit the repository description
automatically.

Tradeoff: one-time manual step, not reproducible from code. Must be documented
in `docs/markdown/RELEASE.md` as a post-first-release checklist item.

**Option B2 (automated, requires secret).** Add a trailing step in
`release.yml` that calls:
```
gh api --method PATCH /orgs/Dockermint/packages/container/pebblify \
  -f description="LevelDB to PebbleDB migration tool..."
```
using a new org-scoped PAT stored as `secrets.GHCR_PACKAGE_ADMIN_TOKEN`
(scope: `write:packages`, `read:org`).

Tradeoff: introduces long-lived org PAT. PAT custody + rotation policy
required. Violates "secrets minimal surface". Higher maintenance.

### Fix C — Correct `source` label casing

Dockerfile LABEL currently hard-codes `github.com/Dockermint/Pebblify`.
**Fix:** remove the `org.opencontainers.image.source` line from the Dockerfile
`LABEL` block; let `docker/metadata-action` inject it from
`steps.repo.outputs.name` (lowercase) exclusively. This eliminates the
case-mismatch and the double-source-of-truth.

Alternative: change Dockerfile LABEL value to lowercase
`https://github.com/Dockermint/pebblify`. Less clean (two sources of truth
remain) but preserves `docker inspect` readability when image is pulled
without manifest annotations.

## CEO Decisions (locked 2026-04-22)

1. **D1 = A1 — add `latest` tag, re-push after `attest-build-provenance`.**
   Semver tag appears first in GHCR UI. Document `latest` pinning caveat
   in README.
2. **D2 = B1 — manual "Inherit access from repository" in GHCR package
   settings.** No new secret. One-time CEO action post-merge; tracked as a
   post-release checklist item in `docs/markdown/release-automation.md`.
3. **D3 = Remove `org.opencontainers.image.source` LABEL line from
   Dockerfile.** `docker/metadata-action` is sole source of truth.
4. **D4 = Yes — cut `v0.4.1-rc1` canary tag first** to validate workflow
   before `v0.4.1` final.

## Scope — Dual-Owner Feature

This feature touches two exclusive write scopes. A **single PR** must be
coordinated across two implementer agents:

| File                              | Owner                 | Change                                                                        |
| :-------------------------------- | :-------------------- | :---------------------------------------------------------------------------- |
| `.github/workflows/release.yml`   | `@devops`             | Fix A (A1): add `latest` tag, re-push after attestation step. Fix B2 step if D2=B2. |
| `Dockerfile`                      | `@container-engineer` | Fix C: remove `org.opencontainers.image.source` LABEL line (or lowercase it). |

CTO orchestration:
- Open **one** feature branch `fix/ghcr-package-display`.
- Delegate `@devops` first (workflow change); delegate `@container-engineer`
  second (Dockerfile change).
- Both changes land in the same branch, same PR, single `Closes #<issue>`.
- No other files modified. `@reviewer` verifies scope = exactly these two files.

(D2 = B1 locked → no new-secret custody review required.)

## Acceptance Criteria

After the `v0.4.1` (or first `v0.4.1-rc*`) release tag is pushed:

1. `https://github.com/Dockermint/pebblify/pkgs/container/pebblify` lists
   `0.4.1` (or `latest`, per D1) as the **first** tag in the Tags panel.
2. The package page shows a non-empty description matching
   `org.opencontainers.image.description`.
3. The "Repository" link in the package sidebar resolves to
   `github.com/Dockermint/pebblify`.
4. All pre-existing release verification gates pass unchanged:
   `gh attestation verify oci://ghcr.io/dockermint/pebblify:0.4.1` → OK,
   SBOM attestation present, GPG tag signature valid.

## Verification

Manual post-release checks (CTO executes during Step 17):

```bash
# Tag ordering + digest
skopeo inspect docker://ghcr.io/dockermint/pebblify:0.4.1 \
  | jq '.RepoTags, .Labels["org.opencontainers.image.source"]'

# Package description (requires gh auth with read:packages)
gh api /orgs/Dockermint/packages/container/pebblify \
  --jq '{name, description, repository: .repository.full_name}'

# Attestation still valid
gh attestation verify oci://ghcr.io/dockermint/pebblify:0.4.1 \
  --owner Dockermint
```

All three MUST return expected values. If description remains empty after
B1, fall back to B2 (requires D2 re-vote).

## Risks

1. **Workflow regression.** Re-pushing `latest` after the attestation step
   could interfere with the referrer graph. Mitigation: canary on
   `v0.4.1-rc1` (D4). Rollback: revert workflow commit, retag.
2. **Long-lived PAT (if D2=B2).** Org-scope `write:packages` PAT is a broad
   credential. Mitigation: prefer B1; if B2 chosen, document rotation in
   `docs/markdown/SECURITY.md` and set 90-day expiry.
3. **GHCR UI behaviour is not a stable API.** Tag ordering heuristics could
   change; the description-inheritance path is undocumented. Mitigation: keep
   the fix minimal, avoid coupling release automation to UI behaviour beyond
   what is necessary.
4. **Double LABEL confusion.** If Dockerfile keeps `source` label and
   workflow also sets it, `docker inspect` can show either — depending on
   tooling. Mitigation: Fix C (single source of truth).

## Follow-ups

- `@technical-writer` adds "GHCR package settings one-time setup" section to
  `docs/markdown/release-automation.md` (B1 requires manual "Inherit from
  repository" click post first-release). Tracked as Feat 2 scope-adjacent or
  dedicated micro-PR post-v0.4.1.
