# Recovery and ownership

Every move has one UUID on both hosts. Keep it even if `move` exits with an error. Run these on the source:

```sh
hopr status <move-uuid> --json
hopr recover <move-uuid> --json
```

`status` is read-only. It shows the local journal and queries the destination. If SSH fails, destination ownership/execution is unknown. `recover` continues the same move and may perform its remaining authorized steps. It never automatically restarts the source. Run it again after fixing a reported dependency or resolving the existing destination UI.

| Journal state | Meaning and recovery |
| --- | --- |
| `prepared` | Exact source is recorded. Destination preflight/reservation must succeed before stopping. A `stop` intent with a still-running PID requires manually exiting that exact source; the exit keystrokes are not resent. |
| `source_stopped` | Process exit was verified. Snapshot, source-pane retirement, or copy may remain. The package is retained; source checkout is not removed. |
| `copied` | Destination accepted a checksum-verified package. Restore uses a private temporary checkout and atomic rename. An existing destination must exactly match the snapshot, otherwise recovery refuses it. |
| `restored` | Git state exists. Import uses stored preimage/desired hashes and backups. Workspace creation and native launch may remain. |
| `target_ready` | Exact destination conversation was confirmed idle and interactive. Completion journaling may remain. |
| `complete` | Destination owns the managed session. Source may be powered off after the source also records completion. |

`intent` records an action before its side effect. In particular, `create_workspace` is reconciled by the durable `hopr-<UUID>` label, and `launch` is reconciled by exact native readiness. **Hopr never sends another launch after launch intent exists**, even if a crash happened just before the first launch. A lost SSH response after a successful launch therefore cannot create a second agent.

If the destination shows a trust/authentication dialog, resolve it there without submitting a new instruction and recover. For tmux, also attest the actual resumed UUID at its idle prompt. If no matching workspace/process exists after uncertain creation/launch, this release stops for manual reconciliation; there is no force/replay/reset flag. Keep both journals and contact the adapter maintainer or inspect the recorded identities. Do not guess by launching the source.

For a return trip, move the destination's current pane as a **new** move UUID. Its history must extend the existing conversation on the original Mac. Equal history is allowed; divergent history or a destination ahead of the incoming conversation is rejected without destructive overwrite. Git changes return in another isolated checkout, leaving the original checkout intact.

## Durable files

Under `state_dir`:

- `moves/<UUID>/journal.json`: state, intent, exact identities, paths, package checksum.
- `moves/<UUID>/package.json`: complete selected workspace/conversation package on both hosts after copy.
- `moves/<UUID>/import-plan.json` and `native-backups/`: destination import preimages and backup bytes.
- `ownership/<agent>-<native-UUID>.json`: local coordination record (`in_transit` or host ID).
- `locks/`: kernel advisory locks, released when a process exits. Do not delete them to bypass another active helper.

Files use temporary-write/fsync/rename and parent-directory fsync; host operations are serialized. Packages are private, but not encrypted at rest. SSH encrypts transit. Preserve packages/backups as carefully as project source and conversations. Local disk corruption or deletion of journals is outside automatic recovery.

There is deliberately no automatic rollback or abort that resumes the source. To abandon a blocked move, first establish that no destination execution occurred, preserve both snapshots, and reconcile ownership manually. If destination execution is unknown, keep the source stopped until connectivity and identity checks resolve it. Manually launching agents, editing journals, or writing files outside hopr can bypass this coordination. A human choosing that route is responsible for preventing concurrent writers and divergent histories.

Source-pane retirement removes its saved automatic resume association before copying; it never stops the whole terminal server. A server restart during uncertain retirement changes the server incarnation and can require manual reconciliation. No claim is made that independent manual/server launches outside the helper are globally fenced.
