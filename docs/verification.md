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

The final full suite passed in 25.231 seconds; the final race run passed in 27.953 seconds. `go vet ./...` and the ARM64 build passed. Fixtures use real temporary Git repositories and real native JSONL import/export; agent lifecycle and Herdr readiness are controlled adapters or a fake Unix socket. Covered behaviors include:

- Codex and Claude handoffs, compatible return trips, divergent/destination-ahead history rejection, differing home and project paths.
- Exact HEAD/unpushed history, staged versus unstaged text and binaries, deletion, untracked files, explicit ignored files, modes, internal symlinks, linked worktrees and detached HEAD.
- Invalid checksums, package paths, symlink ancestors, case collisions, dirty destination conflicts and interrupted native imports.
- Faults before/after source preparation, stop, snapshot, retirement, copy, destination restore/import/workspace creation/launch/readiness. Ambiguous effects are not replayed.
- Lost prepare/copy/advance responses, duplicate requests, repeated recovery, exclusive locks, missing native identity, unsupported versions/features, project-policy mismatch, unresolved writers and secret detection.
- Herdr exact-UUID readiness and integration checks, SSH argv/stdin separation, command-adapter JSON, strict config, private setup creation and refusal to overwrite, default Claude credential-path preservation.

These are simulated lifecycle integrations. They do not prove a real native agent will resume an imported conversation or exit reliably under every terminal configuration.

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

Hopr is installed at `/Users/motti/.local/bin/hopr`. `command -v hopr`, `hopr --version`, `hopr --help`, JSON error/exit behavior, and `hopr setup` were exercised against the installed binary. Setup created `/Users/motti/.config/hopr/config.json` with local ID `work-mac` and destination `private-mac` through `workbox`. It contains no project mapping yet because no real project/session was selected. The Herdr keybinding was not installed automatically.

The final installed `hopr doctor --json` returned `ok: true`: Herdr protocol, native versions, Git, SSH, and both native authentication checks passed. Installed-binary tests also verified setup creation, refusal to overwrite, missing-move/config errors, JSON responses and expected exit codes. Its SHA-256 matches the final repository build. The Claude config-path bug discovered during installation was corrected without relocating or overwriting credentials.

The source has Herdr 0.9.0/protocol 22, Codex 0.160.0 and Claude 2.1.287. Earlier read-only checks found Herdr 0.9.1/protocol 22 and Codex 0.159.3 on the Air; Claude and tmux were absent from the inspected standard locations. Recheck before use. The Codex version mismatch currently blocks a real move. Align supported versions, install/configure Hopr on the Air, configure a shared project, and verify each host's own authentication.

A real-device acceptance run still needs an explicitly selected disposable **real** agent session and destination: verify graceful stop, native transcript discovery/import/resume, exact idle readiness, source-pane retirement, power-off independence, appended-conversation return trip, and recovery after disconnect during real launch. Neither the mock integrations nor the SSH fixture constitutes that acceptance. No real user session has been migrated.
