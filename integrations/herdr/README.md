# Herdr right-click integration

This optional **custom Herdr client** adds **Move to another Mac...** to workspace
rows in the machines sidebar and to pane context menus. Hopr remains independent
of Herdr; stock Herdr can use the same popup through its keyboard shortcut.

For the standard action and popup without maintaining a custom client, use the
[Hopr Herdr plugin](../../plugins/herdr/README.md). The patch below expects the
simple `type = "popup"` binding; a `plugin_action` binding does not enable its menu.

Flow: select **Local**, right-click the project/session row, choose **Move to
another Mac...**, then choose **Home** in the Hopr popup. It checks the destination,
stops the exact idle agent, transfers its conversation and Git state over SSH, and
resumes in a new workspace on the destination. The new workspace appears under
Home when that Herdr machine is connected to the destination server configured in
Hopr. This is a handoff, not dragging a sidebar row or moving a running process.

## Why a custom client

Verified against official Herdr **v0.9.0**, commit
`b99002ac99b09e00b4ca692436cb15a6b0d676f1`, and installed protocol-22 API schemas.
[Custom popups](https://herdr.dev/docs/configuration/#custom-command-keybindings)
are supported. Plugin `contexts` do not add right-click entries in this release's
[fixed menu](https://github.com/herdrdev/herdr/blob/v0.9.0/src/client/shell/context_menu.rs).

The patch only changes client presentation and tests. It uses existing
`command.invoke` with a server-provided opaque command ID and explicit clicked
workspace/pane IDs. Herdr validates their relationship and sets popup context on
the owning server. There are no new wire fields, shell interpolation of pane IDs,
server replacements, or changes to Herdr's same-host live handoff.

## Build

Requires Git, Rust (rustup installs the pinned 1.96.1 toolchain), Zig **0.15.2**,
`just`, and `cargo-nextest`. Build from the Hopr repository:

```sh
sh integrations/herdr/build.sh
```

The script checks out the exact upstream commit in a fresh temporary directory,
applies the patch, checks formatting, runs the five focused regression tests, and
builds `bin/herdr-hopr` in release mode. It retains the temporary source directory.
It does not install a binary or edit configuration. Full upstream validation is
`just check` from that directory (also requires Bun and the Windows Rust target).

Exercise the actual right-click popup against a disposable **stock** server:

```sh
python3 integrations/herdr/smoke.py bin/herdr-hopr ~/.local/bin/herdr
```

This opens a temporary client/server with isolated sockets and config, clicks the
pane menu, invokes a harmless capture script, and asserts the popup's exact pane,
workspace and server context. It stops only its temporary server. No native agent
or SSH transfer runs. Fixture files are retained at the printed path.

On macOS, Zig 0.15.2 cannot link against some SDKs from Xcode/Command Line Tools
26.4 or newer; this is a [known upstream issue](https://github.com/ghostty-org/ghostty/issues/11991).
If an older compatible SDK is already installed, select it for this build only:

```sh
HOPR_MACOS_SDK=/Library/Developer/CommandLineTools/SDKs/MacOSX15.4.sdk \
  sh integrations/herdr/build.sh
```

The script does not change `xcode-select` or system SDK files. Set `ZIG` to an
absolute executable path if the required Zig is outside PATH.

## Enable

1. Install Hopr at `~/.local/bin/hopr` and configure both Macs.
2. Add [the popup binding](../../examples/herdr.toml) to the **source server's**
   `~/.config/herdr/config.toml`. Add it on both Macs for return trips. Keep its
   description exactly `Move agent to another Mac`, with exactly one popup using
   that description. It is the integration marker in the existing command manifest.
3. Reload that server's config with `herdr server reload-config`. Use the explicit
   session/socket for a non-default server. Config reload does not stop agents.
4. In a separate terminal on the Mac displaying the UI, run the custom client:

   ```sh
   /absolute/path/to/hopr/bin/herdr-hopr
   ```

   It attaches to the existing default server. There is no need to stop/restart
   the server. Do not run `server stop`, `update --handoff`, or replace the installed
   `herdr` binary to enable this menu. For a named server use `--session NAME`.
   Run the release build above; debug builds use Herdr's separate `herdr-dev` paths.
5. Select the source machine, right-click its workspace row, choose the move
   action, then select the destination number. The existing shortcut also works.

For the label shown in the screenshot, give the destination a friendly name in
Hopr's `config.json`:

```json
"hosts": {
  "private-mac": {"ssh": "workbox", "id": "private-mac", "label": "Home"}
}
```

`workbox` must be your existing SSH alias for the private Mac. `label` affects
display only; the configured host ID still verifies receiver identity. Herdr's
machine list and Hopr's destination list are separate: this patch does not infer
SSH destinations from sidebar text or silently add trusted hosts. Home's Herdr
connection must point to the same server as the receiver's `default_server`.

## Selection and failures

- Workspace action requires exactly one detected agent. With multiple agents,
  right-click the specific pane. Hopr still rejects concurrent workspace writers.
- Source machine and server boot are captured when opening the menu. A machine
  switch, restarted server, removed pane, unavailable API, or disconnected endpoint
  cannot redirect the request to another pane with the same ID.
- Missing/ambiguous popup configuration gives a visible error. A generic shell
  binding with the same description is never executed.
- Hopr reads `HERDR_ACTIVE_PANE_ID` and verifies `HERDR_SOCKET_PATH` against its
  configured servers. No latest-session heuristic is used.
- Destination selection starts the move. Empty input cancels. Results remain
  visible until Enter. If interrupted during a move, inspect/recover its UUID;
  never automatically restart the source.
- Original stock Herdr windows keep their original menus. Only the custom client
  has this action. Select a machine before right-clicking its rows; upstream only
  exposes workspace context menus for the active machine.
- The destination stays named `hopr-<move-uuid>` for recovery. The client does not
  automatically switch the view to Home. Click Home to view the resumed session.

## Remove or update

Close/detach the custom **client**, leaving the server and panes running, and open
normal `herdr`. Delete `bin/herdr-hopr` (and the retained temporary build directory)
when no longer needed. Remove the optional popup binding and reload config if you
also want to remove the shortcut. Hopr snapshots and checkouts are not deleted.

The patch is pinned; do not force-apply it to newer Herdr versions. Revalidate the
command manifest, endpoint routing and tests when rebasing it. No upstream PR or
official Herdr release is implied. Herdr-derived patch context and distributed
Herdr code are covered by [Herdr's Apache-2.0 license](LICENSE-herdr).
