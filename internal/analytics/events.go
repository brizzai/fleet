package analytics

// Event name constants.
//
// Naming convention: snake_case verb-or-noun phrases. New onboarding events
// share an `onboarding_` prefix so they're easy to filter together in Mixpanel.
const (
	// Lifecycle.
	EventAppStarted = "app_started"
	EventAppQuit    = "app_quit"
	// EventAppActive is the daily-active heartbeat — emitted at most once per
	// calendar day per process (see analytics.Heartbeat) so daily-active-user
	// counts stay accurate for long-running instances that rarely restart.
	// Sent in both full and minimal mode.
	EventAppActive = "app_active"

	// Sessions.
	EventSessionCreated   = "session_created"
	EventSessionAttached  = "session_attached"
	EventSessionRestarted = "session_restarted"
	EventSessionDeleted   = "session_deleted"
	EventSessionRenamed   = "session_renamed"
	EventSessionOrphaned  = "session_orphaned"
	// EventSessionErrored fires once per move into the error status, not on every
	// status pass that finds a session still there. Props: agent; reason, a short
	// enum (start_failed, restart_failed, respawn_failed, tmux_gone, pane_dead,
	// hook_dead, agent_error) — never error text; seconds_since_create; resumed,
	// whether the launch continued an existing conversation (resume or fork) —
	// false for a session launched by an earlier fleet process.
	EventSessionErrored = "session_errored"
	// EventAttachBailed is an attach left within 10s of entering it: the user
	// looked, and came straight back. Props: agent, seconds.
	EventAttachBailed = "attach_bailed"

	// Direct actions.
	EventQuickApprove = "quick_approve"
	EventEditorOpened = "editor_opened"
	EventPROpened     = "pr_opened"
	EventUndoUsed     = "undo_used"
	EventForkSession  = "fork_session"

	// Workspaces & repos.
	EventWorkspaceCreated = "workspace_created"

	// Dialogs.
	EventSettingsOpened  = "settings_opened"
	EventBugReportOpened = "bug_report_opened"
	// EventStatusMisdetected carries a user-confirmed status misdetection:
	// which status was shown, which was correct, and what each signal said.
	// Enum values and booleans only — no paths, no screen content. Fires on
	// submit, so it survives the reporter abandoning the GitHub issue.
	EventStatusMisdetected = "status_misdetected"
	EventCommandPalette    = "command_palette"
	EventDialogOpened      = "dialog_opened"
	EventDialogSubmitted   = "dialog_submitted"
	EventDialogCanceled    = "dialog_canceled"

	// Navigation / filtering.
	EventFilterUsed = "filter_used"
	EventSpaceJump  = "space_jump"
	EventHeaderJump = "header_jump"

	// Frost mode was started from the sidebar.
	EventFrostMode = "frost_mode"

	// RTS slot bindings.
	EventSlotBindingSet = "slot_binding_set"
	EventSlotJumpUsed   = "slot_jump_used"

	// Configuration changes. EventThemeChanged fires once per commit — Settings
	// closed on a different theme, or the first-run picker confirmed — carrying
	// the final theme, never once per preview step.
	EventThemeChanged  = "theme_changed"
	EventConfigChanged = "config_changed"

	// Bulk actions.
	EventReloadAll   = "reload_all"
	EventMarkAllRead = "mark_all_read"
	EventMarkUnread  = "mark_unread"

	// Snooze (attention mute). Props: scope=session|checkout|origin,
	// duration=30m|1h|4h|tomorrow, reason=manual|expired. No titles or paths.
	EventSnoozeSet     = "snooze_set"
	EventSnoozeCleared = "snooze_cleared"

	// Claude / hook signals (engagement).
	EventClaudePromptSubmitted  = "claude_prompt_submitted"
	EventClaudeResponseReceived = "claude_response_received"
	EventAutoNameSucceeded      = "auto_name_succeeded"

	// Chrome extension.
	EventChromeExtensionConnected    = "chrome_extension_connected"
	EventChromeExtensionDisconnected = "chrome_extension_disconnected"

	// Updater.
	EventUpdateCheck   = "update_check"
	EventUpdateApplied = "update_applied"

	// Frustration / failure signals. EventErrorOccurred is a real failure shown
	// as an error toast; guidance ("no PR for this branch") is an info toast and
	// is not tracked.
	EventErrorOccurred           = "error_occurred"
	EventManualRenameAfterAuto   = "manual_rename_after_auto"
	EventQuitWithRunningSessions = "quit_with_running_sessions"
	EventBugReportSubmitted      = "bug_report_submitted"
	// EventStartupFailed is fleet refusing to go on without a dependency. Props:
	// reason = tmux_missing (sent from main before the TUI exists, and only for a
	// user who has answered the consent prompt) | claude_missing (Enter on the
	// launchpad with no claude on PATH).
	EventStartupFailed = "startup_failed"

	// Subsystem failures (counters).
	EventTmuxCommandFailure = "tmux_command_failure"
	EventGitCommandFailure  = "git_command_failure"
	EventGhCommandFailure   = "gh_command_failure"

	// Ticket-tracker failures. One event for every provider, with the provider
	// as a property rather than baked into the name: a per-provider event name
	// makes "how often does ticket fetching fail" a query that has to be
	// rewritten every time a tracker is added.
	EventTicketCommandFailure = "ticket_command_failure"

	// Tickets materialized into a worktree. Carries a "provider" property.
	EventTicketMaterialized = "ticket_materialized"

	// Onboarding funnel (one-shot per install).
	EventOnboardingFirstLaunch         = "onboarding_first_launch"
	EventOnboardingFirstSessionCreated = "onboarding_first_session_created"
	EventOnboardingFirstAttach         = "onboarding_first_attach"
	EventOnboardingFirstClaudeResponse = "onboarding_first_claude_response"
	EventOnboardingFirstQuit           = "onboarding_first_quit"

	// First-run launchpad: the "pick up your recent repos" screen an empty fleet
	// opens on. Props are counts, never the repos themselves —
	// launchpad_shown {discovered_repos} when it renders with items,
	// launchpad_launched {selected, discovered} on Enter, and
	// launchpad_skipped {discovered} on Esc. Not one-shot: an empty fleet shows
	// the launchpad on every launch.
	EventLaunchpadShown    = "launchpad_shown"
	EventLaunchpadLaunched = "launchpad_launched"
	EventLaunchpadSkipped  = "launchpad_skipped"

	// Distribution metric names (not counters; used with analytics.Distribution).
	MetricSessionLifetimeSeconds    = "session_lifetime_seconds"
	MetricSessionPromptsPerSession  = "session_prompts_per_session"
	MetricAttachedSessionUptimeSecs = "attached_session_uptime_seconds"
	MetricAppUptimeSeconds          = "app_uptime_seconds"
	MetricSessionsPerRepo           = "sessions_per_repo"
	MetricSecondsSinceInstall       = "seconds_since_install"

	// Gauge metric names (used with analytics.Gauge).
	MetricReposTotal         = "repos_total"
	MetricWorktreeReposTotal = "worktree_repos_total"
	MetricSessionsTotal      = "sessions_total"
	MetricSessionsByStatus   = "sessions_by_status"
	MetricSlotBindingsTotal  = "slot_bindings_total"
)
