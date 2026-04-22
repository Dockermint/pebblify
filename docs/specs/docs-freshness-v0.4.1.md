# Feature: Documentation Freshness Fixes (v0.4.1)

## Context

Post-v0.4.0 audit surfaced three documentation defects that mislead operators
and break GitHub conventions. No code change, docs-only patch release.

- Roadmap entry: `docs/ROADMAP.md` → "v0.4.1 — Documentation Freshness".
- Related spec: `docs/specs/documentation-refresh.md` (v0.4.0 refresh, does not
  cover these issues).

## Requirements

1. [confirmed] Install doc must reference a version that exists on GHCR.
   `docs/docusaurus/install-podman.mdx:149` currently pulls `v0.5.0`; repo is
   at `v0.4.0`.
2. [confirmed] `README.md:211` link text `[LICENSE]` points at path `LICENCE`.
   Either rename file or fix link text — decision pending CEO (see §4).
3. [assumed — needs confirmation] Add minimal `CONTRIBUTING.md` stub. Several
   community expectations (GitHub's "New contributor" banner, some CI templates)
   key off its presence; currently absent.

## Scope

Exact fixes (file + line):

- `docs/docusaurus/install-podman.mdx:149` — replace `v0.5.0` with `v0.4.0`
  to match the repo-wide pinning pattern (every other doc pin is `v0.4.0`, no
  `:latest` usage found). Keep explicit version pinning.
- `README.md:211` — resolve the link/filename mismatch per CEO decision (§4a).
- Repo root — optionally add `CONTRIBUTING.md` stub per CEO decision (§4b).
  Stub sections: PR process pointer to `CLAUDE.md` workflow (steps 1–19),
  Conventional Commits, branching model (`develop` → `main` via feature
  branches), CodeRabbit expectation, link to issue templates.

## Non-goals

- Full documentation rewrite or reorganisation.
- Translation / i18n work.
- Docusaurus theming, plugin, or navigation changes.
- Backfilling release notes for pre-v0.4.0 versions.
- Adding a CI link-check workflow (separate concern, route through `@devops`
  if desired later).

## Decision Points (CEO)

**(a) `LICENCE` vs `LICENSE` (README.md:211).**

Options:

1. **Rename `LICENCE` → `LICENSE`** (recommended). Apache-2.0 upstream ships
   the file as `LICENSE`; GitHub's license-detection heuristic expects
   `LICENSE[.md|.txt]` and will surface the license badge on the repo
   landing page. `@sysadmin` performs `git mv`.
2. **Keep `LICENCE`** (British spelling) and fix the README link text to
   `[LICENCE](LICENCE)`. Preserves current filename; loses GitHub license
   auto-detection.

Question for CEO: was `LICENCE` an intentional British-spelling choice? If
yes, option 2. If incidental, option 1.

**(b) Add `CONTRIBUTING.md` stub?**

Options:

1. **Yes, minimal stub** (recommended). ~30 lines, points at `CLAUDE.md`
   workflow, Conventional Commits, branching, CodeRabbit, issue templates.
   Owner: `@technical-writer`.
2. **No.** Defer until external contributions materialise.

## Acceptance Criteria

- `grep -rn 'v0\.5\.0' docs/` returns 0 hits (or only hits explicitly tagged
  as future-version examples with adjacent comment).
- `grep -n 'LICENCE\|LICENSE' README.md` link target matches the on-disk
  filename (case-sensitive).
- If option 4a.1 chosen: `ls LICENSE` resolves; `ls LICENCE` does not; GitHub
  repo landing page shows the "Apache-2.0" license badge after merge.
- All internal relative links in `README.md`, `CONTRIBUTING.md` (if added),
  `docs/docusaurus/install-podman.mdx`, and any file touched in this patch
  resolve. Verification via `markdown-link-check` against changed files OR
  manual click-through. No external-URL checks required.
- No new v0.4.1 features shipped; patch is docs-only.

## Owning Agent

- **Primary**: `@technical-writer` — all edits to `README.md`,
  `docs/docusaurus/install-podman.mdx`, and new `CONTRIBUTING.md` stub.
- **Git operations**: `@sysadmin` — `git mv LICENCE LICENSE` if option 4a.1
  selected; branching, commit, PR.
- **Review**: `@reviewer` — verdict APPROVE/BLOCK against this spec.

## Risks

Low.

- Link breakage on the rename: mitigated by grep for `LICENCE` across repo
  before the `git mv` (expect only `README.md:211` reference).
- Docusaurus build break on the `v0.5.0` → `v0.4.0` edit: none expected
  (plain string swap inside a code fence).
- `CONTRIBUTING.md` scope creep: mitigated by the "minimal stub" constraint;
  spec forbids expanding beyond the listed sections in this patch.

## CEO Decisions (locked 2026-04-22)

- **Q2a — LICENCE → LICENSE rename: CONFIRMED.** `@sysadmin` performs
  `git mv LICENCE LICENSE` on the Feat 2 feature branch. README.md:211 link
  target updates to `LICENSE`.
- **Q2b — CONTRIBUTING.md stub: DEFERRED.** Not in scope for v0.4.1. Track
  as backlog item for future release.
- **Release vehicle: CONFIRMED `v0.4.1`.** Bundled with Feat 1 (godoc) and
  Feat 3 (OCI) under one consolidation PR.
