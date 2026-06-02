---
type: added
---
First-run launchpad: when the fleet is empty, fleet now scans your Claude Code history (`~/.claude/projects`) and offers the repos & worktrees you've recently worked in — grouped by origin with nested branches, exactly like the sidebar — instead of a blank "No Sessions Yet" screen. They're all pre-checked, so a single `↵` adds your whole working set: each repo/worktree is pinned and its last conversation resumed (`claude --resume`). `space` toggles a row, `A` selects all/none, `n` types a path, `Esc` falls back to the bare empty state. The boot splash stays up through the scan and reveals the launchpad in one transition. Non-git and missing paths are dropped.
