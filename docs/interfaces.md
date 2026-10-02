# Verified interfaces and scope

Research date: 2026-10-02. Runtime is implemented in Go with the standard library. CCT was inspected during the initial interface research but is not used, installed, invoked, or embedded in hopr. Export, import, history comparison, path mapping and Git packaging are implemented locally.

## Herdr

Read the official [CLI reference](https://herdr.dev/docs/cli-reference/), [custom commands](https://herdr.dev/docs/configuration/#custom-command-keybindings), [connecting machines](https://herdr.dev/docs/connecting-machines/), [session state](https://herdr.dev/docs/session-state/#live-handoff), [socket API](https://herdr.dev/docs/socket-api/), and [plugins](https://herdr.dev/docs/plugins/). Installed CLI help and `herdr api schema --json` were inspected locally (0.9.0) and on the Air (0.9.1), both protocol 22.

Hopr uses newline-delimited JSON requests over a configured local Unix socket: `id`, `method`, `params`, then matching response `id` and `result`/`error`. Methods and relevant schema fields:

| Method | Fields used |
| --- | --- |
| `ping` | `protocol`, `version` |
| `integration.list` | `integrations[].target`, `available`, `state` |
| `agent.get` | `target`; result `agent` with pane/terminal identities, cwd, foreground cwd, status, readiness and native session reference |
| `pane.process_info` | `pane_id`; result `process_info.shell_pid`, `foreground_processes[].pid/name/argv/cwd` |
| `pane.send_keys` | `pane_id`, `keys` |
| `pane.list` | Optional `workspace_id`; result `panes` |
| `pane.get` | `pane_id`; result `pane` with `pane_id`, `workspace_id`, `terminal_id`, optional `agent_session` |
| `plugin.pane.open` | `plugin_id`, `entrypoint`, `placement: popup`, string-map `env` |
| `pane.close` | `pane_id` for the stopped source only |
| `workspace.list` | Result `workspaces[].workspace_id/label` |
| `workspace.create` | `cwd`, `label`, `env`, `focus`; result `root_pane` |
| `agent.start` | `name`, `kind`, `pane_id`, native `args`, `timeout_ms`; result `agent` |

For source selection, `agent_session` must have `source: herdr:codex` or `herdr:claude`, matching `agent`, `kind: id`, and the exact UUID. A pane ID alone is never treated as global.

Codex may defer its session hook until the first turn ([Herdr issue #3517](https://github.com/herdrdev/herdr/issues/3517)). Herdr 0.9.0/0.9.1's `persisted_session_from_launch_args` recognizes only the two arguments `resume <id>`; Hopr also needs `--cd` and `--no-daemon`. Destination readiness can instead combine managed interactive/idle status, exact resume argv/cwd, the running Codex version and its open, held per-UUID writer lock. `lsof` identifies the process's lock file and nonblocking `flock` verifies it is held while native namespace coordination excludes cleanup. An open but unlocked file, multiple thread locks, a different UUID, or a conflicting integration report does not qualify. No terminal text or synthetic prompt/report is used.

Herdr's experimental live handoff replaces a server on the **same host**. Hopr implements a separate application handoff across hosts. Herdr's right-click menu is fixed in the inspected 0.9.0 source; plugin action context metadata does not extend it. The supplied shortcut supports a [standard plugin action](../plugins/herdr/README.md) or a simple custom-command popup. An optional [client patch](../integrations/herdr/README.md) adds the right-click action using existing `command.invoke` parameters `command_id`, `workspace_id`, `tab_id`, `pane_id`, and `selection`, without changing the wire schema.

The plugin reads `HERDR_PLUGIN_CONTEXT_JSON.focused_pane_id` and `workspace_id`
from the action invocation. Herdr 0.9.0 builds popup context from current focus,
so Hopr separately captures the original selection and passes it through the
documented popup `env` map. The popup checks `HERDR_PLUGIN_ID`,
`HERDR_PLUGIN_ENTRYPOINT_ID`, `HERDR_SOCKET_PATH` and the captured identities.
The manifest uses argv commands and has no startup/event hooks or build commands.

## Native agents

Installed version/help and authentication commands were inspected for Codex 0.160.0 and Claude Code 2.1.287. The initial adapters pin those versions. Idle double Ctrl+C is the graceful exit mechanism; process cessation and writer checks are still required afterward. Documentation: [Codex developer commands](https://learn.chatgpt.com/docs/developer-commands?surface=cli), [Claude interactive mode](https://code.claude.com/docs/en/interactive-mode), and [Claude CLI reference](https://code.claude.com/docs/en/cli-reference).

Claude authentication under Herdr is checked through protocol 22
`workspace.create` and `layout.apply` with a command argument array. A one-shot
`hopr auth-probe` process calls the native authentication command in Herdr's launch
context. Private request/result files bind the check to a UUID and exact workspace;
no terminal output is parsed and no native credentials leave the destination.
Cleanup verifies the server incarnation, workspace identity, pane directories,
and idle foreground processes before closing only that diagnostic workspace.
The macOS SSH/Keychain difference is also reported in
[Anthropic issue 76545](https://github.com/anthropics/claude-code/issues/76545).

Resume argument arrays are `codex resume <UUID> --cd <target> --no-daemon` and `claude --resume <UUID>` with destination cwd. No prompt argument or Enter keystroke submits an instruction. Each Mac uses its own `codex login status` / `claude auth status --json` and credentials. Codex shared app-server mode is refused because a stopped terminal process does not prove the conversation writer stopped.

Native JSONL is an internal, version-sensitive format, not a documented general migration API. Codex identity comes from the first `session_meta.payload.id`; Claude uses `sessionId`. Structural cwd fields are remapped; historical message/tool text is retained verbatim as JSON values. Import only allows equal/prefix-compatible history. Native process resume against imported files still requires the real-device verification listed separately. Version pins do not substitute for that validation.

Codex's native per-thread writer coordination was verified against
[`writer_lock.rs` at rust-v0.160.0](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/rollout/src/writer_lock.rs).
Hopr acquires `thread-writer-locks/.coordination.lock` while opening and locking
`<UUID>.lock`, then releases coordination while retaining the UUID lock. It leaves
file cleanup to Codex. This protects imports without excluding unrelated
conversations. The same source file is byte-identical in rust-v0.159.3, rust-v0.159.0,
rust-v0.159.0-alpha.12 and rust-v0.159.0-alpha.12.1. Interoperability was also checked
against the actual lock held by the selected running Codex 0.160.0 source. An
additional `lsof` check rejects writable handles for the exact transcript.

No whole-home copy, credential merge, cross-agent conversion, terminal memory migration, or promise of restoring agent-global state is made. Referenced local assets, Claude sidechains, and unsupported Git features are rejected. Historical text containing old absolute paths remains historical text; tools after resume must use the destination cwd.

## Git, SSH and tmux

Git snapshots use `bundle create … HEAD`, separate binary/full-index staged and unstaged diffs, an index-tree identity, and a checksummed file manifest. `bundle verify`, HEAD/index checks and the reconstructed worktree diff validate restoration. A detached HEAD stays detached; linked worktrees become independent repositories. Only SHA-1 Git repositories are currently supported.

SSH is ordinary OpenSSH with batch mode and connection timeout. No Tailscale API, cloud relay application, Git server, push or pull is involved. The standard SSH configuration selects the route and credentials. Tailscale supplies network connectivity.

tmux 3.7b's installed command help was inspected. The adapter uses structured argv for server/pane queries, keys, a disposable placeholder pane, and native process launch. Its inability to report native idle/session identity is addressed by explicit operator attestation, not screen parsing.
