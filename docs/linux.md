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
| Holder discovery | one system-wide `lsof -Fpcn` | native `/proc/<pid>/{cwd,exe,fd/*}` readlinks + `maps` fallback |
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

## Clipboard

`tmux.EnsureCopyCommand` picks the copy-mode pipe per platform: `pbcopy` on
macOS; on Linux the first of `wl-copy` (Wayland) → `xclip` → `xsel`. With
none installed it sets `set-clipboard on` so tmux falls back to OSC 52,
which most modern Linux terminals accept. `FLEET_NO_COPY_COMMAND=1` opts out
entirely.

## tmux version gating

`allow-passthrough` appeared in tmux 3.3. Ubuntu 22.04 LTS ships 3.2a, where
an unknown option would abort fleet's whole batched `set-option` call — so
fleet probes `tmux -V` and drops that option (only) on older servers.
tmux ≥ 3.3 is recommended; 24.04 ships 3.4.

## Diagnostics / bug reports

On Linux, reports carry `PRETTY_NAME` from `/etc/os-release` plus
`uname -r` where macOS reports `sw_vers`.

## Packaging

- `Dockerfile` — golang:1.26 build stage → ubuntu:24.04 runtime with tmux,
  git, xclip.
- `.goreleaser.yml` `nfpms` — `.deb`/`.rpm` with tmux+git dependencies,
  gh+xclip recommends.
- `contrib/systemd/fleet-tmux.service` — optional user unit starting the
  tmux server at login; see comments in the unit for `loginctl
  enable-linger`.

## Known limits on Linux

- Editor launch works via CLI launchers on PATH (`code`, JetBrains Toolbox
  scripts, `vim`, …). The macOS app-bundle fallback (`open -a`) has no Linux
  equivalent; an editor without its CLI on PATH won't be offered.
- Watching very many worktrees may hit `fs.inotify.max_user_watches`; raise
  it via sysctl if the hook watcher logs errors.
- The Chrome tab-control extension installs its native-messaging manifest to
  `~/.config/{google-chrome,chromium}/NativeMessagingHosts` (already
  handled upstream).
