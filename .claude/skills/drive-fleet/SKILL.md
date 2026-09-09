---
name: drive-fleet
description: >
  Run fleet in an isolated sandbox and drive it — press keys, capture the screen
  as text, sweep terminal sizes — so a UI change can be looked at instead of only
  compiled. Use this whenever you touch anything that renders: a dialog, the
  sidebar, the preview pane, the drawer, a keybinding, a status glyph, a settings
  row, spacing, colors, or widths. Trigger on: "add a setting", "new dialog",
  "change the sidebar", "does this look right", "check the layout", "what does it
  look like", "screenshot fleet", "run fleet", "try it", "does it fit at 80
  columns", or any UI work where `make build` passing is not the same as it
  working.
allowed-tools: Read, Grep, Glob, Bash
user-invocable: true
---

# Drive the fleet TUI

`make build` proves a UI change compiles. It says nothing about whether the
dialog fits, the column aligns, or the footer is still on screen. This skill
closes that gap: boot a seeded fleet in a sandbox, press keys, read the screen.

The whole surface is `demo/drive.sh`. It drives a throwaway fleet under
`/private/tmp/fleet-drive` on its own tmux server — never the user's real
sessions. That holds when you are yourself running inside a tmux pane, which
you usually are: the script clears `$TMUX` before every tmux call, because
`$TMUX` names a server and outranks `TMUX_TMPDIR`, and `down` verifies the
socket it is about to kill lives under the sandbox.

## Step 1: Bring it up

```bash
demo/drive.sh up            # builds if needed, seeds, launches at 120x40
demo/drive.sh up 80x24      # or start at a size you want to test
```

First run takes a few seconds (Go build + `git init` on three repos). After
that `up` reuses the sandbox. If it is already running, `up` relaunches.

## Step 2: Drive it

```bash
demo/drive.sh key S                 # key names: S, Down, Enter, Escape, C-k
demo/drive.sh key Down Down Enter   # several keys in order
demo/drive.sh type "my branch name" # literal text — never key-name parsed
demo/drive.sh size 80x24            # resize; the pane really is 24 rows
```

`key` and `type` are different tools and neither substitutes for the other.
`key` hands its arguments to tmux's key-name parser, so `Down` means the arrow.
`type` sends literal characters, so `Down` means the word.

## Step 3: Read the screen

```bash
demo/drive.sh snap                       # settled plain-text grid
demo/drive.sh snap -e                    # keep ANSI, to check colors
demo/drive.sh snap -w 'Appearance'       # settle AND assert, in one call
demo/drive.sh png /tmp/frame.png         # image of that same frame
```

Prefer `-w` whenever you know what should appear. Without it you race the
dialog's first paint; with it, the wait and the assertion are one atomic step
and a failure is loud instead of a confusing empty grep.

`snap` returns when two consecutive captures match. It is honest about giving
up: after 4 seconds it prints the frame anyway with a warning on stderr, because
the only frames that never settle are the loading spinners and aborting over one
is worse than a caveat.

## What is in the sandbox

Six sessions across four checkouts, built so every visual affordance appears
exactly once:

```
▾ api-server 2  ◐ · ● · ●
  ▾ main  ● · ●
    ● ✻ Refactor sidebar          running · Claude
    ● ✻ Fix flaky preview [1]     finished · slot badge
  ▾ auth *  ◐                     worktree · dirty marker
    ◐ ✻ Add test coverage         waiting
    · ◇ Wire codex hooks          idle · Codex
▾ notes 1
  ▾ main  ☾ 2d                    group snooze
    · △ Scratch notes  ☾          suspended · OpenCode
▾ tools 1  ✕
  ▾ main  ✕
    ✕ ✻ Trim stale panes          error
```

Statuses are pinned by seeding SQLite and giving each row a live, silent tmux
pane. Which agent holds which status is not arbitrary — only Claude reaches the
pane-detection path where a quiet pane leaves the seeded status alone. Codex and
OpenCode force `idle` when no hook file exists, and `suspended` needs no pane at
all. If you add rows, keep that mapping or the status will be rewritten within
seconds and you will think you found a bug.

Not seeded: PR badges, tickets, drawer shells.

## Reading a capture well

**Check a dialog against the terminal height, not just its content.** This is
the failure this harness exists for — a dialog that renders correctly at 40 rows
and silently loses its footer at 22. Sweep it:

```bash
demo/drive.sh key S
for h in 40 30 24 22 20; do
  demo/drive.sh size 80x$h >/dev/null
  echo "$h: $(demo/drive.sh snap | grep -c 'esc save')"
done
```

A row that drops to 0 is a clip. `size` verifies the pane really is the height
you asked for, so these numbers can be trusted — the tmux status bar otherwise
steals a row and every measurement is off by one.

**Layout breakpoints worth probing:** 80 columns is the dual/single-pane
boundary, so `80x24` is the narrowest two-pane layout, not a comfortable
default. Try `79` and `49` for the stacked and single layouts.

**Count columns with the text, not the image.** `snap` output is an exact
character grid — `awk '{print length($0)}'`, `cut -c`, and `grep -n` all mean
what you expect. A PNG cannot answer "is this column aligned".

**A PNG is not authoritative for glyphs.** `freeze`'s font lacks a few of the
codepoints fleet uses, so `✻` (Claude), `◐` (waiting) and `☾` (snooze) render as
tofu boxes in an image while being perfectly fine in the terminal. Colors,
spacing, alignment and layout are faithful; glyph identity is not. Check glyphs
with `snap`, and never report a boxed glyph as a bug without confirming it in
the text first.

**Use `-e` for color questions only.** Colors carry meaning in fleet's design
system (focus > selection > mode), so if the change touched a status color or an
accent fill, capture ANSI and check the escape codes rather than guessing.

## Two things not to do

- **Never point this at the user's real fleet.** Every key here is safe because
  nothing in the sandbox is real. On a live fleet, `d` removes a worktree, `r`
  kills an agent mid-turn, and `Y` approves a permission prompt on the user's
  behalf. If they want you to look at their real fleet, ask — do not improvise.
- **Do not trust a green screenshot too far.** This catches layout: clipping,
  alignment, wrong glyphs, colors, spacing. It cannot catch the bugs that
  dominate fleet's history — status races, stale hook ids, keychain limits. A
  frame that looks right is not a passing test.

## Tear down

```bash
demo/drive.sh down             # stop; keeps the sandbox for a fast next `up`
demo/drive.sh down --purge     # also delete /private/tmp/fleet-drive
demo/drive.sh status           # up/down, size, session count
```

Leaving it running between edits is fine and is the fast path — `up` after a
code change rebuilds and relaunches in one command.

## Checklist

- [ ] `make build` passes
- [ ] `demo/drive.sh up` and the change is **on screen**, not just compiled
- [ ] Checked at a small size (`80x24`) if it renders in a dialog or panel
- [ ] Nothing else in the frame moved — `snap` before and after, and diff
- [ ] `demo/drive.sh down` when finished
