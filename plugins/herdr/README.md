# Hopr plugin for stock Herdr

This package adds the `hopr.move` action and a destination-picker popup to stock
Herdr 0.9.0 or later with protocol 22. It uses the installed Hopr binary and its
existing configuration. There is no custom Herdr build or background service.
Native agent and Git support retain [Hopr's limits](../../README.md#limits-and-safety-boundaries).

## Install

First [install and configure Hopr](../../README.md#build-and-install) on both Macs.
The plugin launcher expects `~/.local/bin/hopr`; a different location can be set
with `HOPR_BIN` in the Herdr server environment. The binary must include the
`herdr-popup` entrypoint delivered with this plugin.

From this repository checkout:

```sh
herdr plugin link "$PWD/plugins/herdr"
herdr plugin action list --plugin hopr
```

Alternatively, install the current feature branch from GitHub:

```sh
herdr plugin install mottibec/hopr/plugins/herdr --ref motti/hopr
```

For a reproducible installation, replace the branch with a reviewed commit SHA.
Use either `link` or `install`; Herdr refuses to install over a linked plugin.
The plugin does not build or update the Hopr binary or edit any keybindings.

Add this to `~/.config/herdr/config.toml`, replacing any earlier Hopr binding:

```toml
[[keys.command]]
key = "prefix+m"
type = "plugin_action"
command = "hopr.move"
description = "Move agent to another Mac"
```

Then run `herdr server reload-config` against the intended local server. With the
default prefix, select the agent pane, press **Ctrl+B**, release both keys, then
press plain **m** (no Shift, Option or Control). Choose the destination, such as
Home. Opening the popup does not stop the agent; selecting a destination starts
preflight and the handoff. An empty answer cancels. Results stay open until Enter.

The source machine needs the plugin when starting a move. Install it on both Macs
to use the same shortcut for return trips. Each Mac still uses its own credentials.

## Selection and safety

The action captures Herdr's invocation pane and workspace, looks up that pane's
terminal and native session reference, and binds them to the configured socket
incarnation. It passes this selection as JSON through `plugin.pane.open`, then
validates it when the menu opens and after the destination is selected. Moving
focus while the action starts cannot select a different conversation. Missing or
changed identities are errors; no latest-conversation fallback exists.

The inherited `HERDR_SOCKET_PATH` must match a configured Hopr server. Run the
plugin on the machine owning the pane. Selecting a remote machine in the UI does
not redirect Hopr to a different host. The internal `herdr-popup` and
`menu --herdr-plugin` entrypoints require the plugin's captured context; ordinary
CLI users should use `hopr move` or the [simple popup](../../examples/herdr.toml).

Stock Herdr's plugin API does not add right-click menu entries. This plugin opens
through its action/shortcut. The separate [custom client patch](../../integrations/herdr/README.md)
is optional and requires its own maintenance; it is unnecessary for this plugin.

## Test and uninstall

```sh
go test ./...
go test -race ./...
go build -o bin/hopr ./cmd/hopr
python3 plugins/herdr/smoke.py /path/to/stock/herdr bin/hopr
```

The smoke test starts an isolated stock Herdr server/client, links this package in
temporary configuration, and sends Ctrl+B then m through a real PTY. It verifies
the captured pane and opens the actual Hopr menu on a harmless shell. Hopr must
refuse that shell because it has no native agent identity. No real agent or SSH
transfer is involved. The temporary directory is printed and retained; only its
server is stopped.

Remove the Hopr keybinding and reload configuration. Use `herdr plugin unlink hopr`
for a linked checkout or `herdr plugin uninstall hopr` for a GitHub installation.
Keep Hopr journals and checkouts until pending moves are resolved. See the main
[uninstall instructions](../../README.md#exit-codes-and-uninstall) for the binary.

Interface reference: [Herdr v0.9.0 plugins](https://github.com/herdrdev/herdr/blob/v0.9.0/docs/next/website/src/content/docs/plugins.mdx).
