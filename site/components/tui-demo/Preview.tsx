"use client";

import type { Dispatch } from "react";
import { PALETTE } from "./palette";
import { SPINNER_FRAMES, STATUS_ICON } from "./glyphs";
import type { Action, DemoState, Session, Repo } from "./state";

interface Props {
  state: DemoState;
  spinnerFrame: number;
  dispatch: Dispatch<Action>;
}

const CLAUDE_LOGO = [
  " ▐▛███▜▌  ",
  "▝▜█████▛▘",
  "  ▘▘ ▝▝   ",
];

const CLAUDE_VERSION = "v2.1.152";
const CLAUDE_MODEL = "Opus 4.7 (1M context) with xhigh effort · Claude Max";

export function Preview({ state, spinnerFrame, dispatch }: Props) {
  const repo = state.repos[state.cursor.repoIdx];
  const session =
    repo && state.cursor.sessionIdx !== null
      ? repo.sessions[state.cursor.sessionIdx]
      : undefined;

  if (!repo || !session) {
    return <EmptyPreview repo={repo} />;
  }

  return (
    <ClaudeCodePreview
      repo={repo}
      session={session}
      spinnerFrame={spinnerFrame}
      inputFocused={state.inputFocused}
      inputText={state.inputText}
      dispatch={dispatch}
    />
  );
}

function EmptyPreview({ repo }: { repo?: Repo }) {
  return (
    <div
      style={{
        padding: "1.5rem",
        color: PALETTE.textDim,
        fontFamily: "var(--font-mono)",
        fontSize: "0.85rem",
        height: "100%",
        background: PALETTE.bg,
      }}
    >
      <span style={{ color: PALETTE.blue }}>{repo?.name ?? "—"}</span>
      <div style={{ marginTop: "0.5rem", fontStyle: "italic" }}>
        No session selected. Press{" "}
        <kbd
          style={{
            background: PALETTE.surfaceElev,
            color: PALETTE.text,
            padding: "0 0.35ch",
            borderRadius: 3,
          }}
        >
          a
        </kbd>{" "}
        to spawn one.
      </div>
    </div>
  );
}

function ClaudeCodePreview({
  repo,
  session,
  spinnerFrame,
  inputFocused,
  inputText,
  dispatch,
}: {
  repo: Repo;
  session: Session;
  spinnerFrame: number;
  inputFocused: boolean;
  inputText: string;
  dispatch: Dispatch<Action>;
}) {
  return (
    <div
      style={{
        background: PALETTE.bg,
        height: "100%",
        display: "grid",
        gridTemplateRows: "auto auto 1fr auto auto auto",
        fontFamily: "var(--font-mono)",
        fontSize: "0.78rem",
        color: PALETTE.text,
        overflow: "hidden",
      }}
    >
      <RepoExplanation repo={repo} session={session} />

      <ClaudeBanner />

      {/* Scrollable activity area */}
      <div
        style={{
          padding: "0.4rem 0.85rem",
          color: PALETTE.text,
          lineHeight: 1.55,
          overflow: "hidden",
          minHeight: 0,
        }}
      >
        <Activity session={session} spinnerFrame={spinnerFrame} />
      </div>

      <Rule />

      <PromptInput
        session={session}
        inputFocused={inputFocused}
        inputText={inputText}
        dispatch={dispatch}
      />

      <Rule />

      <ClaudeFooter />
    </div>
  );
}

function RepoExplanation({ repo, session }: { repo: Repo; session: Session }) {
  const statusMeta = sessionStatusMeta(session.status);
  const prMeta = repo.pr ? prStateMeta(repo.pr.state) : null;
  const timeLabel = sessionTimeLabel(session.status);

  return (
    <div
      style={{
        padding: "0.6rem 0.85rem 0.55rem",
        background: PALETTE.bg,
        borderBottom: `1px solid ${PALETTE.border}`,
        display: "flex",
        flexDirection: "column",
        gap: "0.18rem",
        fontFamily: "var(--font-mono)",
        fontSize: "0.78rem",
        lineHeight: 1.4,
      }}
    >
      {/* Line 1: ◐ session-name  status */}
      <div style={{ display: "flex", alignItems: "center", gap: "0.55ch" }}>
        <span
          style={{
            color: statusMeta.color,
            fontWeight: 700,
            textShadow: `0 0 6px ${statusMeta.color}88`,
          }}
        >
          {statusMeta.icon}
        </span>
        <span style={{ color: PALETTE.text, fontWeight: 600 }}>{session.name}</span>
        <span style={{ color: statusMeta.color }}>{statusMeta.label}</span>
      </div>

      {/* Line 2: path · last used */}
      <div style={{ color: PALETTE.textDim, fontSize: "0.74rem" }}>
        ~/code/{repo.name}
        <span style={{ margin: "0 0.7ch", color: PALETTE.border }}>·</span>
        {timeLabel}
      </div>

      {/* Line 3:  branch  * uncommitted  PR #N (...) */}
      <div
        style={{
          display: "flex",
          flexWrap: "wrap",
          alignItems: "center",
          gap: "0.7ch",
          fontSize: "0.78rem",
        }}
      >
        <span style={{ color: PALETTE.pink ?? "#f48fb1", display: "inline-flex", gap: "0.35ch" }}>
          <span aria-hidden></span>
          <span>{repo.branch}</span>
        </span>
        {repo.dirty && (
          <span style={{ color: PALETTE.yellow, fontWeight: 600 }}>
            * uncommitted
          </span>
        )}
        {repo.pr && prMeta && (
          <span style={{ color: prMeta.color, fontWeight: 600 }}>
            PR #{repo.pr.number}{" "}
            <span style={{ color: PALETTE.textDim, fontWeight: 400 }}>
              ({prMeta.detail})
            </span>
          </span>
        )}
      </div>
    </div>
  );
}

function sessionStatusMeta(status: Session["status"]): {
  color: string;
  icon: string;
  label: string;
} {
  switch (status) {
    case "running":
      return { color: PALETTE.green, icon: "●", label: "running" };
    case "waiting":
      return { color: PALETTE.yellow, icon: "◐", label: "waiting" };
    case "finished":
      return { color: PALETTE.blue, icon: "●", label: "finished" };
    case "idle":
      return { color: PALETTE.gray, icon: "○", label: "idle" };
    case "starting":
      return { color: PALETTE.accent, icon: "○", label: "starting" };
    case "error":
      return { color: PALETTE.red, icon: "✕", label: "crashed · press r to restart" };
  }
}

function prStateMeta(state: NonNullable<Repo["pr"]>["state"]): {
  color: string;
  detail: string;
} {
  switch (state) {
    case "approved":
      return { color: PALETTE.green, detail: "CI passing, approved" };
    case "pending":
      return { color: PALETTE.yellow, detail: "CI pending" };
    case "failing":
      return { color: PALETTE.red, detail: "CI failing, review pending" };
    case "merged":
      return { color: PALETTE.purple, detail: "merged" };
  }
}

function sessionTimeLabel(status: Session["status"]): string {
  switch (status) {
    case "running":
    case "starting":
      return "active now";
    case "waiting":
      return "waiting for you";
    case "finished":
      return "finished just now";
    case "idle":
      return "last used 3m ago";
    case "error":
      return "crashed 1m ago";
  }
}

function ClaudeBanner() {
  return (
    <div
      style={{
        padding: "0.85rem 0.85rem 0.5rem",
        borderBottom: `1px solid ${PALETTE.border}`,
        background: PALETTE.bg,
      }}
    >
      <div style={{ display: "flex", gap: "0.85rem", alignItems: "flex-start" }}>
        <pre
          aria-hidden
          style={{
            margin: 0,
            color: PALETTE.orange,
            lineHeight: 1,
            fontSize: "0.92rem",
            whiteSpace: "pre",
            textShadow: `0 0 8px ${PALETTE.orange}88`,
          }}
        >{CLAUDE_LOGO.join("\n")}</pre>
        <div style={{ display: "flex", flexDirection: "column", gap: 1 }}>
          <div style={{ color: PALETTE.text, fontWeight: 600 }}>
            Claude Code{" "}
            <span style={{ color: PALETTE.textDim, fontWeight: 400 }}>
              {CLAUDE_VERSION}
            </span>
          </div>
          <div style={{ color: PALETTE.textDim, fontSize: "0.74rem" }}>
            {CLAUDE_MODEL}
          </div>
        </div>
      </div>
    </div>
  );
}

function Activity({
  session,
  spinnerFrame,
}: {
  session: Session;
  spinnerFrame: number;
}) {
  if (session.pendingApprove) {
    return <ApprovePrompt session={session} />;
  }

  if (session.status === "starting") {
    return (
      <div style={{ color: PALETTE.accent }}>
        <span style={{ marginRight: "0.5ch" }}>
          {SPINNER_FRAMES[spinnerFrame % SPINNER_FRAMES.length]}
        </span>
        Spawning Claude session…
      </div>
    );
  }

  if (session.status === "error") {
    return (
      <div style={{ color: PALETTE.red }}>
        ✕ Session crashed. Press{" "}
        <kbd style={kbdInline}>r</kbd> to restart and resume.
      </div>
    );
  }

  // running / finished / idle — show recent activity as a short transcript
  const isRunning = session.status === "running";
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "0.3rem" }}>
      {session.activity && (
        <div style={{ color: PALETTE.text }}>
          <span style={{ color: PALETTE.purple, marginRight: "0.5ch" }}>●</span>
          {session.activity}
        </div>
      )}
      {isRunning && (
        <div style={{ color: PALETTE.textDim, fontStyle: "italic" }}>
          <span style={{ color: PALETTE.accent, marginRight: "0.5ch" }}>
            {SPINNER_FRAMES[spinnerFrame % SPINNER_FRAMES.length]}
          </span>
          Thinking…
        </div>
      )}
      {session.status === "finished" && (
        <div style={{ color: PALETTE.textDim }}>
          Done. Awaiting next prompt.
        </div>
      )}
    </div>
  );
}

function ApprovePrompt({ session }: { session: Session }) {
  return (
    <div
      style={{
        border: `1px solid ${PALETTE.yellow}`,
        borderRadius: 4,
        background: `${PALETTE.yellow}11`,
        padding: "0.5rem 0.75rem",
        display: "flex",
        flexDirection: "column",
        gap: "0.35rem",
      }}
    >
      <div style={{ color: PALETTE.yellow, fontWeight: 700 }}>
        ◐ Approve tool use?
      </div>
      <div style={{ color: PALETTE.text }}>
        {session.activity ?? "Bash command: gh pr ready"}
      </div>
      <div style={{ color: PALETTE.textDim, marginTop: "0.15rem" }}>
        Press{" "}
        <kbd
          style={{
            background: PALETTE.yellow,
            color: PALETTE.bg,
            padding: "0 0.5ch",
            borderRadius: 3,
            fontWeight: 700,
          }}
        >
          ⏎ Enter
        </kbd>{" "}
        to approve
      </div>
    </div>
  );
}

function PromptInput({
  session,
  inputFocused,
  inputText,
  dispatch,
}: {
  session: Session;
  inputFocused: boolean;
  inputText: string;
  dispatch: Dispatch<Action>;
}) {
  const isThinking =
    session.status === "running" || session.status === "starting";

  return (
    <div
      data-fleet-input="prompt"
      role="textbox"
      tabIndex={-1}
      onClick={(e) => {
        e.stopPropagation();
        dispatch({ type: "focus_input" });
      }}
      style={{
        padding: "0.5rem 0.85rem",
        display: "flex",
        alignItems: "center",
        gap: "0.5ch",
        color: PALETTE.text,
        cursor: "text",
        background: inputFocused ? `${PALETTE.accent}11` : "transparent",
        borderLeft: `2px solid ${inputFocused ? PALETTE.accent : "transparent"}`,
        transition: "background 0.15s ease, border-color 0.15s ease",
      }}
    >
      <span
        style={{
          color: isThinking && !inputFocused ? PALETTE.textDim : PALETTE.pink ?? "#f48fb1",
          fontWeight: 700,
        }}
      >
        ❯
      </span>
      {inputFocused ? (
        <>
          <span style={{ color: PALETTE.text, whiteSpace: "pre" }}>{inputText}</span>
          <span
            aria-hidden
            style={{
              display: "inline-block",
              width: "0.55ch",
              height: "1em",
              background: PALETTE.text,
              animation: "fleet-blink 1.05s steps(2,start) infinite",
            }}
          />
        </>
      ) : (
        <span style={{ color: PALETTE.textDim, fontStyle: "italic", fontSize: "0.92em" }}>
          {isThinking ? "(Claude is working…)" : "click to type a prompt"}
        </span>
      )}
    </div>
  );
}

function ClaudeFooter() {
  return (
    <div
      style={{
        padding: "0.4rem 0.85rem",
        background: PALETTE.bg,
        borderTop: `1px dashed ${PALETTE.border}`,
        display: "flex",
        alignItems: "center",
        gap: "0.5ch",
        fontSize: "0.72rem",
        color: PALETTE.textDim,
      }}
    >
      <span style={{ color: PALETTE.orange, fontWeight: 700 }}>⏵⏵</span>
      <span style={{ color: PALETTE.text }}>auto mode on</span>
      <span style={{ color: PALETTE.textDim }}>(shift+tab to cycle)</span>
    </div>
  );
}

function Rule() {
  return (
    <div
      aria-hidden
      style={{
        height: 1,
        background: PALETTE.border,
        opacity: 0.6,
      }}
    />
  );
}

const kbdInline: React.CSSProperties = {
  background: PALETTE.surfaceElev,
  color: PALETTE.text,
  padding: "0 0.4ch",
  borderRadius: 3,
  fontWeight: 700,
};

