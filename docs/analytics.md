# Analytics

fleet collects anonymous usage events via [Mixpanel](https://mixpanel.com) to understand how the tool is used, what new users do (and don't) succeed at, and to prioritize development.

## What We Collect

### Events

All events are **Mixpanel events** with a `distinct_id` set to the anonymous device ID (see Privacy below). Direct actions and lifecycle events:

| Event | Properties | When |
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
| `manual_rename_after_auto` | — | User renames a session we auto-named (auto-title was wrong) |
| `quit_with_running_sessions` | `running_count`, `waiting_count` | App quit while sessions were active |
| `claude_prompt_submitted` | — | UserPromptSubmit hook fired (engagement) |
| `claude_response_received` | — | Stop hook fired (Claude finished a turn) |
| `chrome_extension_connected` | — | Chrome native messaging host reachable |
| `chrome_extension_disconnected` | — | Chrome native messaging host failed after a previous success |

### Onboarding funnel (one-shot per install)

Persisted in `~/.config/fleet/install_state.json`. Each event fires exactly once per install:

| Event | Properties |
|---|---|
| `onboarding_first_launch` | — |
| `onboarding_first_session_created` | `seconds_since_install` |
| `onboarding_first_attach` | `seconds_since_install` |
| `onboarding_first_claude_response` | `seconds_since_install` |
| `onboarding_first_quit` | `uptime_seconds`, `session_count`, `attached_at_least_once` |

### Numeric metrics (events with a `value` property)

Mixpanel doesn't have native gauges or distributions, so these are emitted as events with a numeric `value` property. Use Mixpanel's aggregations (sum / avg / percentile by event) to chart them.

Gauges (snapshots at `app_started` + `app_quit`):

| Event | `value` | Extra properties |
|---|---|---|
| `repos_total` | count | — |
| `worktree_repos_total` | count | — |
| `sessions_total` | count | — |
| `sessions_by_status` | count | `status` |
| `slot_bindings_total` | count | — |

Distributions (sampled at the relevant moment):

| Event | `value` | When |
|---|---|---|
| `session_lifetime_seconds` | seconds | Session deleted |
| `session_prompts_per_session` | count | Session deleted |
| `attached_session_uptime_seconds` | seconds | User detaches (only on successful attach) |
| `app_uptime_seconds` | seconds | App quit |
| `sessions_per_repo` | count | Snapshot — one event per repo |

### People profile (Mixpanel `/engage`)

Set on the device's people profile so you can build cohorts and break events down by these dimensions:

| Property | Example |
|---|---|
| `app_version` | `v1.0.0` |
| `os_version` | `15.3` |
| `arch` | `arm64` |
| `theme` | `tokyo-night` |
| `enter_mode` | `attach` |
| `auto_name_sessions` | `true` |
| `copy_claude_settings` | `true` |

## What We Do NOT Collect

- File paths or project names
- Code content or prompts (only the count of prompts, not their contents)
- Usernames, emails, or any PII
- Git branch names or commit hashes
- Session titles or repo names

Defense-in-depth: `sanitizeKey` (in `internal/analytics/analytics.go`) drops any property whose key matches a small PII blocklist (`path`, `repo`, `branch`, `title`, `hostname`, `prompt`, `message`, etc.) before the event reaches the Mixpanel SDK.

## Privacy

### Anonymous Device ID

Each installation generates a **one-way SHA256 hash** of the macOS hardware UUID. This hash:
- Cannot be reversed to identify you or your machine
- Is stable across app updates (cached at `~/.config/fleet/device_id`)
- Is the only identifier sent to Mixpanel (as `distinct_id`)

## How to Opt Out

Any of these methods will completely disable analytics:

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

When telemetry is disabled, the Mixpanel client is never created and every call (`Track` / `Gauge` / `Distribution` / `SetUserProperties`) becomes a no-op — no network traffic.

## Architecture

```text
internal/analytics/
├── analytics.go     # Init, Track, Gauge, Distribution, SetUserProperties, Shutdown
├── events.go        # Event-name constants
├── onboarding.go    # ~/.config/fleet/install_state.json milestone dedupe
└── snapshot.go      # EmitSnapshot — boundary gauges
```

- **Buffered worker** — Mixpanel's SDK is synchronous HTTP. The TUI must never block in `Update()`, so events are pushed onto a 256-slot channel and a single worker goroutine ships them. Track/Gauge/Distribution return immediately.
- **Queue full?** — Events are dropped silently (logged at debug level). In practice the queue is small enough that this only happens if the network is completely stuck for an extended time.
- **Shutdown** — Closes the queue, waits up to 2s for the worker to drain, then returns. Surviving events are lost.
- **Subprocesses** — `fleet hook-handler` and `fleet chrome-host` do **not** initialize analytics — they're transient (one invocation per Claude Code hook fire) and starting an HTTP worker per invocation isn't worth it.

## Project Token

Compiled into the binary (`mixpanelToken` constant in `analytics.go`). No remote configuration; updating the token requires a new release.
