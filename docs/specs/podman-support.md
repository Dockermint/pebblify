# Spec: Podman Quadlet Support

Owner: `@software-architect`
Date: 2026-04-18
Last revised: 2026-04-18 (CEO decisions locked — cross-ref daemon-mode.md)
Feature branch: `feat/podman-support`
Implementation owners: `@lead-dev` (Makefile), `@container-engineer` (Quadlet file)

---

## Objective

Provide a single `make install-podman` target that deploys Pebblify daemon as a rootless Podman Quadlet for the current user. No root required. No systemd unit is enabled by the target; the operator does that manually.

**Platform**: Podman Quadlet requires a Linux host with systemd user session support. macOS users may use Podman Desktop, which provides a Linux VM; the `install-podman` target is not guaranteed to work on macOS without Podman Desktop's VM layer. The target does not guard against macOS explicitly but emits a clear warning if `systemctl --user status` is unavailable.

---

## Prerequisite

Feature branch `feat/daemon-mode` must be merged to `develop` before this branch is cut. The Quadlet depends on the daemon config path `~/.pebblify/config.toml` established in the daemon spec.

---

## Makefile Target (`@lead-dev`)

Target name: `install-podman`

Location: `Makefile` (owned by `@lead-dev`)

Requirements:
- Check that `podman` is on `PATH`; exit with a clear error message if not found.
- Check that systemd user session is available (`systemctl --user status`); warn but do not exit if not (some CI environments lack it).
- Create `~/.config/containers/systemd/` if it does not exist.
- Copy (or generate) the Quadlet file to `~/.config/containers/systemd/pebblify.container`. Do NOT overwrite if the file already exists (emit a message and skip).
- Create `~/.pebblify/` directory (mode 0700) if it does not exist.
- Create `~/.pebblify/config.toml` from an embedded template with sane defaults. Do NOT overwrite if already exists.
- Create `~/.pebblify/.env` with all `PEBBLIFY_*` env var stubs as empty strings. Mode 0600. Do NOT overwrite if already exists.
- Emit a post-install message:

```
Pebblify Quadlet installed.
Next steps:
  1. Edit ~/.pebblify/config.toml
  2. Fill in secrets: ~/.pebblify/.env
  3. Reload user units: systemctl --user daemon-reload
  4. Start: systemctl --user start pebblify
  5. Enable on login: systemctl --user enable pebblify
```

---

## Quadlet File (`@container-engineer`)

File path: `~/.config/containers/systemd/pebblify.container`

This file is authored by `@container-engineer`. The spec defines required directives; `@container-engineer` fills in values and any additional directives per Quadlet format rules.

### Required Directives

```ini
[Unit]
Description=Pebblify daemon (LevelDB → PebbleDB conversion service)
After=network-online.target

[Container]
Image=ghcr.io/dockermint/pebblify:v0.4.0
Exec=daemon
PublishPort=2324:2324
PublishPort=2325:2325
PublishPort=2323:2323
Volume=%h/.pebblify/config.toml:/etc/pebblify/config.toml:ro,z
Volume=pebblify-snapshots:/snapshots:z
EnvironmentFile=%h/.pebblify/.env

[Service]
Restart=on-failure

[Install]
WantedBy=default.target
```

Notes:
- `%h` is the systemd specifier for `$HOME`; valid in Quadlet files.
- `:z` relabels the volume for SELinux; safe to include even on non-SELinux systems.
- Port 2324 = API, port 2325 = Health, port 2323 = Telemetry.
- `Exec=daemon` sets the container command to `pebblify daemon`.
- `User=` directive is NOT needed; Quadlet runs rootless by default as the invoking user.
- `pebblify-snapshots` is a named Podman volume; `@container-engineer` decides whether to also provide a `pebblify-snapshots.volume` Quadlet file or document manual creation.

### Volume for snapshots

`config.toml` inside the container must point `save.local.local_save_directory` to `/snapshots`. The default template created by `make install-podman` pre-sets this value.

---

## Scope Boundary Clarification

| Task                              | Owner                 |
| :-------------------------------- | :-------------------- |
| `Makefile` target `install-podman`| `@lead-dev`           |
| Quadlet `.container` file content | `@container-engineer` |
| Optional `.volume` Quadlet file   | `@container-engineer` |
| systemd `.service` unit (system-wide daemon) | `@container-engineer` — see daemon-mode.md handoff |
| `~/.pebblify/config.toml` template| `@lead-dev` (embedded in Makefile) |
| `~/.pebblify/.env` stub template  | `@lead-dev` (embedded in Makefile) |

`@lead-dev` does not write the `.container` file content. `@container-engineer` does not write Makefile lines.

The Makefile target references the `.container` file by copying it from the repository (a checked-in template at a path owned by `@container-engineer`) or by embedding it inline in the Makefile as a heredoc. The two agents must coordinate on the delivery mechanism. Architect recommendation: `@container-engineer` checks in `deploy/quadlet/pebblify.container` as the canonical template; `@lead-dev` copies it in the Makefile target. This keeps the Quadlet syntax exclusively in `@container-engineer`'s scope.

**Cross-reference**: `@container-engineer` also owns `systemd/pebblify.service` and `systemd/pebblify.env.example` (system-wide daemon install, used by `install-systemd-daemon`). See `docs/specs/daemon-mode.md` — "systemd Unit Ownership" section. Both the Quadlet file and the systemd unit are `@container-engineer`'s deliverables; scope amendment to CLAUDE.md is required before implementation begins (see `docs/roadmap/v0.4.0.md` — "CLAUDE.md Amendment Required").

---

## Out of Scope

- Podman Pod support (multiple containers in a pod).
- Podman secrets integration (beyond the `.env` file approach).
- Auto-update via `podman auto-update` / `io.containers.autoupdate` label — may be added by `@container-engineer` as an optional directive.
- Root-mode Podman (rootful); this spec is rootless-only.

---

## Hand-off

| Agent               | Scope files                                                    | Action                                     |
| :------------------ | :------------------------------------------------------------- | :----------------------------------------- |
| `@lead-dev`         | `Makefile`                                                     | Add install-podman target                  |
| `@container-engineer` | `deploy/quadlet/pebblify.container` (new file in their scope); also `systemd/pebblify.service` and `systemd/pebblify.env.example` (owned in daemon-mode branch) | Author Quadlet template; pending CLAUDE.md scope amendment for systemd files |
| `@qa`               | no test files for Makefile targets; document manual verify     | —                                          |
| `@reviewer`         | read-only                                                      | APPROVE or BLOCK                           |
| `@sysadmin`         | git operations                                                 | Branch, commit, PR                         |
| `@technical-writer` | invoked post-merge                                             | Document Podman install in docs/; note Linux-native and macOS-via-Podman-Desktop paths |
