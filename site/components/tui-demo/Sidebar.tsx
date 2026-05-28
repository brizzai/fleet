"use client";

import { PALETTE } from "./palette";
import { RepoHeader } from "./RepoHeader";
import { SessionLine } from "./SessionLine";
import type { DemoState } from "./state";

interface Props {
  state: DemoState;
  spinnerFrame: number;
}

export function Sidebar({ state, spinnerFrame }: Props) {
  const filter = state.filter.toLowerCase();
  return (
    <div
      role="tree"
      aria-label="fleet sessions"
      style={{
        borderRight: `1px solid ${PALETTE.border}`,
        backgroundColor: PALETTE.bg,
        height: "100%",
        fontFamily: "var(--font-mono)",
        fontSize: "0.85rem",
        display: "flex",
        flexDirection: "column",
        minHeight: 0,
      }}
    >
      <div
        style={{
          color: PALETTE.blue,
          padding: "0.75rem 0.85rem 0.4rem",
          textTransform: "uppercase",
          letterSpacing: "0.08em",
          fontSize: "0.72rem",
          borderBottom: `1px solid ${PALETTE.border}`,
        }}
      >
        Sessions
      </div>
      <div
        style={{
          flex: 1,
          minHeight: 0,
          overflowY: "auto",
          padding: "0.4rem 0",
        }}
      >
      {state.repos.map((repo, repoIdx) => {
        const headerSelected =
          state.cursor.repoIdx === repoIdx && state.cursor.sessionIdx === null;
        return (
          <div key={repo.id}>
            <RepoHeader repo={repo} selected={headerSelected} />
            {!repo.collapsed &&
              repo.sessions
                .map((session, sessionIdx) => ({ session, sessionIdx }))
                .filter(({ session }) =>
                  filter ? session.name.toLowerCase().includes(filter) : true,
                )
                .map(({ session, sessionIdx }, _idx, arr) => {
                  const isLast = sessionIdx === arr[arr.length - 1]!.sessionIdx;
                  const selected =
                    state.cursor.repoIdx === repoIdx &&
                    state.cursor.sessionIdx === sessionIdx;
                  return (
                    <SessionLine
                      key={session.id}
                      session={session}
                      isLast={isLast}
                      selected={selected}
                      spinnerFrame={spinnerFrame}
                    />
                  );
                })}
            {repo.sessions.length === 0 && (
              <div
                style={{
                  padding: "0 0.6rem 0 3.5ch",
                  color: PALETTE.textDim,
                  fontStyle: "italic",
                  fontSize: "0.78rem",
                }}
              >
                (empty)
              </div>
            )}
          </div>
        );
      })}
      {state.filterMode && (
        <div
          style={{
            position: "sticky",
            bottom: 0,
            padding: "0.4rem 0.85rem",
            background: PALETTE.surface,
            borderTop: `1px solid ${PALETTE.border}`,
            color: PALETTE.text,
            fontSize: "0.8rem",
          }}
        >
          / {state.filter}
          <span style={{ animation: "fleet-blink 1.05s steps(2,start) infinite" }}>
            ▋
          </span>
        </div>
      )}
      </div>
    </div>
  );
}
