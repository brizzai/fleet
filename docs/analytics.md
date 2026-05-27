# Analytics

fleet collects anonymous usage metrics and crash reports via [Sentry](https://sentry.io) to understand how the tool is used, catch regressions early, and prioritize development.

The backend changed from Amplitude to Sentry — the opt-out controls are the same.

## What We Collect

### Counters (one per occurrence)

Lifecycle and direct actions:

| Event | Attributes | When |
|---|---|---|
| `app_started` | `version`, `session_count`, `repo_count` | TUI launches |
| `app_quit` | `uptime_seconds`, `session_count` | TUI exits |
| `session_created` | — | New session started |
| `session_attached` | — | User enters a session |
| `session_restarted` | — | Session restarted |
| `session_deleted` | — | Session deleted |
| `session_renamed` | — | Session renamed |
| `session_orphaned` | `lifetime_seconds` | Session deleted without ever being attached |
| `quick_approve` | — | Y key pressed |
| `editor_opened` | `editor` | e key pressed |
| `pr_opened` | — | p key pressed |
| `workspace_created` | `provider` | Worktree/workspace created |
| `theme_changed` | `theme` | Theme cycled in settings |
| `filter_used` | — | / key pressed |
| `space_jump` | — | Space key pressed |
| `settings_opened` | — | S key pressed |
| `bug_report_opened` | — | ! key pressed |
| `command_palette` | — | : / Ctrl+P pressed |
| `reload_all` | — | "Reload all sessions" command |
| `mark_all_read` | — | "Mark all read" command |
| `error_occurred` | `category` | Any error shown |
| `manual_rename_after_auto` | — | User renames a session we auto-named (signal that auto-title was wrong) |
| `quit_with_running_sessions` | `running_count`, `waiting_count` | App quit while sessions were active |
| `claude_prompt_submitted` | — | UserPromptSubmit hook fired (engagement) |
| `claude_response_received` | — | Stop hook fired (Claude finished a turn) |
| `chrome_extension_connected` | — | Chrome native messaging host reachable (first success or after a failure) |
| `chrome_extension_disconnected` | — | Chrome native messaging host failed after previously succeeding |

Onboarding funnel (one-shot per install, persisted in `~/.config/fleet/install_state.json`):

| Event | Attributes | When |
|---|---|---|
| `onboarding_first_launch` | — | First TUI launch on this install |
| `onboarding_first_session_created` | `seconds_since_install` | First time a session is created |
| `onboarding_first_attach` | `seconds_since_install` | First time the user attaches to a session |
| `onboarding_first_claude_response` | `seconds_since_install` | First Stop hook seen — Claude actually answered |
| `onboarding_first_quit` | `uptime_seconds`, `session_count`, `attached_at_least_once` | First app quit after install |

### Gauges (point-in-time snapshots)

Emitted at boundary events (`app_started`, `app_quit`):

| Metric | Attributes | Meaning |
|---|---|---|
| `repos_total` | — | Number of pinned repos |
| `worktree_repos_total` | — | Subset that are git worktrees |
| `sessions_total` | — | Number of active sessions |
| `sessions_by_status` | `status` | Sessions per status (running/waiting/finished/idle/error/starting) |
| `slot_bindings_total` | — | RTS-style slot bindings set |

### Distributions (sampled values)

| Metric | Sampled at | Meaning |
|---|---|---|
| `session_lifetime_seconds` | Session deleted | Time from create → delete |
| `session_prompts_per_session` | Session deleted | `PromptCount` at delete time |
| `attached_session_uptime_seconds` | User detaches | Time spent attached |
| `app_uptime_seconds` | App quit | Total time the TUI was open |
| `sessions_per_repo` | Snapshot | One value per repo |

### Default Attributes (applied to every metric)

| Attribute | Example |
|---|---|
| `app_version` | `v1.0.0` |
| `os_version` | `15.3` |
| `arch` | `arm64` |
| `device_id` | `abc12345…` (SHA256, see below) |
| `theme` | `tokyo-night` |
| `enter_mode` | `attach` |
| `auto_name_sessions` | `true` |
| `copy_claude_settings` | `true` |

### Errors and Panics

The same Sentry SDK also captures:
- **Panics** in the TUI run loop — wrapped with `sentry.CaptureException` then re-raised so the runtime still prints a stack trace.
- **Flash errors** shown to the user via `ErrorHistory.Add()` — same buffer that backs the bug report dialog.

Both are gated by the same telemetry toggle as the counters above.

## What We Do NOT Collect

- File paths or project names
- Code content or prompts (only the count of prompts, not their contents)
- Usernames, emails, or any PII
- Git branch names or commit hashes
- Session titles or repo names
- IP addresses (Sentry anonymizes by default)

There's also a defense-in-depth `sanitizeKey` filter in `internal/analytics/analytics.go` that drops any attribute whose key matches a small PII blocklist (`path`, `repo`, `branch`, `title`, `hostname`, `prompt`, `message`, etc.).

## Privacy

### Anonymous Device ID

Each installation generates a **one-way SHA256 hash** of the macOS hardware UUID. This hash:
- Cannot be reversed to identify you or your machine
- Is stable across app updates (cached at `~/.config/fleet/device_id`)
- Is the only identifier sent to Sentry (attached as `user.id` and the `device_id` attribute)

## How to Opt Out

Any of these methods will completely disable analytics **and** crash/error reporting — there is a single toggle for both:

### 1. Environment Variable

```bash
export FLEET_TELEMETRY_DISABLED=1
```

Or use the standard [Do Not Track](https://consoledonottrack.com/) convention:

```bash
export DO_NOT_TRACK=1
```

### 2. Config File

Edit `~/.config/fleet/config.json`:

```json
{
  "telemetry": false
}
```

### 3. Settings Dialog

Press `S` in the TUI and toggle **Telemetry** to **off**.

When telemetry is disabled, `sentry.Init` is never called and the global meter is a no-op — no network traffic of any kind.

## Architecture

```text
internal/analytics/
├── analytics.go        # sentry.Init, meter, Track, Gauge, Distribution, CaptureError
├── events.go           # Event-name and metric-name constants
├── onboarding.go       # ~/.config/fleet/install_state.json milestone dedupe
└── snapshot.go         # EmitSnapshot — boundary gauges
```

- **Global singleton** — `Init()` creates once, all helpers are safe to call from anywhere
- **No-op when disabled** — every helper checks the disabled flag and returns immediately
- **Thread-safe** — protected by mutex; the Sentry meter itself is goroutine-safe
- **Flushed on quit** — `Shutdown()` calls `sentry.Flush(2s)` before the binary exits

## DSN

Events are routed to a fleet-specific Sentry project. The DSN is compiled into the binary (`sentryDSN` in `analytics.go`) — there is no remote configuration. Subprocesses like `fleet hook-handler` do **not** initialize Sentry because they're transient (one invocation per Claude Code hook fire); panics there are logged to `~/.config/fleet/debug.log` only.
