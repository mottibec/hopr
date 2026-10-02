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

Add [the popup binding](../examples/herdr.toml) to Herdr yourself. Select the agent pane, press `prefix+alt+m`, and choose a configured destination. The popup reads `HERDR_ACTIVE_PANE_ID`; `HERDR_SOCKET_PATH` must match a configured local server. An explicit `--server` selects a configured server. UI machine selection alone does not retarget hopr's local socket connection.

**There is no right-click menu entry in this release.** Herdr 0.9.0's [context-menu implementation](https://github.com/herdrdev/herdr/blob/v0.9.0/src/client/shell/context_menu.rs) builds a fixed menu. Its plugin action `contexts` field does not add an entry there. A Herdr change would need to add the menu action, carry the clicked pane and owning server to a popup, and invoke hopr on that host. The documented popup binding works without a Herdr fork.

The built-in adapter requires protocol 22, a current official Codex/Claude integration, its exact `agent_session` UUID, interactive readiness and idle state, and one supported foreground process. It queries JSON APIs; it does not inspect screen text. Destination workspace labels are `hopr-<move-uuid>` for reconciliation. A socket incarnation and terminal identity guard against reused pane IDs.

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
