---
name: fleet
description: Drives fleet, a CLI that runs parallel Claude Code, Codex, OpenCode, and Copilot sessions in git worktrees or existing directories. Use when handing independent work to a background agent session, creating a git worktree to work in, starting a session on a chosen agent/model/effort, checking what every session is doing or waiting on, reading a session's output, answering its prompt, sending it a message and waiting for the result, restarting or suspending it, or when the user mentions fleet, `fleet wt`, `fleet add`, `fleet send`, `fleet status`, worktree sessions, or running agents in parallel.
---

<!-- Installed by `fleet skill install`. Local edits are overwritten on reinstall. -->

# fleet

`fleet` runs coding-agent sessions in parallel, each in its own tmux session and — unless
you point it at an existing directory — its own git worktree. From a shell you can create
one, hand it a task, see what it and every other session is doing, answer its prompts, and
collect its result.

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
- `--agent claude|codex|opencode|copilot` overrides the user's configured default agent.
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
  (`anthropic/claude-sonnet-5`), and Copilot's own ids. fleet passes it through and does not translate between
  agents, so use the spelling the chosen agent uses.
- `--effort` works for **Claude, Codex and Copilot** — it becomes `--effort` for Claude
  and Copilot and a `model_reasoning_effort` config override for Codex. Claude accepts
  `low|medium|high|xhigh|max|ultracode`; Codex and Copilot take their own levels. Passing
  it with `--agent opencode` is an error, not a silent no-op: the command fleet launches
  OpenCode with has no option for it, so it can only be set inside the agent. `--model`
  works for all four (Copilot falls back to `auto` on a model it doesn't know).
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
  run as a shell command. Not forceable — `fleet restart <session>` instead.
- The session is **suspended**: fleet wakes it and delivers the message as its first
  prompt. Nothing for you to do.

Delivery is verified against the pane, and there is no auto-retry. If it reports failure,
check `fleet status <session>` (its last prompt) before sending again — a duplicate send
submits the task twice.

## See what every session is doing

```bash
fleet status                 # one line per session, live
fleet status --waiting       # only sessions blocked on a prompt (--active: working or blocked)
fleet status <session>...    # detail: last prompt, last reply, the prompt, the input box
fleet status --json          # the same, structured; --full stops truncating prompts and replies
```

Status is recomputed live with the pipeline fleet's TUI uses, so it is current whether or
not the TUI is open, and costs two tmux calls for all sessions together — call it freely.
Suspended sessions are counted, not listed, unless you pass `--all`.

Under a session's line, an indented line appears only when there is something to act on:

- `? <question> [<what it is about>]  1) …  2) …` — blocked on a prompt. The bracket is
  the command or file the prompt is about.
- `✎ unsent draft: "…"` — text typed into the input box and never sent. Claude's dim
  **suggestion** in an empty box is not a draft and is never reported as one.
- `! …` — the agent exited, and why if fleet can tell (`resume failed: no conversation
  found` means `fleet restart <session> --fresh`).

`exited` means the agent process is gone and the pane is at a shell prompt.

## Read what a session said

```bash
fleet read <session>             # last 3 turns from its transcript: your prompt, its final reply
fleet read <session> --turns 1   # just the latest exchange
fleet read <session> --lines 80  # the screen instead, chrome stripped (scrollback included)
```

Turns come from the agent's own transcript (Claude), which holds the full reply rather than
whatever happened to be on screen. For other agents, `fleet read` prints the cleaned screen.

## Delegate and collect

```bash
fleet send -wait <session> "<task>"            # send, then block until the turn ends
fleet wait <session> --until done --timeout 20m
fleet wait a b c --any                          # first of several to finish
```

Both print the session's final reply when they return. Branch on the exit code rather than
parsing text: **0** finished, **2** timed out, **3** blocked on a prompt (the prompt is
printed — answer it or tell the user), **4** the agent exited or errored. `--until` also
takes `idle` (finished its turn), `waiting` and `running`. `fleet send ...; fleet wait ...`
is safe too: a state that already holds when the wait starts only counts once it has held
for a few seconds, so the wait never returns before the agent has picked the message up.

Don't poll `fleet status` in a loop to wait — `fleet wait` is built for it.

## Answer prompts and steer a session

```bash
fleet answer <session> <n|yes|no> [--expect "<text from the question>"]
fleet interrupt <session>      # Esc: stop the current turn
fleet submit <session>         # send the unsent draft in its input box
fleet clear <session>          # erase the unsent draft
fleet restart <session>        # relaunch an exited agent, resuming its conversation
fleet suspend <session>        # free its memory; fleet resume <session> brings it back
fleet remove <session>         # delete the session (its worktree is kept)
```

`fleet answer` picks by the numbers `fleet status` shows, or `yes`/`no` for the option
whose label opens with that word. It checks the cursor landed before pressing Enter and
prints the next question if the prompt has several. Pass `--expect` with a few words of the
question you read, so it refuses if a different prompt has appeared since.

A permission prompt is the user's safety check. Answer one only when the user handed you
that call or it is plainly inside the task they gave the session; anything destructive or
outside that scope, show to the user instead. Questions the agent asks (`kind: question`)
are usually yours to answer if you delegated the work.

Every command here refuses rather than typing into the wrong place: a pane at a shell
prompt, a prompt on screen when you meant the input box, an interrupt with nothing
running. The error says what to do instead. `restart` refuses to resume a Claude
conversation with no transcript (one that never got a prompt has none, and the launch
would fail with "No conversation found"); `--fresh` starts a new conversation.

`fleet list` still prints id, title, status and path from fleet's database alone, without
reading any pane.

## Two things not to do

- **Never run bare `fleet`.** It launches a full-screen TUI and will hang a
  non-interactive shell. Every command above is one-shot.
- **Don't spawn a session per subtask.** One session is a whole agent with its own
  context, not a job-queue entry. One per independent thread of work.
