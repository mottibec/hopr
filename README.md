# hopr

Move one idle Codex CLI or Claude Code conversation and its Git workspace between Apple Silicon Macs over ordinary SSH/Tailscale. **No CCT, cloud service, Git push/pull, temporary remote branch, or custom background daemon is required.** The Go binary has no third-party Go dependencies.

This is an application-level **stop → snapshot → transfer → import → native resume** tool. It does not move process memory, terminal scrollback, shell environment, running commands, background services, approvals, or live tool connections. Start services separately on the destination. The original checkout and a recoverable package remain on the source. Once the move is `complete`, the source Mac can be switched off.

The terminal manager is an adapter, separate from the transfer engine:

| Backend | Session selection and readiness |
| --- | --- |
| **Herdr** | Exact integration-reported UUID and idle/done source state; managed interactive readiness on the destination. Popup or CLI. |
| **tmux** | Explicit operator identity/idle attestations, bound to the native PID and server. tmux cannot report conversation readiness itself. |
| **Command adapter** | Documented JSON-over-stdin contract for other terminal managers. No terminal-output scraping. |

An M2 and an M4 can use different terminal backends. Native agent types must match. The built-in native adapters support Codex and Claude; other AI agents require a new native codec and lifecycle adapter.

## Build and install

Requires Go 1.23+ to build, Git, OpenSSH, and macOS ARM64 to operate. Go is not needed on the destination if you copy the binary. The build was verified with Go 1.27.1.

```sh
make build
make test
make race
sh scripts/install.sh
```

This produces `bin/hopr`, a macOS ARM64 binary. Install it at **`~/.local/bin/hopr` on both Macs**. The SSH transport invokes the fixed command `exec "$HOME/.local/bin/hopr" receive`, so it works even when noninteractive SSH has a minimal PATH. The installer refuses to overwrite an existing binary and does not alter configuration.

Create the configuration on each Mac:

```sh
# Work Mac
hopr setup --host-id work-mac --to workbox --remote-id private-mac \
  --project my-app --path ~/sources/my-app

# Private Mac: work-mac-ssh must be an SSH alias that reaches the work Mac
hopr setup --host-id private-mac --to work-mac-ssh --remote-id work-mac \
  --project my-app --path ~/projects/my-app

hopr doctor --json
```

`setup` creates `~/.config/hopr/config.json`, detects executable paths and the inherited Herdr socket (or its standard default), and refuses to overwrite an existing config. Use `--socket /absolute/socket` for another Herdr server or `--backend tmux` for tmux. Project paths must exist; setup does not clone repositories. The receiver still creates an isolated checkout when moving.

Without `--project`/`--path`, setup creates a starter config; add project mappings before the first move. The same project key must have each Mac's own path. Edit [the example](examples/config.json) for ignored files, Claude memory, requirements, additional hosts, or advanced adapters. `doctor` checks local tools, authentication and the live Herdr protocol; successful doctor output alone does not mean a project/session passes every move preflight. Keep `state_dir` and `workspace_root` outside your repositories. Existing state/workspace directories must have mode `700`; files created by hopr are private.

`--config PATH` or `HOPR_CONFIG` selects a local configuration. The receiver reads its **own** configuration and credentials; the source cannot supply a remote configuration path.

Initial built-in compatibility is deliberately narrow: **Codex 0.160.0, Claude Code 2.1.287, Herdr 0.9.0/0.9.1 (protocol 22), tmux 3.7b**. Unknown versions fail before stopping a source. Codex must be launched with `--no-daemon`, because exiting a TUI connected to a shared app-server does not establish that its session is stopped.

## First move

1. Confirm normal SSH works with host-key verification and without a password prompt. Tailscale connectivity alone does not configure macOS Remote Login or SSH authentication.
2. Run `hopr doctor` on both Macs and `hopr doctor --to m4` on the source. Each Mac needs its own agent login and installed project dependencies.
3. Configure the official Herdr native-session integration yourself if needed. hopr never edits agent settings or installs hooks silently.
4. Select an idle agent in one Git repository, with no draft, background jobs, unsupported child processes, shared app-server source, or other workspace writers. Codex's recognized Node REPL and Computer History helpers are tracked and must exit with the source before export. Do not type into either agent during the handoff.
5. Move the exact pane:

```sh
hopr move --pane w1:p1 --to m4 --server default --json
hopr status <move-uuid> --json
hopr recover <move-uuid> --json
```

An optional `--id <uuid>` supplies a durable idempotency key. Record the returned UUID even when the command reports an error. A repeated operation for that UUID never launches a second agent.

The destination checkout is `workspace_root/<project-key>/<move-uuid>`. It is always isolated; an unrelated existing checkout is never reused or overwritten. The launch arguments are `codex resume <uuid> --cd <checkout> --no-daemon` or `claude --resume <uuid>` in the checkout. No instruction is automatically submitted. Trust/authentication dialogs can block readiness; resolve them in that destination pane, then recover.

Keep the destination pane open until completion. Codex may defer its Herdr
session report until your first turn; Hopr can verify destination readiness from
the exact resume process and its held native UUID lock. Source selection still
needs the integration report, so moving back immediately before your first turn
may be refused.

After snapshotting, hopr retires **only the selected source pane**, once it has become an idle shell. This prevents Herdr's native-session restore from resurrecting the source after a later server restart. It never stops the Herdr server. The source checkout, native transcript, and package remain recoverable.

## Herdr popup

The [standard Herdr plugin](plugins/herdr/README.md) packages Hopr as an action and
popup for stock Herdr. Link it with `herdr plugin link "$PWD/plugins/herdr"`, then
add [examples/herdr-plugin.toml](examples/herdr-plugin.toml) to your Herdr config
and run `herdr server reload-config`. Select the agent pane, press **Ctrl+B**,
release, then plain **m**, and choose the destination. This uses Herdr's default
prefix; a customized prefix changes the first step.

Alternatively, use the simple popup without installing the plugin. Add
[examples/herdr.toml](examples/herdr.toml) yourself (choose one binding):

```toml
[[keys.command]]
key = "prefix+m"
type = "popup"
command = '"$HOME/.local/bin/hopr" menu'
description = "Move agent to another Mac"
```

`menu` reads `HERDR_ACTIVE_PANE_ID` for the underlying pane. It resolves `HERDR_SOCKET_PATH` only against configured servers. Unknown inherited sockets fail. `--server` overrides the source explicitly; pane IDs are scoped to that server. Selecting another machine in Herdr's UI does not retarget CLI commands. Run hopr on the host that owns the pane.

The stock Herdr workflow is **select pane → shortcut → choose Mac**. Neither the
plugin nor the simple popup adds right-click entries to stock Herdr. The optional
[custom Herdr client](integrations/herdr/README.md) supplies that separate feature
and needs its own maintenance; it is unnecessary for the plugin.

Destinations can have an optional `label`, such as `"Home"`, in `hosts`. Labels are display-only; SSH aliases and verified host identities remain explicit. Menu results stay visible until Enter.

Herdr's experimental `--handoff` replaces its server on the same host; it is unrelated to hopr's cross-host application handoff.

## tmux

Set `backend` to `tmux` and `default_server` to your tmux socket label (`default` normally). No Herdr installation or server is used. The CLI takes exact `%N` pane IDs. At an empty, idle native agent prompt, explicitly assert the session identity:

```sh
hopr attest --pane %1 --agent codex --session <native-uuid>
hopr move --pane %1 --to m4
```

An attestation expires after five minutes and is tied to the process start time. On a tmux destination, attach to session `hopr-<move-uuid>`, verify the exact resumed conversation and idle prompt, run `hopr attest` for its reported pane/UUID, then `hopr recover <move-uuid>`. There is no automatic readiness claim from process existence. Source panes should run their agent from a persistent shell; direct-exec panes that disappear with their server can require manual recovery.

## What transfers

| State | Handling |
| --- | --- |
| Current HEAD and unpushed ancestor commits | Self-contained Git bundle, transferred directly over SSH. No remote repository required. |
| Branch name and staged state | Recreated branch and verified Git index tree; binary-capable staged patch. |
| Unstaged edits, binary data, deletions, executable bits | Complete selected file manifest, independently checked against the recorded unstaged binary patch. |
| Relative in-repository symlinks | Preserved without following them. External/absolute symlinks are rejected. |
| Untracked files | Included, except Git-ignored files. |
| Ignored files | Only explicit relative files/directories in `include_ignored`; no glob expansion. Configure the same inclusions on both hosts. |
| Linked worktree | Reconstructed as an independent repository; source `.git` pointer is never copied. |
| Native conversation | Exact UUID JSONL transcript. Only structural cwd metadata is remapped; message text and historical commands are preserved. |
| Claude project auto-memory | Opt-in on both hosts via `claude_memory`; differing destination files are rejected. |

Compatible return-trip histories are compared as JSON records, preserving numeric precision and allowing only workspace-path normalization. The destination must be a prefix of the incoming history. Codex keeps its existing prefix byte-for-byte and appends new records; Claude imports into the new checkout's project namespace while preserving old files. Divergence or a destination that is ahead is rejected. Backups and import preimages permit interrupted-import recovery.

## Limits and safety boundaries

- One idle native agent and one exact configured SHA-1 Git root per move. No submodules, Git LFS, custom clean/smudge filters, working-tree encodings, sparse/partial/shallow repositories, conflict/rebase/merge/bisect state, stash/replace refs, alternates, intent-to-add, or unusual index flags.
- Only current-HEAD history and current branch are restored. Other branch/tag refs, remotes, hooks, repository-local config, reflogs, submodule repositories, extended attributes, ACLs, resource forks, and empty untracked directories are not transferred. Local remotes can be configured afterward without needing network access for this move.
- A 256 MiB encoded-package cap bounds memory and transfer size. Large history can exceed it even with a small worktree. Destination requires at least 768 MiB available. Transfers are whole-package retries, not resumable chunks.
- Only uncompressed native JSONL sessions are supported. Claude sidechains/auxiliary session directories and referenced local images/assets are rejected. Inline content remains. External URLs, historical absolute paths inside message/tool text, downloaded assets, Codex global memory, plugins, user skills/configuration, credentials, sandbox settings, model availability, and external services are not migrated.
- Known secret patterns in selected files/transcripts/memory block transfer, without printing matching values. This is a conservative detector, **not** a proof that arbitrary secrets are absent; Git object history is not scanned. There is no secret-check bypass flag.
- Process and `lsof` checks reject unresolved same-user writers and unsupported agent children. Recognized Codex helpers are recorded by PID/start time, executable, full command and process group; surviving helpers or their groups block export and recovery. Hopr never force-kills them. Their in-memory state is not migrated. These are cooperative safeguards, not a filesystem freeze or defense against another user/root. Keep both workspaces untouched until completion.
- Other Codex sessions may run on either Mac. Hopr uses Codex's per-conversation writer lock during source snapshot/transfer and destination import, plus an exact-transcript writer check. An active writer for the same UUID blocks the move. Co-resident Codex builds with verified locking are 0.160.0, 0.159.3, 0.159.0, 0.159.0-alpha.12 and 0.159.0-alpha.12.1; unknown versions fail explicitly. The moved agent still requires 0.160.0. Claude retains its conservative destination process restriction.
- Unknown stop/create/launch outcomes remain recoverable but may require operator intervention. hopr never automatically resumes the source. See [recovery](docs/recovery.md).

## Further documentation

- [Configuration and adapter contract](docs/adapters.md)
- [Recovery and ownership](docs/recovery.md)
- [Verified interfaces](docs/interfaces.md)
- [Test results and two-Mac verification](docs/verification.md)

## Exit codes and uninstall

`--json` emits one `{"ok":...,"result":...,"error":...}` object. A failed move can include its journal in `result`; retain its UUID. Receiver stdout is exclusively the private protocol.

| Code | Meaning |
| --- | --- |
| 0 | Completed successfully |
| 1 | Internal or I/O error |
| 2 | Usage/configuration error |
| 3 | Unsupported state/version, missing dependency/authentication, secret gate, or operator action required |
| 4 | Busy, conflict, or invalid package |
| 5 | Uncertain outcome; status/recovery required |
| 6 | Move not found |

To uninstall, remove the installed `~/.local/bin/hopr` binary and any popup binding you added. If installed, unlink/uninstall the [Herdr plugin](plugins/herdr/README.md#test-and-uninstall). Retain `~/.local/state/hopr` and `HoprWorkspaces` until all moves are resolved and you have backed up the desired work. Removing the binary does not delete checkouts or native conversations. Delete only explicitly chosen fixture/checkpoint directories later; do not delete an active checkout.
