# Spec: Documentation Refresh — v0.4.0

Owner: `@software-architect`
Date: 2026-04-18
Feature branch: `docs/release-v0.4.0`
Implementation owner: `@technical-writer`

---

## Objective

Update all user-facing documentation to cover the v0.4.0 feature set: daemon mode, CI attestations, Podman support, and the new Makefile targets. This work is invoked post-merge (step 14 of the CLAUDE.md workflow), after all three feature branches are on `main`.

**Platform constraint for docs**: `pebblify daemon` is Linux-only at runtime. All install documentation must be split by platform:
- `install-cli`: cross-platform (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64). Document for all four targets.
- `install-systemd-daemon`: Linux-only. Clearly labeled. Do not imply macOS support.
- `install-podman` / Quadlet: Linux-native; macOS users use Podman Desktop (which provides a Linux VM). Document both paths with clear platform callouts.
- No `install-launchd-daemon` documentation exists or should be mentioned.

macOS users who want to run the daemon must use Docker or Podman. The quickstart and daemon docs must include a "macOS users" callout directing them to the Docker Compose or Podman Quadlet path.

---

## Files to Produce or Update

All files listed below are in `@technical-writer`'s exclusive write scope. No other agent touches these files.

### `README.md`

Additions:
- New section **Daemon mode** between the existing CLI usage section and the Docker section. Content: one-paragraph description, quickstart (download config, fill .env, run `pebblify daemon` on Linux or via Docker/Podman on macOS), link to `docs/markdown/daemon-quickstart.md`. Include a "macOS users" callout directing to Docker Compose or Podman path.
- Update **Installation** section: split into three subsections: (1) CLI install (`make install-cli`, cross-platform), (2) Daemon install (`make install-systemd-daemon`, Linux-only, clearly labeled), (3) Podman install (`make install-podman`, Linux-native; macOS via Podman Desktop).
- Add **Podman** subsection in Installation referencing `make install-podman`.
- Update **Build** section: note that darwin/amd64 and darwin/arm64 binaries are now included in releases.
- Add **Artifact attestation** paragraph: how to run `gh attestation verify` with the GHCR image and release binaries.

Do not remove or reorganize existing CLI content. Additions only, plus targeted in-place updates to Installation and Build.

### `docs/markdown/`

New files:

| File                                    | Content                                                                 |
| :-------------------------------------- | :---------------------------------------------------------------------- |
| `docs/markdown/daemon-quickstart.md`    | 5-minute tutorial: install, config.toml minimal setup, run daemon, POST job via curl |
| `docs/markdown/daemon-config.md`        | Full config schema reference: every TOML key, type, default, env override |
| `docs/markdown/daemon-api.md`           | API reference: POST /v1/job, GET /v1/status, auth modes, error codes    |
| `docs/markdown/telegram-integration.md` | Step-by-step: create Telegram bot, obtain token, set channel_id         |
| `docs/markdown/s3-setup.md`             | S3 config walkthrough: IAM policy, bucket, access key, save_path        |
| `docs/markdown/release-automation.md`  | CI attestation docs: workflow overview, gh attestation verify examples  |

### `docs/docusaurus/`

New MDX files (site-facing user guide):

| File                                          | Content                                                          |
| :-------------------------------------------- | :--------------------------------------------------------------- |
| `docs/docusaurus/install-cli.mdx`             | CLI install options (go install, binary download, make install-cli) — cross-platform, all four targets |
| `docs/docusaurus/install-systemd-daemon.mdx`  | make install-systemd-daemon walkthrough + systemctl steps — **Linux only**, labeled prominently |
| `docs/docusaurus/install-podman.mdx`          | make install-podman walkthrough + systemctl --user steps — Linux-native; macOS-via-Podman-Desktop path documented separately within the same page |
| `docs/docusaurus/daemon-quickstart.mdx`       | Adapted from markdown quickstart; Docusaurus admonitions/tabs OK |
| `docs/docusaurus/configuration-reference.mdx` | Full TOML key reference with collapsible sections per [section]  |
| `docs/docusaurus/telegram-integration.mdx`   | Adapted from markdown; step-by-step with screenshots placeholders |
| `docs/docusaurus/s3-setup.mdx`               | Adapted from markdown                                            |

---

## Content Requirements

### Daemon config reference

Must document every TOML key with:
- Key path (e.g. `api.authentification_mode`)
- Type
- Default value
- Valid values or format
- Corresponding env var (if any)
- Security note (if key interacts with secrets)

### API reference

Must include:
- Full request/response JSON schemas
- All HTTP status codes returned by each endpoint and their meaning
- `curl` examples for both auth modes
- A note that `authentification_mode = unsecure` is not recommended for production

### Attestation docs

Must include:
- Explanation of SLSA provenance and SBOM
- Exact `gh attestation verify` commands for binaries and Docker image
- Link to https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations

---

## Tone and Style

- Technical audience: blockchain node operators and DevOps engineers.
- Direct, imperative voice. No marketing filler.
- Code blocks for all CLI commands, config snippets, and JSON examples.
- No emojis in documentation files.

---

## Hand-off

| Agent               | Scope files                                                               | Action                             |
| :------------------ | :------------------------------------------------------------------------ | :--------------------------------- |
| `@technical-writer` | `README.md`, `docs/markdown/*.md`, `docs/docusaurus/*.mdx`               | Produce all files listed above     |
| `@reviewer`         | read-only                                                                 | Audit for CLAUDE.md compliance     |
| `@sysadmin`         | git operations                                                            | Branch, commit, PR                 |

Note: `@technical-writer` should reference `docs/specs/daemon-mode.md`, `docs/specs/podman-support.md`, and `docs/specs/ci-attestations-arm64.md` as the authoritative source for all technical details. Do not invent behavior not covered in the specs.
