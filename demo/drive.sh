#!/bin/bash
set -euo pipefail
# fleet drive — run fleet in an isolated sandbox and drive it from the shell.
# Built so a coding agent can press keys and read the screen while working on
# the TUI, without touching the user's real sessions.
#
# Usage:
#   demo/drive.sh up [WxH]          Build, seed and launch. Default 120x40.
#   demo/drive.sh seed              Rebuild the fixture only.
#   demo/drive.sh key <keys...>     Send keys by name: key Down Down Enter
#   demo/drive.sh type <text...>    Send literal text.
#   demo/drive.sh snap [-e] [-w RE] Print the settled screen. -e keeps ANSI.
#   demo/drive.sh png <file>        Render the current frame to a PNG.
#   demo/drive.sh size <WxH>        Resize the window.
#   demo/drive.sh status            One line of state.
#   demo/drive.sh down [--purge]    Tear down. --purge also removes the sandbox.

# Physically resolved: on macOS /tmp is a symlink to /private/tmp, and
# `git rev-parse --show-toplevel` returns the real path. An unresolved root
# here makes pinned_repos and the sessions' repo roots different strings, so
# every checkout renders twice — once with its sessions, once empty.
DRIVE_DIR="$(cd /tmp && pwd -P)/fleet-drive"
SANDBOX="$DRIVE_DIR/home"
REPOS="$DRIVE_DIR/repos"
DRIVE_SESSION="fleetdrive"
FLEET="./build/fleet"
SNAP_TIMEOUT_MS=4000
SNAP_POLL=0.12
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$ROOT_DIR"

# Helper: every tmux call goes to a private server. TMUX_TMPDIR is what makes
# this a sandbox rather than a guest on the user's server — it is the single
# stop for four separate leaks: migration's session renames, the server-scope
# option writes in EnsureCopyCommand/EnsureExtendedKeys, a fixture name
# colliding with a real session, and RefreshSessionCache enumerating the
# user's real panes every tick.
tmux_() {
    # `env -u TMUX -u TMUX_PANE` is load-bearing, not tidiness. $TMUX names a
    # socket and tmux prefers it over TMUX_TMPDIR — verified: with TMUX_TMPDIR
    # pointing at an empty directory and $TMUX set, `tmux ls` still answered
    # from the $TMUX server. So without this, TMUX_TMPDIR is inert and every
    # command here lands on the caller's server: `down` would kill-server the
    # user's real tmux, and the fixture panes would be created among their real
    # sessions for their running fleet to adopt.
    #
    # This is the normal case, not an edge one: agent sessions run inside tmux
    # panes, so anything driving this script has $TMUX set.
    env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$DRIVE_DIR/tmux" tmux "$@"
}

# Helper: the environment every fleet process in the sandbox must inherit.
# The three *_CONFIG_DIR overrides are not redundant with HOME — those lookups
# check the env var first and fall back to HOME only if it is unset, so an
# ambient one inherited from the caller's shell would punch straight through
# the sandbox and let InjectClaudeHooks rewrite the real settings.json.
fleet_env() {
    echo "-e HOME=$SANDBOX"
    echo "-e TMUX_TMPDIR=$DRIVE_DIR/tmux"
    echo "-e CLAUDE_CONFIG_DIR=$SANDBOX/.claude"
    echo "-e CODEX_HOME=$SANDBOX/.codex"
    echo "-e OPENCODE_CONFIG_DIR=$SANDBOX/.config/opencode"
    echo "-e PATH=$SANDBOX/bin:/usr/bin:/bin:/usr/sbin:/sbin"
    echo "-e FLEET_DEMO_PREFIX=$REPOS"
    echo "-e FLEET_AUTO_UPDATE_DISABLED=1"
    echo "-e FLEET_TELEMETRY_DISABLED=1"
    echo "-e DO_NOT_TRACK=1"
    echo "-e FLEET_FREEZE_ANIM=1"
    echo "-e TERM=xterm-256color"
    echo "-e COLORTERM=truecolor"
}

# Helper: sub-second sleep. Resolved once because probing per call would cost
# more than the wait. bash 3.2 on macOS rejects `read -t 0.12`, so that is out.
NAP_KIND=""
nap() {
    if [ -z "$NAP_KIND" ]; then
        if sleep 0.01 2>/dev/null; then NAP_KIND=sleep
        elif command -v perl >/dev/null 2>&1; then NAP_KIND=perl
        elif command -v python3 >/dev/null 2>&1; then NAP_KIND=py
        else
            echo "drive: need one of sleep, perl, or python3 for sub-second waits." >&2
            exit 2
        fi
    fi
    case "$NAP_KIND" in
        sleep) sleep "$1" ;;
        perl)  perl -e "select(undef,undef,undef,$1)" ;;
        py)    python3 -c "import time;time.sleep($1)" ;;
    esac
}

# Helper: fail early and name the fix, rather than half-building a sandbox.
require_tools() {
    command -v tmux >/dev/null 2>&1 || {
        echo "drive: tmux is not installed. Install it with: brew install tmux" >&2; exit 2; }
    command -v sqlite3 >/dev/null 2>&1 || {
        echo "drive: sqlite3 is not installed. macOS ships it at /usr/bin/sqlite3; check your PATH." >&2; exit 2; }
    command -v git >/dev/null 2>&1 || {
        echo "drive: git is not installed." >&2; exit 2; }
}

# Helper: refuse to drive a fleet that is not there, with the command to fix it.
require_up() {
    if ! tmux_ has-session -t "$DRIVE_SESSION" 2>/dev/null; then
        echo "drive: fleet is not running. Run: demo/drive.sh up" >&2
        exit 3
    fi
}

# Helper: build only when needed. version=dev is a second lock on the
# auto-updater beside FLEET_AUTO_UPDATE_DISABLED — `make build` stamps a real
# `git describe` version, and a hit there replaces the binary and re-execs
# itself out from under the change being tested.
ensure_build() {
    if [ ! -x "$FLEET" ] || [ -n "$(find cmd internal -name '*.go' -newer "$FLEET" -print -quit 2>/dev/null)" ]; then
        echo "  Building (version=dev)..."
        make build VERSION=dev >/dev/null
    fi
}

# Helper: the sandbox HOME. Every line here buys one specific thing.
ensure_sandbox() {
    mkdir -p "$SANDBOX/.config/fleet" "$SANDBOX/bin" "$DRIVE_DIR/tmux" "$DRIVE_DIR/tmp"

    # Without this marker, the first launch runs migration, which renames
    # brizzcode_* sessions on whatever tmux server it can reach.
    touch "$SANDBOX/.config/fleet/.migrated-from-brizz-code"

    # This config is what keeps the first frame from being a dialog and later
    # frames from changing on their own. The consent and onboarding flags stop
    # two full-screen takeovers; the release-notes version stops the What's New
    # shimmer (a ~60ms animation loop); seen_tips stops five tipOnce boxes that
    # appear and then self-retire after 45s of visible time, changing the frame
    # with no input at all.
    cat > "$SANDBOX/.config/fleet/config.json" <<'JSONEOF'
{
  "analytics_consent_seen": true,
  "display_onboarding_seen": true,
  "telemetry_mode": "off",
  "release_notes_seen_version": "9999.0.0",
  "auto_update": false,
  "auto_name_sessions": false,
  "tick_interval_sec": 2,
  "session_suspend_mode": "off",
  "seen_tips": [
    "command_palette", "terminal_drawer", "agent_skill", "connect_linear",
    "sessions_suspended", "reload_failed_sessions", "tcc_blocked_folder",
    "hooks_repaired"
  ]
}
JSONEOF

    # The hard guarantee behind "no real agents". BuildLaunchCmd emits the bare
    # words claude/codex/opencode, resolved through the pane's PATH — so with
    # these first, a stray `r` or a Ctrl+K "Reload All Sessions" from whoever is
    # driving launches `cat`, not a real agent burning real quota. Everything
    # else in this script is a promise not to press a key; this is a mechanism.
    for a in claude codex opencode; do
        printf '#!/bin/sh\nexec cat\n' > "$SANDBOX/bin/$a"
        chmod +x "$SANDBOX/bin/$a"
    done
}

# Helper: real directories on disk, because sidebar grouping shells out to
# `git rev-parse --show-toplevel` and `git config --get remote.origin.url`.
ensure_repos() {
    mkdir -p "$REPOS"
    local git_id="-c user.email=drive@example.com -c user.name=drive -c commit.gpgsign=false"

    for r in api-server notes tools; do
        [ -d "$REPOS/$r/.git" ] && continue
        mkdir -p "$REPOS/$r"
        ( cd "$REPOS/$r"
          git init -q -b main .
          echo "# $r" > README.md
          git $git_id add -A
          git $git_id commit -qm "init" )
    done

    # example.com, not github.com, on purpose: it groups the checkout and its
    # worktree under one origin header while making `gh pr view` fail instantly
    # instead of spending a network round trip. PR badges are out of scope and
    # this is what keeps them out for free.
    ( cd "$REPOS/api-server"
      git remote get-url origin >/dev/null 2>&1 || \
          git remote add origin git@example.com:acme/api-server.git )

    if [ ! -d "$REPOS/api-server-auth" ]; then
        ( cd "$REPOS/api-server"
          git worktree add -q -b feat/auth "$REPOS/api-server-auth" 2>/dev/null || true )
    fi
    # The one dirty marker in the fixture.
    echo "wip $(date +%s)" >> "$REPOS/api-server-auth/README.md" 2>/dev/null || true
}

# Helper: let the binary write its own schema. `fleet list` specifically, not
# `add` or `worktree` — those call migration.Run(), which is the tmux-rename
# path this sandbox exists to avoid. The redirect matters too: debuglog's
# package default writes to stderr, so session.Open's Info lines would
# otherwise spray slog over the caller's terminal.
ensure_db() {
    HOME="$SANDBOX" "$FLEET" list >/dev/null 2>&1 || true
}

# The fixture. One line per session: id|title|repo|agent|status|pane
#
# Statuses are constrained by how UpdateStatus dispatches on agent, and this is
# the whole reason the table looks like it does. Only Claude reaches
# updateStatusFromPane, where a quiet pane makes detectStatus return "" and the
# default arm keeps whatever status was seeded — so running/waiting/finished/
# error are Claude-only fixed points. Codex and OpenCode force idle when there
# is no hook file, so idle is theirs. Suspended short-circuits before any tmux
# check, so it needs no pane at all and any agent can hold it.
#
# Mirrors buildPreviewFixture's vocabulary (2-3 word verb-first titles, every
# affordance exactly once) and extends it with error, suspended and OpenCode,
# which that fixture omits on purpose but a layout harness needs.
fixture_rows() {
    cat <<'ROWS'
d0000001|Refactor sidebar|api-server|claude|running|yes
d0000002|Fix flaky preview|api-server|claude|finished|yes
d0000003|Add test coverage|api-server-auth|claude|waiting|yes
d0000004|Wire codex hooks|api-server-auth|codex|idle|yes
d0000005|Scratch notes|notes|opencode|suspended|no
d0000006|Trim stale panes|tools|claude|error|yes
ROWS
}

# Helper: sanitizeName's rules, so a fixture name round-trips exactly as a real
# one would — every char outside [A-Za-z0-9-] becomes a hyphen, case preserved.
slug() {
    echo "$1" | sed 's/[^A-Za-z0-9-]/-/g; s/--*/-/g; s/^-//; s/-$//'
}

# Helper: one quiet pane per live row. `cat` with no stdin blocks forever,
# paints nothing and generates no window_activity, which is exactly what keeps
# detectStatus returning "" so the seeded status stands.
ensure_panes() {
    local id title repo agent status pane name
    while IFS='|' read -r id title repo agent status pane; do
        [ "$pane" = "yes" ] || continue
        name="fleet_$(slug "$title")_$id"
        tmux_ has-session -t "$name" 2>/dev/null && continue
        tmux_ new-session -d -s "$name" -c "$REPOS/$repo" cat
    done <<EOF
$(fixture_rows)
EOF
}

# Helper: the rows themselves.
seed_rows() {
    local now created id title repo agent status pane sid tmuxname i sql
    now=$(date +%s)
    created=$((now - 172800))
    i=2

    sql="PRAGMA busy_timeout=5000;
DELETE FROM slot_bindings; DELETE FROM snoozed_groups; DELETE FROM sessions;
DELETE FROM pinned_repos;"

    while IFS='|' read -r id title repo agent status pane; do
        sid="$id-$created"
        if [ "$pane" = "yes" ]; then
            tmuxname="fleet_$(slug "$title")_$id"
        else
            tmuxname=""
        fi
        # last_accessed lands in the middle of a display bucket, not on its
        # edge: the preview footer renders relative time, so seeding "now"
        # would give a frame that flips from "just now" to "1m ago" with no
        # input. At i*3600+1800 each row reads a stable "Nh ago" for 30 minutes.
        sql="$sql
INSERT INTO sessions (id,title,project_path,agent,account,status,tmux_session,
  created_at,last_accessed,acknowledged,claude_session_id,workspace_name,
  manually_renamed,first_prompt,title_generated,prompt_count,snoozed_until)
VALUES ('$sid','$title','$REPOS/$repo','$agent','','$status','$tmuxname',
  $created,$((now - (i * 3600 + 1800))),0,'','',1,'',1,0,0);"
        i=$((i + 1))
    done <<EOF
$(fixture_rows)
EOF

    # The one slot badge, and the one group snooze. The snooze goes on the
    # group holding the suspended row deliberately: a snoozed group renders its
    # children dim and suspended is already dim, so no affordance is masked.
    # Snoozing the group with the error row would grey out the error colour.
    sql="$sql
INSERT INTO slot_bindings (slot_number, session_id) VALUES (1, 'd0000002-$created');
INSERT INTO snoozed_groups (group_key, snoozed_until) VALUES ('$REPOS/notes', $((now + 144000)));
INSERT INTO pinned_repos (repo_path) VALUES ('$REPOS/api-server'),
  ('$REPOS/api-server-auth'), ('$REPOS/notes'), ('$REPOS/tools');"

    echo "$sql" | sqlite3 "$SANDBOX/.config/fleet/state.db" >/dev/null
}

launch_tui() {
    local w="$1" h="$2"
    local -a envargs=()
    while IFS= read -r line; do
        envargs+=(-e "${line#-e }")
    done < <(fleet_env)

    # remain-on-exit keeps the error text on screen when fleet dies on launch —
    # otherwise the pane vanishes and the only diagnosis is "it didn't work".
    tmux_ new-session -d -s "$DRIVE_SESSION" -x "$w" -y "$h" \
        -c "$ROOT_DIR" "${envargs[@]}" "$FLEET"
    tmux_ set-option -t "$DRIVE_SESSION" remain-on-exit on >/dev/null 2>&1 || true
    tmux_ set-option -t "$DRIVE_SESSION" window-size manual >/dev/null 2>&1 || true
    # Per-session, not -g: the session carries its own explicit value that a
    # global would not override, and a visible status bar silently costs the
    # pane a row — which would make every size this script reports a lie.
    tmux_ set-option -t "$DRIVE_SESSION" status off >/dev/null 2>&1 || true
}

# Resize so the PANE ends up the requested size, not the window. Those differ
# by the status bar, and a harness that mislabels its geometry is worse than no
# harness — every "clips at 80x22" finding would be off by one. Verify rather
# than assume, and correct once if tmux disagrees.
do_size() {
    local size="$1" w h got
    w="${size%x*}"; h="${size#*x}"
    tmux_ resize-window -t "$DRIVE_SESSION" -x "$w" -y "$h"
    nap 0.15
    got=$(tmux_ display-message -p -t "$DRIVE_SESSION" '#{pane_height}')
    if [ "$got" != "$h" ] && [ -n "$got" ]; then
        tmux_ resize-window -t "$DRIVE_SESSION" -x "$w" -y "$((h + h - got))"
        nap 0.15
        got=$(tmux_ display-message -p -t "$DRIVE_SESSION" '#{pane_height}')
    fi
    if [ "$got" != "$h" ]; then
        echo "drive: warning: asked for ${w}x${h}, pane is ${w}x${got}." >&2
    fi
}

# Helper: the splash uses ▰/▱ for its progress bar, so their absence is a
# precise "booted" signal — the only other user of those glyphs is the command
# palette's priority gauge, which cannot be open during boot.
wait_booted() {
    local i=0 frame
    while [ $i -lt 100 ]; do
        frame=$(tmux_ capture-pane -p -t "$DRIVE_SESSION" 2>/dev/null || echo "")
        case "$frame" in
            *▰*|*▱*) ;;
            *Sessions*|*"No Sessions"*) return 0 ;;
        esac
        nap 0.2
        i=$((i + 1))
    done
    echo "drive: fleet did not reach a booted frame in 20s." >&2
    echo "--- last frame ---" >&2
    tmux_ capture-pane -p -t "$DRIVE_SESSION" 2>&1 | sed 's/^/  /' >&2
    echo "--- debug.log tail ---" >&2
    tail -20 "$SANDBOX/.config/fleet/debug.log" 2>/dev/null | sed 's/^/  /' >&2
    exit 3
}

# Two consecutive identical captures, polled at 120ms. The interval is not
# 100ms on purpose: fleet's tick cadences are 100ms (spinners), 80ms (splash)
# and 60ms (shimmer), and a 100ms sampler aliases onto the spinner exactly —
# it would report a spinning dialog as settled. 120ms shares no small-integer
# ratio with any of them.
wait_stable() {
    local ansi="$1" want="$2"
    local prev="" cur="" elapsed=0 capargs="-p"
    [ "$ansi" = "yes" ] && capargs="-p -e"

    while [ $elapsed -lt $SNAP_TIMEOUT_MS ]; do
        # shellcheck disable=SC2086
        cur=$(tmux_ capture-pane $capargs -t "$DRIVE_SESSION" 2>/dev/null) || {
            echo "drive: fleet is not running. Run: demo/drive.sh up" >&2; exit 3; }
        if [ "$cur" = "$prev" ]; then
            if [ -z "$want" ] || printf '%s' "$cur" | grep -qE "$want"; then
                printf '%s\n' "$cur"
                return 0
            fi
        fi
        prev="$cur"
        nap $SNAP_POLL
        elapsed=$((elapsed + 120))
    done

    if [ -n "$want" ]; then
        echo "drive: frame never matched /$want/ within $((SNAP_TIMEOUT_MS/1000))s." >&2
        printf '%s\n' "$cur" >&2
        exit 3
    fi
    # An unsettled frame is not an error. The only frames that legitimately
    # never settle are the loading spinners, and aborting a `set -e` caller
    # over one is worse than handing back the frame with a caveat.
    echo "drive: warning: frame unsettled after $((SNAP_TIMEOUT_MS/1000))s — a spinner is probably running." >&2
    printf '%s\n' "$cur"
}

do_seed() {
    ensure_sandbox
    ensure_repos
    ensure_db
    ensure_panes
    seed_rows
}

do_up() {
    local size="${1:-120x40}" w h
    w="${size%x*}"; h="${size#*x}"
    require_tools
    echo "=== fleet drive ==="
    ensure_build
    echo "  [1/3] Seeding sandbox at $DRIVE_DIR..."
    do_seed
    echo "  [2/3] Launching fleet (${w}x${h})..."
    tmux_ kill-session -t "$DRIVE_SESSION" 2>/dev/null || true
    launch_tui "$w" "$h"
    echo "  [3/3] Waiting for boot..."
    wait_booted
    echo ""
    echo "=== ready ==="
    echo "  ${w}x${h}, 6 sessions across 4 checkouts under $REPOS"
    echo ""
    echo "  demo/drive.sh key S          # open settings"
    echo "  demo/drive.sh snap           # capture the screen as text"
    echo "  demo/drive.sh size 80x24     # probe a layout breakpoint"
    echo "  demo/drive.sh down           # tear it all down"
}

do_snap() {
    local ansi="no" want=""
    while [ $# -gt 0 ]; do
        case "$1" in
            -e) ansi="yes"; shift ;;
            -w) want="${2:-}"; shift 2 ;;
            *)  echo "drive: unknown snap option: $1" >&2; exit 1 ;;
        esac
    done
    require_up
    wait_stable "$ansi" "$want"
}

do_png() {
    local out="${1:-}"
    [ -n "$out" ] || { echo "drive: png needs an output path. Run: demo/drive.sh png /tmp/frame.png" >&2; exit 1; }
    command -v freeze >/dev/null 2>&1 || {
        echo "drive: png needs charmbracelet/freeze. Install it with:" >&2
        echo "  brew install charmbracelet/tap/freeze" >&2
        exit 2; }
    require_up
    local raw="$DRIVE_DIR/tmp/frame.ansi"
    wait_stable "yes" "" > "$raw"
    # Default font only. --font.family with a system font name (Menlo, SF Mono,
    # Andale Mono were all tried) exits 0 and writes a near-blank image — three
    # different names produced byte-identical 170KB files against 717KB for the
    # default. A silent wrong answer is worse than the missing glyphs below.
    freeze --language ansi -o "$out" "$raw" >/dev/null
    echo "$out"
}

do_status() {
    if tmux_ has-session -t "$DRIVE_SESSION" 2>/dev/null; then
        local size rows
        size=$(tmux_ display-message -p -t "$DRIVE_SESSION" '#{window_width}x#{window_height}')
        rows=$(sqlite3 "$SANDBOX/.config/fleet/state.db" "SELECT COUNT(*) FROM sessions;" 2>/dev/null || echo "?")
        echo "up  $size  $rows sessions  $DRIVE_DIR"
    else
        echo "down  $DRIVE_DIR"
    fi
}

do_down() {
    # kill-server is irreversible and takes every session on whichever server it
    # reaches, so verify the target rather than trusting that the environment
    # came out right. An earlier version asserted this isolation in a comment
    # and was wrong for exactly one reason ($TMUX outranking TMUX_TMPDIR); a
    # comment cannot fail a run, and this can.
    local sock
    sock=$(tmux_ display-message -p '#{socket_path}' 2>/dev/null || true)
    case "$sock" in
        "$DRIVE_DIR"/*)
            tmux_ kill-server 2>/dev/null || true
            ;;
        "")
            : # No server to kill; `down` stays idempotent.
            ;;
        *)
            echo "drive: refusing to kill-server — socket is $sock, outside $DRIVE_DIR." >&2
            echo "drive: that socket is not this sandbox's. Nothing was killed." >&2
            exit 3
            ;;
    esac
    if [ "${1:-}" = "--purge" ]; then
        rm -rf "$DRIVE_DIR"
        echo "Torn down and purged $DRIVE_DIR."
    else
        echo "Torn down. Sandbox kept at $DRIVE_DIR (--purge to remove)."
    fi
}

main() {
    local verb="${1:-}"
    [ $# -gt 0 ] && shift
    case "$verb" in
        up)     do_up "$@" ;;
        seed)   require_tools; do_seed; echo "Reseeded $DRIVE_DIR." ;;
        key)    require_up; tmux_ send-keys -t "$DRIVE_SESSION" "$@" ;;
        type)   require_up; tmux_ send-keys -t "$DRIVE_SESSION" -l -- "$*" ;;
        snap)   do_snap "$@" ;;
        png)    do_png "$@" ;;
        size)   require_up; do_size "$1" ;;
        status) do_status ;;
        down)   do_down "$@" ;;
        *)
            sed -n '3,17p' "$0" | sed 's/^# \{0,1\}//'
            [ -n "$verb" ] && exit 1 || exit 0
            ;;
    esac
}

main "$@"
