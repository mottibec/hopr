# Configuration and terminal adapters

Start with [the complete example](../examples/config.json). JSON is strict: unknown fields and trailing data are errors. Configure each host separately; hopr never writes this file or agent settings.

`hopr setup` is the explicit exception: it creates a new private Hopr configuration and refuses to replace one. It detects local executable paths and accepts `--host-id`, `--to`, `--remote-id`, `--project`/`--path`, `--backend`, and `--socket`. It does not configure SSH, install remote binaries, log into agents, or edit Herdr bindings. A bare setup creates a starter config requiring host/project mappings before a move.

| Field | Meaning |
| --- | --- |
| `host_id` | Stable, distinct label for this Mac. The other Mac's host mapping must use this exact ID. |
| `backend` | `herdr` (default), `tmux`, or `command`. Backends can differ between Macs. |
| `state_dir` | Private journal, ownership, package, backup, and lock directory. Default `~/.local/state/hopr`. |
| `workspace_root` | Required absolute path or `~/…` for isolated destination checkouts. Keep outside existing repositories. |
| `default_server` | Herdr server label from `servers`, or a tmux `-L` socket label. |
| `servers` | Herdr label → absolute Unix socket path. Obtain the actual path from your installation; do not assume all sessions use the example default. |
| `hosts` | Destination menu label → `{ "ssh": "SSH-alias-or-Tailscale-host", "id": "remote-host-id" }`. Configure both directions for return trips. |
| `projects` | Shared project key → local policy. Keys must match across Macs; home paths can differ. |
| `projects.*.path` | Exact source Git root on this Mac. A destination checkout under `workspace_root/<key>/<UUID>` is also recognized for return trips. |
| `projects.*.include_ignored` | Literal relative files/directories to include. No globs, traversal, `.git`, or symlink escapes. Empty by default. Policies must match on both hosts. |
| `projects.*.claude_memory` | Opt in to project auto-memory. Must match on both hosts; conflicting files are never replaced. |
| `projects.*.requirements` | Local executable argument arrays checked before stopping. Example: `[["/opt/homebrew/bin/node", "--version"]]`. Commands must be read-only. They run without a project cwd. |
| `executables` | Paths for `git`, `ssh`, `herdr`, `codex`, `claude`, `lsof`, and `tmux`. `~/` expands locally. |
| `codex_home`, `claude_home` | Native storage paths, defaulting to `~/.codex` and `~/.claude`. Used locally; never copied wholesale. A custom Claude home uses `CLAUDE_CONFIG_DIR` and needs its own existing authentication/config. The default leaves that variable unset so `~/.claude.json` stays in its native location. Herdr must be started with a compatible agent environment; default-home operation assumes its server/shell does not independently set a custom `CLAUDE_CONFIG_DIR`. |
| `timeout_seconds` | Lifecycle/API readiness timeout, 1–300, default 60. Subprocesses have a separate two-minute bound. |
| `adapter_command` | Required only for `command`: executable plus fixed arguments, without shell interpolation. |

SSH uses the receiver's own config at `~/.config/hopr/config.json`. Configure ports, users, identity files and known-host behavior in OpenSSH. Hopr does not disable host-key checking or copy SSH keys. Its fixed remote command is `exec "$HOME/.local/bin/hopr" receive`; request values travel through stdin. The receiver requires both the expected host identity and an allowed source-host mapping.

## Herdr

Use the [standard plugin](../plugins/herdr/README.md), or add [the simple popup binding](../examples/herdr.toml) yourself. Select the agent pane, press `prefix+m` (default: Ctrl+B, release, then plain m), and choose a configured destination. The simple popup reads `HERDR_ACTIVE_PANE_ID`; the plugin captures the action's pane, workspace, terminal, session reference and server incarnation before opening its popup and revalidates them before moving. Both require `HERDR_SOCKET_PATH` to match a configured local server. An explicit `--server` selects a configured server for CLI use; the plugin refuses mismatched inherited sockets. UI machine selection alone does not retarget hopr's local socket connection.

For Claude, `doctor` and destination preflight run `claude auth status --json`
inside a short-lived Herdr diagnostic workspace. macOS Keychain access can differ
between SSH and Herdr, so the receiver's SSH login check does not establish whether
the destination agent can authenticate. The probe uses the configured executable
and Claude home, returns only a nonce and success/error, and closes its own idle
workspace. It does not send a prompt, read tokens itself, or copy credentials.
Herdr must already be running in a context with access to that Mac's Claude login.
The tmux and command backends retain their direct authentication check.

If a probe is interrupted, it may leave a `hopr-auth-<UUID>` diagnostic workspace
and a private `auth-probes/<UUID>` directory under Hopr's state directory. Its
`request.json` identifies the workspace and configuration; `started` prevents
re-execution of the same probe and `result.json` holds only the outcome. A later
preflight runs a fresh check. No conversation is launched by these probes. Close
an abandoned diagnostic workspace only after checking it contains no work, then
remove its corresponding probe directory.

The optional [custom Herdr client](../integrations/herdr/README.md) adds a right-click action for workspaces and panes. Stock Herdr 0.9.0 has a fixed context menu; plugin `contexts` do not extend it. The patch invokes exactly one configured Hopr popup using the existing `command.invoke` API, scoped to the clicked pane and server. Installing the Hopr binary alone does not modify Herdr. The shortcut works with stock Herdr.

The built-in adapter requires Herdr 0.9.0 or 0.9.1 with protocol 22, a current official Codex/Claude integration, its exact `agent_session` UUID, idle/done state without a pending launch, and one supported foreground process. Manually started source agents do not have Herdr's managed `interactive_ready` flag; that flag is required on the destination, which Hopr starts using `agent.start`. It queries JSON APIs; it does not inspect screen text. Destination workspace labels are `hopr-<move-uuid>` for reconciliation. A socket incarnation and terminal identity guard against reused pane IDs.

Codex can delay its session hook until the first turn. Before that hook arrives,
destination readiness requires Herdr's managed interactive/idle state, exact
resume argv and cwd from `pane.process_info`, the running executable's verified
version, and that process holding only the expected UUID's native writer lock.
A conflicting reported identity is always rejected. This sends no prompt and
does not fabricate an integration report. Source selection still requires the
official session report; an immediate return before the first user turn can
therefore be refused. Claude requires its integration-reported identity.
Agent names use all UUID bits encoded in lowercase base32 to fit Herdr's
32-character limit; workspace labels retain the full UUID.

Codex may have the bundled `node_repl` at
`/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node_repl` and the
Computer History MCP executable under the configured Codex home. Hopr matches
their complete installed paths and commands; a process name alone is insufficient.
It records their identities before stopping and waits for both them and their
process groups to exit. New/unrecognized descendants, changed identities and
writable workspace/transcript descriptors remain blockers. Helpers at other
installation paths are currently unsupported. Fresh destination helpers use that
Mac's own configuration; no REPL memory or live MCP connection is transferred.

Unrelated Codex processes may remain running. The destination checks the UUID's
native writer lock before source shutdown, and holds it while planning/applying
the import. The source also holds this lock after shutdown while snapshotting and
transferring. Lock acquisition follows Codex's coordination-file protocol so
native stale-lock cleanup cannot invalidate the held lock. Update Hopr on both
Macs: journals and wire requests now include source helper identities; older
receivers reject these fields before stopping the source.

## tmux

The built-in tmux adapter uses 3.7b. It cannot infer native UUID/idle state from tmux. `hopr attest` explicitly asserts the exact session at an empty idle prompt, records its PID/start time, and expires after five minutes. Do this on the source before moving and on the destination after checking its resumed conversation. `hopr recover` can then confirm readiness. The transfer engine and native history rules are identical to Herdr's.

## Command adapter protocol 1

Set `backend` to `command` and `adapter_command` to an executable argument array. Each call starts a short-lived subprocess with one JSON request on stdin and expects one JSON reply on stdout. Use stderr for logs. The authoritative Go wire types are `AdapterRequest`, `AdapterReply`, `Session`, and `Journal` in `internal/handoff`. Missing booleans are false. Return an `error` object with `code` and `message` to refuse an action. Native codecs remain Codex/Claude only.

Example inspect request and partial reply shape:

```json
{"protocol":1,"action":"inspect","pane":"selected-pane","server":"local"}
```

```json
{"session":{"host":"m2","server":"local","server_token":"incarnation-42","pane":"selected-pane","terminal":"terminal-uuid","agent":"codex","id":"11111111-2222-4333-8444-555555555555","cwd":"/Users/alice/project","pid":12345,"shell_pid":12340,"process_start":"exact-ps-start-value"}}
```

These are examples, not fake evidence an adapter may return. Obtain identities/readiness from supported manager/agent interfaces, or require explicit operator attestation. Never infer readiness from an existing PID or terminal text. Hopr assigns the project/policy, validates native identity, pins the executable version, and adds generic process/writer checks.

| Action | Input | Required reply / behavior |
| --- | --- | --- |
| `inspect` | `pane`, `server` | Complete `session`, including exact native UUID, cwd, PID/start, terminal identity, server incarnation. Require idle. |
| `preflight` | `session`, `target` | Verify backend support, identity/lifecycle capability and destination readiness without stopping anything. `{}` on success. |
| `guard` | `session`, `stopped` | Revalidate selection, idle state or process cessation, and reject concurrent writers. |
| `stop` | `session` | Gracefully exit that exact agent only. Never kill a server. Unknown outcome is an error, not a retry. |
| `stopped` | `session` | `{"stopped":true}` only after verifying the native process cannot write. |
| `retire` | `session` | Idempotently remove that stopped pane's automatic-resume registration without affecting other sessions. |
| `find_workspace` | `label` | Return `pane`, `terminal`, `server_token` for exactly one matching workspace; empty `pane` if absent, error if ambiguous. |
| `create_workspace` | `label`, `path` | Create an isolated, idle destination pane; return the same three identities. |
| `launch` | `journal` | Launch the same native agent with exact resume UUID and destination cwd. Submit no prompt. |
| `ready` | `journal` | `{"ready":true}` only when the exact UUID is interactively idle in the expected terminal/path/server incarnation. |

Treat every unknown stop/create/launch outcome conservatively. Hopr records intent before calling; subsequent recovery observes instead of repeating those effects. A generic adapter is trusted local code, not a sandbox. No additional terminal backend is advertised as tested just because it could implement this contract.
