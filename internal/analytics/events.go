package analytics

// Event name constants.
//
// Naming convention: snake_case verb-or-noun phrases. New onboarding events
// share an `onboarding_` prefix so they're easy to filter together in Mixpanel.
const (
	// Lifecycle.
	EventAppStarted = "app_started"
	EventAppQuit    = "app_quit"

	// Sessions.
	EventSessionCreated   = "session_created"
	EventSessionAttached  = "session_attached"
	EventSessionRestarted = "session_restarted"
	EventSessionDeleted   = "session_deleted"
	EventSessionRenamed   = "session_renamed"
	EventSessionOrphaned  = "session_orphaned"

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
	EventCommandPalette  = "command_palette"
	EventDialogOpened    = "dialog_opened"
	EventDialogSubmitted = "dialog_submitted"
	EventDialogCanceled  = "dialog_canceled"

	// Navigation / filtering.
	EventFilterUsed = "filter_used"
	EventSpaceJump  = "space_jump"

	// RTS slot bindings.
	EventSlotBindingSet = "slot_binding_set"
	EventSlotJumpUsed   = "slot_jump_used"

	// Configuration changes.
	EventThemeChanged  = "theme_changed"
	EventConfigChanged = "config_changed"

	// Bulk actions.
	EventReloadAll   = "reload_all"
	EventMarkAllRead = "mark_all_read"

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

	// Frustration / failure signals.
	EventErrorOccurred           = "error_occurred"
	EventManualRenameAfterAuto   = "manual_rename_after_auto"
	EventQuitWithRunningSessions = "quit_with_running_sessions"
	EventBugReportSubmitted      = "bug_report_submitted"

	// Subsystem failures (counters).
	EventTmuxCommandFailure = "tmux_command_failure"
	EventGitCommandFailure  = "git_command_failure"
	EventGhCommandFailure   = "gh_command_failure"

	// Onboarding funnel (one-shot per install).
	EventOnboardingFirstLaunch         = "onboarding_first_launch"
	EventOnboardingFirstSessionCreated = "onboarding_first_session_created"
	EventOnboardingFirstAttach         = "onboarding_first_attach"
	EventOnboardingFirstClaudeResponse = "onboarding_first_claude_response"
	EventOnboardingFirstQuit           = "onboarding_first_quit"

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
