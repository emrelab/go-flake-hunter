# Interactive terminal workspace

Build the checkout with `make build`, then run `./go-flake-hunter tui ./...`
from a Go workspace. The optional target list and `--budget`, `--timeout`,
`--jobs`, `--short`, `--race`, `--capsule-dir` flags set the starting configuration.
Opening the workspace discovers packages and stored capsules without running tests.

The interface uses the same hunt, doctor, replay and compile commands as the
scriptable CLI. Their validation, locking, witness cleanup and capsule formats
remain the source of truth. JSON, Markdown and SARIF remain available through
the ordinary commands.

| View | Workflow |
|---|---|
| Overview | Start a scan, browse findings, replay, preview, check environment |
| Findings | Filter with `/`, move with arrows or j/k, Enter for details, r to replay, g to preview |
| Activity | Follow package phases, elapsed time and run budget; scroll logs or source; arrows pan long lines |
| Settings | Edit targets, budget, timeout, workers, capsule directory, short/race; reload catalog |

Tab / Shift+Tab and 1–4 switch views. Escape closes an editor, detail or prompt.
`?` opens scrollable help. Settings cannot change during a task. The target
editor accepts one path or pattern, including spaces; supply multiple targets
at launch. Settings are session-only. `NO_COLOR` disables color styling.

## Preview and write

A confirmed same-process, same-clause witness can become a regression test.
`g` runs `compile --print` and opens its source without writing. Press `w`,
review the confirmation, then type `y` to write through the existing compiler.
Changed or missing capsules require another preview. The rendered source must
also match the approved preview byte for byte before any write, including when
the package clause or checkout changes. Compiler safeguards still
protect user-owned files. Generation does not imply verification: use the
generated command or `compile --verify` to check the test against your checkout.

Environment replay prompts for the recorded value in a masked editor. It does
not save that value in settings or logs; target output is discarded for that
replay because a test might print secrets. As with CLI `--env`, another process
with debugging access to the running program may inspect its memory or child
environment. Replay runs three rounds and distinguishes reproduced, changed
and broken targets.

## Cancellation and terminal behavior

Ctrl+C cancels an active task and waits for engine cleanup. Partial evidence
remains available in Findings. `q` during a task asks whether to stop and exit;
if the task finishes before you answer, the workspace exits as requested. An
idle `q` exits immediately. Outside a task, Ctrl+C cancels an editor or asks
for a second press to exit. Normal exits, context cancellation and reported
errors restore terminal modes, cursor and the previous screen. Go commands use
an isolated process group on Linux/macOS; cancellation terminates the group.
Windows starts each Go command suspended, assigns it to a Job Object, then
resumes it. Cancellation terminates the job; closing its last non-inherited
handle also kills the entire tree if explicit termination fails. Setup failures
stop the suspended process instead of running without tree ownership.
Inherited output pipes have a bounded wait so they cannot indefinitely freeze the interface.

The native terminal package supports Linux/macOS termios and Windows Console
APIs without Go dependencies. Other systems return an unsupported message.
Redirected streams are rejected by `tui`. Bare invocation opens the TUI only
with real terminals, outside CI and TERM=dumb. The layout requires 60×20 and
caps the viewport at 240×100; smaller terminals show a resize prompt. Log history
retains 200 lines of up to 4096 bytes; report capture is bounded at 8 MiB and
serialized capsules on both reads and writes at 16 MiB (including the final
newline). Unicode input is supported; display clipping is conservative for characters whose terminal cell width varies.

Bracketed paste is isolated from shortcuts and confirmation prompts on terminals
that report it. The native Windows console delivers pasted characters as key
events, so it cannot provide that distinction; pasted text there behaves like
typing. Use a supported Unix terminal when clipboard isolation is required.

## Validation

`make tui-smoke` uses a real Unix PTY and only Python's standard library. It
checks startup, resizing, paste isolation, scan → finding → replay → preview →
confirmed write, cancellation, NO_COLOR and terminal restoration. CI runs it
on Linux. Windows CI allocates a real console in a separate helper and injects
native key records to check Unicode, repeated keys, navigation, Ctrl+C, size and
mode restoration. Process tests synchronize descendant readiness over sockets;
Windows also exercises failed job termination and repeated handle cleanup.
Go unit tests cover fragmented terminal input, Unicode editing,
secret-output suppression, stale preview rejection and bounded buffers.

To render real terminal captures, install Pillow in a temporary environment
and run `scripts/tui-smoke.py --binary ./go-flake-hunter --preview-dir /tmp/tui`.
Pillow is optional and is not needed to build or run the application.
