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

The full Go suite passed in 50.991 seconds; the race detector passed in 87.763 seconds after the concurrency and readiness fixes. `go vet ./...` and the ARM64 build passed. Fixtures use real temporary Git repositories and real native JSONL import/export; agent lifecycle and Herdr readiness are controlled adapters or a fake Unix socket. Covered behaviors include:

- Codex and Claude handoffs, compatible return trips, divergent/destination-ahead history rejection, differing home and project paths.
- Exact HEAD/unpushed history, staged versus unstaged text and binaries, deletion, untracked files, explicit ignored files, modes, internal symlinks, linked worktrees and detached HEAD.
- Invalid checksums, package paths, symlink ancestors, case collisions, dirty destination conflicts and interrupted native imports.
- Faults before/after source preparation, stop, snapshot, retirement, copy, destination restore/import/workspace creation/launch/readiness. Ambiguous effects are not replayed.
- Lost prepare/copy/advance responses, duplicate requests, repeated recovery, exclusive locks, missing native identity, unsupported versions/features, project-policy mismatch, unresolved writers and secret detection.
- Herdr exact-UUID readiness and integration checks, SSH argv/stdin separation, command-adapter JSON, strict config, private setup creation and refusal to overwrite, default Claude credential-path preservation.
- Standard Herdr plugin selection preservation across focus changes, missing context and changed server/socket/pane/workspace/terminal/session rejection.
- Real disposable process trees: recognized helper shutdown, orphaned helpers and process groups, unknown children, PID identity changes, and writable project handles.
- Scoped native writer locks: unrelated Codex processes stay running, the same UUID blocks, a writer arriving after preflight blocks import, and namespace coordination is honored.
- Readiness before the first prompt: exact resume argv/cwd and a held native UUID lock; wrong/unlocked identities fail. Herdr names respect the length limit without truncating UUID identity.
- Both journals retain destination ownership after recovery from a crash following launch.

These are simulated lifecycle integrations. They do not prove a real native agent will resume an imported conversation or exit reliably under every terminal configuration.

## Standard Herdr plugin

`python3 plugins/herdr/smoke.py ~/.local/bin/herdr bin/hopr` passed against stock
Herdr 0.9.0, using an isolated server, client PTY, plugin registry and Hopr config.
It sent ordinary Ctrl+B then m bytes, invoked the real plugin action, verified
the original pane/server/terminal context, and opened the real Hopr menu. The
menu correctly rejected the fixture shell's missing agent. The retained fixture
is `/private/tmp/hopr-popup-nlld1vd1`. No real agent was stopped and no SSH transfer
ran. This verifies terminal dispatch, not physical keyboard routing through the
user's macOS global shortcuts.

The plugin does not add native right-click entries. The optional custom-client
verification below concerns a separate integration.

The standard plugin is linked locally, with `prefix+m` → `hopr.move` as the
active binding. Config reload returned `applied` with no diagnostics. The
concurrency fix installs the same ARM64 Hopr build on both Macs, retaining binary
backups. No Herdr server is replaced or restarted.

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

The source has Herdr 0.9.0/protocol 22, Codex 0.160.0 and Claude 2.1.287.
The Air has Codex 0.160.0 with working login and Herdr 0.9.1/protocol 22.
Its co-resident desktop Codex 0.159.0-alpha.12.1 and shared daemon 0.160.0 use the
verified native writer-lock protocol and were left running. Claude 2.1.287 was subsequently installed privately on the Air for the selected
Claude test below; its existing 2.1.286 installation was retained.

## Selected native Codex handoff

Move: `36336847-8295-4691-88e5-6d8b7149d5a9`; native conversation:
`01a0fd39-1fee-7ab3-acb6-72ea5f53fbc3`.

- Source: work Mac, `/Users/motti/hopr-test`, Herdr `w1D:p1`.
- Destination: private Mac through `workbox`, isolated checkout
  `/Users/mottibechhofer/HoprWorkspaces/hopr-test/36336847-8295-4691-88e5-6d8b7149d5a9`.
- The selected Codex PID and its Node REPL / Computer History children exited
  gracefully. Source-pane retirement, SSH transfer, package verification, Git
  restoration and native import ran with the other Codex sessions still open.
- Independent Git comparisons matched HEAD and ancestor history, index entries,
  separate binary-capable staged/unstaged diffs, file bytes, modes, symlinks and
  untracked state. HEAD: `da6e8dcbc6decb8475e14631ba877e01941504c3`.
- The first launch was rejected because the generated Herdr name was too long.
  After confirming the structured rejection occurred before execution and the
  target remained an empty shell, its journal was backed up and reconciled to
  reuse the existing checkout/import/pane. A regression test covers the fix.
- Codex resumed the original conversation. The user accepted its folder-trust
  prompt, continued the conversation, then closed that workspace before Hopr
  completed verification. The destination history extension was backed up and
  retained when recreating the deleted workspace for this same move; the old
  import was not replayed.
- Missing pre-turn identity was traced to Codex's deferred SessionStart hook and
  Herdr's two-argument resume parser. The fallback now verifies the held native
  UUID lock, exact argv/cwd and managed interactive readiness, without submitting
  an instruction or writing a synthetic integration report.

These interventions mean this run is **not evidence of an uninterrupted,
automatic end-to-end handoff**. Both journals now record `complete`, owner `private-mac`, pane `w7:p1`, terminal
`term_65cdf562188f67`. Two subsequent recoveries completed without another launch;
the destination remained PID 92591. The existing destination Codex processes
30770, 61555, 61687 and 83016 remained running. The resumed native transcript
retains the backed-up destination history extension.

The same installed ARM64 binary on both Macs has SHA-256
`b60841f01c84769f2ba0c4c023ecb6919a80c024926172151dfaea41e1515e1e`.
The installed local doctor, stock-plugin PTY smoke, vet, full suite and race
checks passed. Real Claude, source-power-off independence, a full
return trip with appended history, and disconnect injection around a real
launch remain to be tested. Automated tests cover those state transitions with
controlled agent lifecycle adapters.

## Selected Claude test (pending authentication)

The user selected a disposable Claude test in `/Users/motti/hopr-test`, keeping
the other source Claude/Codex sessions open. Herdr pane `w1H:p1`, native UUID
`fcfe3796-fbf1-4ec0-9133-1eee1c0356d7`, completed the initial fixture prompt and
passed the real source identity/process/writer guard. Git state was recorded for
an independent destination comparison.

The verified ARM64 Claude 2.1.287 executable was copied into
`~/.local/share/hopr/tools/claude-2.1.287/claude` on Home (SHA-256
`6eab8333fe2121553100d8f40bfada384a3e989b94f947e18ba6677a6fcb41ea`). Only Hopr's
executable setting was updated, with a config backup. No credentials or agent
configuration were copied from the source.

Move `0227b615-e7a6-4eeb-b9e9-c5983ef31a0a` stopped at destination preparation:
Claude's `auth status --json` over SSH reported `loggedIn: false` after the user
reported completing login. Both ordinary and TTY SSH checks gave that result.
A Claude Keychain item exists, but a credential-access probe with its output
discarded failed. Switching the probe to the GUI audit session with
`launchctl asuser` was denied by macOS; that route was not pursued further.
Local GUI authentication status is awaiting user verification.

The source is still running. Its journal is `prepared` / `prepare_remote`; no
Claude source stop, export package, target checkout, import or launch occurred.
Use **this same move ID** with `hopr recover` once destination authentication is
available to the receiver. This is not a completed Claude acceptance test.

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
routing are covered by Rust tests. The selected native handoff is recorded separately above.

Installed locally: `~/.local/bin/herdr-hopr` beside the unchanged stock Herdr,
plus the updated `~/.local/bin/hopr`. Binary hashes match `bin/` builds. The original
Herdr config backup is
`~/.config/herdr/config.toml.before-hopr-rightclick-1790956301004185000`.
The new menu appears when opening the custom client; existing stock-client windows
keep their original menus. Latest installed `hopr doctor --json` passed.
