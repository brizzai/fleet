# Linux support

fleet is a pure-Go TUI; most of the codebase was already portable. This
document covers the parts that are genuinely platform-specific and how the
Linux implementations differ from macOS.

## Process discovery: `internal/proc`

The one hard platform dependency. fleet clears leftover dev-stack daemons
(process-compose, air, vite/node, …) off a git worktree before removing it,
and shows each drawer shell's foreground command.

| | macOS (`proc_darwin.go`) | Linux (`proc_linux.go`) |
|---|---|---|
| Holder discovery | one system-wide `lsof -Fpcn` | native `/proc/<pid>/{cwd,exe,fd/*}` readlinks (no `maps` scan — see below) |
| Foreground command | one `ps -axo pid=,ppid=,tpgid=,command=` | `/proc/<pid>/stat` (tpgid), leader's `cmdline` |
| Subprocesses spawned | lsof, ps | none |

Shared in `proc.go`: the exported API (`FindHolders`, `ForegroundCommands`,
`Kill`, `Alive`), the never-kill policy, and the pure lsof/ps parsers (kept
untagged so their tests run on Linux CI).

Implementation notes:

- `/proc/<pid>/stat` is parsed after the **last** `)` — comm is parenthesized
  and may itself contain spaces/parens. Post-paren field 6 is `tpgid`.
- Pids owned by other users fail readlink with EPERM and are skipped — the
  same blindness non-root lsof has on macOS.
- Login shells announce themselves as `-bash`/`-zsh` in argv[0]; the
  never-kill matcher strips the dash (macOS never hit this because lsof
  reports comm). Regression-tested.
- Semantics: on Linux an open fd never blocks rmdir; the walk exists to kill
  daemons that would otherwise keep recreating files mid-removal.
- No `/proc/<pid>/maps` scan: it would cost a full file read for every
  same-user process on the box per holder scan, and its only unique catch — a
  worktree file mmap'd and then closed — isn't how dev daemons hold a dir. A
  daemon exec'd from a since-deleted worktree binary is still caught via the
  `exe` readlink, which reports `path (deleted)`.

## Clipboard

`tmux.EnsureCopyCommand` picks the copy-mode pipe per platform: `pbcopy` on
macOS; on Linux the first *usable* tool — `wl-copy` when `WAYLAND_DISPLAY`
is set, else `xclip`/`xsel` when `DISPLAY` is set. A tool on PATH whose
display server isn't reachable (headless/SSH, or wl-clipboard installed as
a dependency on an X11 desktop) is skipped: piping into it delivers
nothing. With no usable tool, fleet leaves tmux alone — the default
`set-clipboard external` already forwards copy-mode selections via OSC 52,
which most modern Linux terminals accept, and a deliberate
`set-clipboard off` is never overridden.
`FLEET_NO_COPY_COMMAND=1` opts out entirely.

Note that `copy-command` and OSC 52 are independent, not a fallback chain:
`copy-pipe-and-cancel` passes `set_clip=1`, so tmux emits the OSC 52
selection *as well as* running `copy-command`. A `copy-command` that fails
therefore doesn't lose the copy unless `set-clipboard off` is also set,
which suppresses OSC 52 on its own.

Display vars (`WAYLAND_DISPLAY`/`DISPLAY`) are read from fleet's own
process environment: tmux runs `copy-command` jobs with the *session*
environment, which `update-environment` refreshes from the most recently
attached client — and fleet's terminal is the client its sessions get
attached from, making its env the best proxy for a single server-wide
option when reachability varies per client.

## tmux version gating

`allow-passthrough` appeared in tmux 3.3. Ubuntu 22.04 LTS ships 3.2a, where
an unknown option would abort fleet's whole batched `set-option` call — so
fleet probes the version and drops that option (only) on older servers.
The probe asks the *running server* (`display-message -p '#{version}'`),
not just the binary: options are interpreted by the server, and a package
upgrade swaps the binary without restarting a user's live server — `tmux
-V` alone would let a 3.4 client wave the option through to a still-running
3.2 server. The binary's `tmux -V` is only the fallback when no server is
up yet. tmux ≥ 3.3 is recommended; 24.04 ships 3.4.

## Idle-session suspend (memory pressure)

The suspend sweep's pressure probe reads `/proc/pressure/memory` (PSI,
kernel ≥ 4.20 with `CONFIG_PSI` — present on all mainstream distros) where
macOS reads the Jetsam pressure level: sustained `some avg10 ≥ 10%` maps to
warning, `full avg10 ≥ 10%` (or `some ≥ 50%`) to critical. Free swap comes
from `/proc/meminfo`, but unlike macOS's demand-grown swap files, Linux swap
partitions sit partially used on healthy boxes — so low free swap never
escalates the level on its own; it only upgrades a PSI *warning* to critical
(`SwapEscalatesPressure`, `pressure_linux.go`). A swapless box reports swap
as unknown, and kernels without PSI report unknown pressure — in both cases
no session is ever auto-suspended.

## Diagnostics / bug reports

On Linux, reports carry `PRETTY_NAME` from `/etc/os-release` plus
`uname -r` where macOS reports `sw_vers`.

## Packaging

- `Dockerfile` — golang:1.26 build stage → ubuntu:24.04 runtime with tmux,
  git, ca-certificates, locales. No clipboard tool: a plain container has no
  display server to reach, so copy-mode uses the OSC 52 path.
- `.goreleaser.yml` `nfpms` — `.deb`/`.rpm` with tmux+git dependencies,
  gh+xclip recommends; ships the systemd user unit to
  `/usr/lib/systemd/user/`.
- `contrib/systemd/fleet-tmux.service` — optional user unit starting the
  tmux server at login; see comments in the unit for `loginctl
  enable-linger`.
- Package installs land the binary in `/usr/bin`, which an unprivileged
  user can't replace — the auto-updater probes this *before* downloading
  and skips with a hint in `debug.log` (once per hourly check, not per
  launch). Update via `apt`/`dnf`, or turn auto-update off in Settings.

## Known limits on Linux

- Editor launch works via CLI launchers on PATH (`code`, JetBrains Toolbox
  scripts, `vim`, …). The macOS app-bundle fallback (`open -a`) has no Linux
  equivalent; an editor without its CLI on PATH won't be offered.
- Watching very many worktrees may hit `fs.inotify.max_user_watches`; raise
  it via sysctl if the hook watcher logs errors.
- The Chrome tab-control extension installs its native-messaging manifest to
  `~/.config/{google-chrome,chromium}/NativeMessagingHosts` (already
  handled upstream).
