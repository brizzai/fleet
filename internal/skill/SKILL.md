---
name: fleet
description: Drives fleet, a CLI that runs parallel Claude Code, Codex, and OpenCode sessions in git worktrees or existing directories. Use when handing independent work to a background agent session, creating a git worktree to work in, starting a session on a chosen agent/model/effort, sending a message to or checking on a running session, or when the user mentions fleet, `fleet wt`, `fleet add`, `fleet send`, worktree sessions, or running agents in parallel.
---

<!-- Installed by `fleet skill install`. Local edits are overwritten on reinstall. -->

# fleet

`fleet` runs coding-agent sessions in parallel, each in its own tmux session and — unless
you point it at an existing directory — its own git worktree. From a shell you can create
one, hand it a task, and message it later.

Run `fleet <command> --help` for the exact flags. This file covers when and how to reach
for them.

## When to hand work to a session

Spawn a session when the work is **independent of the current turn** and would otherwise
block it: a second feature, a long refactor, a flaky-test hunt, an issue that just needs
doing. The session gets its own worktree, so it can't collide with the tree you're editing.

Do the work inline instead when it's part of what the user asked for right now, or when it
touches files being edited in this conversation.

Say which session you started and what you gave it. The user drives fleet's TUI and needs
to know a new row appeared.

## Start a session on a task

```bash
fleet wt <branch> -p "<the task, phrased as you would to another agent>"
```

Creates the worktree, starts the agent, and delivers the prompt as its first message, so
the session opens **already working on it**. `-p` accepts multi-line text and text starting
with `-`.

Pipe a task in from another tool with `-p -`, which reads stdin:

```bash
gh issue view 242 | fleet wt fix-242 -p -
```

Worth knowing:

- `--base <branch>` branches from something other than the repo's default branch.
- `--path <repo>` picks the repo (defaults to cwd). It resolves to the repo's **main**
  worktree, so running it from inside one worktree doesn't nest names.
- `--agent claude|codex|opencode` overrides the user's configured default agent.
- `--account <email>` picks which Claude subscription the session bills to. Claude only —
  passing it with another agent is an error, not a silent no-op.
- `--ticket BRZ-3182` materializes a Linear issue (description, comments, screenshots)
  into the worktree and names the branch from it. Conflicts with `-p`.
- `--no-session` makes only the worktree and prints its path, so it composes:
  `cd "$(fleet worktree scratch --no-session)"`.
- `--model` / `--effort` — see below.

## Start a session in a directory that already exists

```bash
fleet add [path] -p "<the task>"
```

No worktree and no branch: the agent works in that directory as-is. The path defaults to
the current directory. Use this when the directory is **already isolated** — a worktree
someone made earlier, a scratch clone, a non-git folder — and prefer `fleet wt` otherwise,
since a session sharing a tree with the one you're editing will collide with you.

It takes the same session-shaping flags as `fleet wt`: `-p` (including `-p -` for stdin),
`--agent`, `--account`, `--model`, `--effort`. Bare `fleet add` with no arguments prints
usage rather than starting anything.

## Choose the model and effort

```bash
fleet wt  <branch> --model opus --effort xhigh -p "<task>"
fleet add <path>   --agent codex --model gpt-5.1-codex-max --effort high
```

- `--model` takes whatever that agent accepts: an alias or full id for Claude (`opus`,
  `claude-opus-5`), a model id for Codex, and `provider/model` for OpenCode
  (`anthropic/claude-sonnet-5`). fleet passes it through and does not translate between
  agents, so use the spelling the chosen agent uses.
- `--effort` works for **Claude and Codex only** — it becomes `--effort` for Claude and a
  `model_reasoning_effort` config override for Codex. Claude accepts
  `low|medium|high|xhigh|max|ultracode`; Codex takes its own levels. Passing it with
  `--agent opencode` is an error, not a silent no-op: the command fleet launches OpenCode
  with has no option for it, so it can only be set inside the agent. `--model` works for
  all three.
- Both apply to the **launch only**. The user can change model in-session (`/model`), and
  a later restart uses the agent's own defaults rather than re-imposing these.
- Values must be bare names — anything with a space or a shell metacharacter is rejected
  before the session is created.

Reach for these when the task warrants it (a hard refactor on `opus --effort xhigh`, a
mechanical sweep on a cheaper model), not by default. Left off, the user's own configured
defaults apply, which is usually what they want.

## Message a running session

```bash
fleet send <session> <message>
```

Flags must come **before** the selector, because a message is free-form prose that may
start with a dash. A lone `-` as the message reads stdin.

Selectors, most precise first: `@0`–`@9` slot, exact id, exact title, workspace name, git
branch, id prefix, title substring. A selector that matches several sessions is an error
listing them — answer it with a more precise selector, not a retry of the same string.

`fleet send` refuses rather than typing into the wrong place:

- The agent is **waiting on a prompt**: text typed into a numbered menu is discarded, and
  its Enter confirms whatever was highlighted. Pass `-force` only when the message really
  is the answer to that prompt.
- The pane is **back at a shell prompt** (the agent exited or crashed): the message would
  run as a shell command. Not forceable — restart the session instead.
- The session is **suspended**: fleet wakes it and delivers the message as its first
  prompt. Nothing for you to do.

Delivery is verified against the pane, and there is no auto-retry. If it reports failure,
check `fleet list` before sending again — a duplicate send submits the task twice.

## See what is running

```bash
fleet list
```

Columns: id, title, status, path. A status of `waiting` means that session needs a human,
so tell the user rather than trying to answer it for them.

## Two things not to do

- **Never run bare `fleet`.** It launches a full-screen TUI and will hang a
  non-interactive shell. Every command above is one-shot.
- **Don't spawn a session per subtask.** One session is a whole agent with its own
  context, not a job-queue entry. One per independent thread of work.
