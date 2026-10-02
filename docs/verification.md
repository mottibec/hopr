# Verification record

Date: 2026-10-02. Host: macOS ARM64. Go 1.27.1 was downloaded into a disposable build-tools directory because Go was initially absent from PATH; no system Go installation was required. The delivered binary is macOS ARM64 with no third-party Go dependencies.

## Automated checks

Run from the repository:

```sh
make test
make race
go vet ./...
make build
```

The latest full Go suite passed in 27.495 seconds; the latest race run passed in 30.112 seconds. `go vet ./...` and the ARM64 build passed. Fixtures use real temporary Git repositories and real native JSONL import/export; agent lifecycle and Herdr readiness are controlled adapters or a fake Unix socket. Covered behaviors include:

- Codex and Claude handoffs, compatible return trips, divergent/destination-ahead history rejection, differing home and project paths.
- Exact HEAD/unpushed history, staged versus unstaged text and binaries, deletion, untracked files, explicit ignored files, modes, internal symlinks, linked worktrees and detached HEAD.
- Invalid checksums, package paths, symlink ancestors, case collisions, dirty destination conflicts and interrupted native imports.
- Faults before/after source preparation, stop, snapshot, retirement, copy, destination restore/import/workspace creation/launch/readiness. Ambiguous effects are not replayed.
- Lost prepare/copy/advance responses, duplicate requests, repeated recovery, exclusive locks, missing native identity, unsupported versions/features, project-policy mismatch, unresolved writers and secret detection.
- Herdr exact-UUID readiness and integration checks, SSH argv/stdin separation, command-adapter JSON, strict config, private setup creation and refusal to overwrite, default Claude credential-path preservation.
- Standard Herdr plugin selection preservation across focus changes, missing context and changed server/socket/pane/workspace/terminal/session rejection.

These are simulated lifecycle integrations. They do not prove a real native agent will resume an imported conversation or exit reliably under every terminal configuration.

## Standard Herdr plugin

`python3 plugins/herdr/smoke.py ~/.local/bin/herdr bin/hopr` passed against stock
Herdr 0.9.0, using an isolated server, client PTY, plugin registry and Hopr config.
It sent ordinary Ctrl+B then m bytes, invoked the real plugin action, verified
the original pane/server/terminal context, and opened the real Hopr menu. The
menu correctly rejected the fixture shell's missing agent. The retained fixture
is `/private/tmp/hopr-popup-cuk7l7ar`. No real agent was stopped and no SSH transfer
ran. This verifies terminal dispatch, not physical keyboard routing through the
user's macOS global shortcuts.

The plugin does not add native right-click entries. The optional custom-client
verification below concerns a separate integration.

The updated Hopr binary and locally linked plugin are installed on the work Mac.
The only active Hopr binding is `prefix+m` → `hopr.move`; `server reload-config`
returned `applied` with no diagnostics and the installed `doctor --json` passed.
The installed binary matches `bin/hopr` (SHA-256
`bb89687cbaa91002fcc57e5836fb6f442e65ca3138856f823a9033ad6395ddc4`).
The previous binary and Herdr configuration were backed up with suffix
`before-plugin-1790958441858852000` and
`before-hopr-plugin-1790958441858852000`, respectively. The Air's binary was not
updated in this plugin pass. No Herdr server was restarted or real session moved.

## Real SSH / Tailscale fixture

The user selected `mottis-macbook-air-2.tail91305c.ts.net`. Its existing `workbox` SSH alias was verified to reach that Mac as its configured user with known-host verification. No global receiver or agent settings were installed there.

To reproduce only after selecting a disposable destination:

```sh
go test -c -o /private/tmp/hopr-live-tests ./internal/handoff
HOPR_SMOKE_HOST=workbox /private/tmp/hopr-live-tests \
  -test.run '^TestRealSSHFixture$' -test.v
```

The latest transfer run passed in 33.87 seconds. It created only `/private/tmp/hopr-smoke-OsZZba5W` on the Air, containing a test binary and private Codex/Claude fixtures. The directory is retained for inspection. Move IDs:

- Codex fixture: `922b0695-e8f5-4216-b265-7cedfd628284`.
- Claude fixture: `1d390360-d4cf-4403-b171-c2dd43f28da4`.

The transport, receiving process, checksum validation, native import, Git restoration, journals and recovery ran over actual SSH between the Macs. Independent destination Git commands matched source HEAD, index tree and status. A deliberately lost response after destination processing recovered without duplicate effects. **Agent stop, launch and ready events were simulated** in the test-only receiver. No real conversation was stopped or resumed.

Earlier fixture directories may remain in `/private/tmp/hopr-smoke-*`; inspect and remove only the specific disposable run you intend to clean up. Normal test commands do not contact any remote host unless `HOPR_SMOKE_HOST` is explicitly set.

## Real tmux fixture

```sh
HOPR_TMUX_SMOKE=1 go test ./internal/handoff -run '^TestRealTmuxFixture$' -v
```

Passed on installed tmux 3.7b using a unique disposable server, which the test shut down afterward. This validates real pane creation, lookup, argument-array launch and refusal to claim readiness without attestation. The native agent was a controlled executable, not Codex or Claude.

## Local installation and remaining device checks

Hopr is installed at `/Users/motti/.local/bin/hopr`. `command -v hopr`, `hopr --version`, `hopr --help`, JSON error/exit behavior, and `hopr setup` were exercised against the installed binary. Setup created `/Users/motti/.config/hopr/config.json` with local ID `work-mac` and destination `private-mac` through `workbox`. Both Macs now have a disposable `hopr-test` project mapping. The private destination uses the friendly label `Home`. On the user-requested right-click integration pass, the local Herdr popup binding was added after announcing the change and backing up config; `server reload-config` returned `applied` with no diagnostics. No Herdr server was restarted.

The final installed `hopr doctor --json` returned `ok: true`: Herdr protocol, native versions, Git, SSH, and both native authentication checks passed. Installed-binary tests also verified setup creation, refusal to overwrite, missing-move/config errors, JSON responses and expected exit codes. Its SHA-256 matches the final repository build. The Claude config-path bug discovered during installation was corrected without relocating or overwriting credentials.

The source has Herdr 0.9.0/protocol 22, Codex 0.160.0 and Claude 2.1.287. After the user updated Codex, the Air was verified at 0.160.0 with working login and Herdr 0.9.1/protocol 22. Claude was found under the Air's nvm installation at 2.1.286 with an unsuccessful auth check. Recheck versions, local credentials and process ownership before a real move. The latest popup/readiness build was installed locally; this pass did not update the Air's binary.

The user selected the disposable source Codex session `01a0fd39-1fee-7ab3-acb6-72ea5f53fbc3` in pane `w1D:p1` for the private Mac. The first move attempt stopped during idle-readiness preflight: no source process stopped, no journal was created, and no transfer occurred. That attempt exposed a source adapter bug: manually launched agents lack Herdr's managed `interactive_ready` flag. The corrected adapter accepts independently validated idle/done source agents while retaining managed readiness checks on destination launches. Regression tests cover the omitted field. Subsequent read-only inspection found source helper children and existing destination Codex processes; those conservative writer guards have not been bypassed.

The selected real-device acceptance run still needs to verify: verify graceful stop, native transcript discovery/import/resume, exact idle readiness, source-pane retirement, power-off independence, appended-conversation return trip, and recovery after disconnect during real launch. Neither the mock integrations nor the SSH fixture constitutes that acceptance. No real user session has been migrated.


## Right-click integration verification

The patch is pinned to Herdr v0.9.0 at
`b99002ac99b09e00b4ca692436cb15a6b0d676f1`. Rust 1.96.1 and Zig 0.15.2 were
installed in build tooling; Zig needed the already-installed macOS 15.4 SDK for
its build runner. System SDK selection was not changed.

- `cargo fmt --check` and macOS `cargo clippy --all-targets --locked -- -D warnings` passed.
- `just test-one hopr`: all five new client regression tests passed.
- Full nextest run: **3,106 passed, 2 failed, 2 skipped**. Both failures reproduced
  on a separate untouched checkout of the exact upstream tag:
  `live_handoff_preserves_pane_process_io` (`Invalid argument`, OS error 22), and
  `live_handoff_keeps_unmanaged_agent_name_bound_to_saved_session` (agent detection
  timeout). Thus `just check` is **not green** on this machine; these failures also
  occur without the Hopr patch. No test was disabled or expectation weakened.
- `just windows-lint` passed. Maintenance, UI architecture, integration assets,
  plugin marketplace and docs contract recipes all passed when run separately.
- The ARM64 release binary built successfully. The patch applies cleanly to the
  pinned untouched source tree.
- `python3 integrations/herdr/smoke.py bin/herdr-hopr ~/.local/bin/herdr` passed
  against a real disposable **stock Herdr 0.9.0 server** and patched client. Actual
  terminal mouse input opened the pane's right-click action and launched the
  configured capture popup. It received the exact `w1:p1` / `w1` identities and
  the isolated server socket. Fixture retained at `/private/tmp/hopr-popup-uist7kja`.
  A patched-server run also passed at `/private/tmp/hopr-popup-r0jlm9or`.

The smoke uses a harmless popup script. It does **not** stop/import/resume a native
agent or transfer a project. Workspace-row selection and Local/Home duplicate-ID
routing are covered by Rust tests. Real two-Mac native acceptance remains pending.

Installed locally: `~/.local/bin/herdr-hopr` beside the unchanged stock Herdr,
plus the updated `~/.local/bin/hopr`. Binary hashes match `bin/` builds. The original
Herdr config backup is
`~/.config/herdr/config.toml.before-hopr-rightclick-1790956301004185000`.
The new menu appears when opening the custom client; existing stock-client windows
keep their original menus. Latest installed `hopr doctor --json` passed.
