# fleet — Vision & Direction

## What is fleet?

A TUI for managing multiple coding-agent sessions (Claude Code, Codex) in parallel. It started because running several AI coding agents simultaneously means you lose track of which ones need your attention. fleet solves that — and is evolving into something bigger.

## The Reverse IDE

Traditional IDE: the human writes code, the tool assists.
fleet: **agents write code, the human orchestrates, reviews, and directs.**

When AI agents handle editing, navigating files, running commands, committing, and creating PRs — the developer's job shifts. You don't need an editor or file tree. You need:

- **Awareness** — what's happening across all your work, at a glance
- **Attention routing** — jump in exactly when an agent needs you
- **Comprehension** — understand what agents accomplished, fast
- **Direction** — decide what should happen next
- **Project health** — see the state of branches, PRs, CI, features

fleet is the workspace for this new workflow.

## The Core Problem: Comprehension at Scale

With 5-10 features in flight — each with an agent working on it — the hardest part isn't automation. The agents already commit, push, create PRs, run tests. That's solved.

The unsolved problem is **comprehension**:

- You can't hold all of it in your head
- Git diffs are too raw to scan quickly
- Terminal output is too noisy
- You need to *understand* what happened, not just *see data*
- You need to know what's done AND what's left

Raw data doesn't equal understanding. This is the gap fleet needs to close.

## Typical Workflow

- 1 session = 1 branch = 1 task/feature
- Sometimes 2 sessions per feature for independent parallel parts (e.g., two unrelated pages to build)
- The agent handles the git flow — commits, pushes, PRs via slash commands
- The user orchestrates: assigns work, monitors progress, approves when needed, reviews results

## Killer Feature Directions

### 1. AI-Powered Session Digests

Use the Anthropic API (Haiku — fast, cheap, ~$0.001/call) to summarize what a session accomplished:

- Capture pane content + git diff when a session finishes
- Generate a 1-2 sentence summary: *"Added JWT auth middleware with refresh token rotation. Modified 5 files. Tests passing."*
- Show in sidebar as a subtitle, expanded in preview

This directly solves the comprehension problem — understand any session in seconds without attaching.

### 2. Event-Driven Agent Spawning

Make fleet reactive to external events:

- CI fails on a branch → automatically start a fix session
- PR gets review comments → start a session to address them
- New ticket assigned in Linear → start a session with the ticket context

Agents spawn automatically. You wake up to work already in progress.

### 3. The Human Context Window

Humans have limited working memory, just like AI has a context window. fleet should manage yours:

- When you switch to a session: *"Last time you were here, you asked the agent to refactor the auth module. It finished 2 hours ago. 3 commits, PR #42 open, CI passing, 1 review comment."*
- Surface what changed since you last looked
- Minimize context-switching cost across features

### 4. Feature Context (Cross-Session Knowledge)

**The problem:** When working on a feature across multiple sessions (or restarting a session), context is trapped inside individual conversations. A bug investigation session discovers the root cause — but starting a new fix session means re-explaining everything. Two sessions on the same feature don't share knowledge about fields, APIs, pages that were added.

**The insight:** Claude Code already reads project files (CLAUDE.md, memory files) on every prompt. fleet can manage **ephemeral feature context** that sessions pick up automatically — without committing anything to the repo.

**How it could work:**
- fleet maintains a feature context file per branch, stored outside the repo (e.g., `~/.claude/projects/{project}/memory/feature_{branch}.md` — Claude's native memory directory)
- First prompt is captured as the feature description automatically
- Key discoveries/decisions can be added manually (hotkey) or via AI summarization when sessions finish
- Any session on that branch reads this context naturally — no injection needed
- Context is cleaned up when the branch is deleted

**Why this location:**
- `~/.claude/projects/{project}/memory/` is where Claude already looks for project context
- Outside the repo — nothing to gitignore, nothing to commit
- fleet already knows the project path and branch per session
- Multiple sessions on the same branch share the same context file

**Open questions:**
- Should this be automatic (always capture) or manual (explicit "save context" action)?
- How much should AI summarization play a role vs. user-written notes?
- Should context accumulate forever or have a rolling window?

## Long-Term Identity

fleet is evolving toward being a **one-stop-shop for AI-assisted development** — the place where all your agent-driven coding work converges. Not a code editor, but a work orchestrator.

Two possible directions (not mutually exclusive):

- **New-age IDE** — the primary workspace for developers who work through AI agents
- **Agentic workflow platform** — a reactive system that manages the lifecycle of AI coding tasks

## Design Principles

- **Don't automate what the agents already do.** They commit, create PRs, run tests. Don't duplicate that. Focus on what they *can't* do: see across sessions, prioritize attention, summarize work, react to external events.
- **Comprehension over data.** Showing a git diff doesn't help. Showing "Added auth middleware, 5 files changed, tests passing" does.
- **The user should feel 10x productive.** Not in control, not impressed by UI — *productive*. "I accomplish in 1 hour what used to take a full day."
- **Go deep on hook-capable agents.** The deep hook integration — instant status, conversation resume, prompt-based naming — is the moat. fleet supports Claude Code and Codex because they expose real hooks; it stays native to *how they work* rather than diluting that depth with shallow keystroke-shimming across every CLI.
