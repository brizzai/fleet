"use client";

import { BRANCH_ICON, STATUS_ICON, TREE, PR_ICON } from "./glyphs";
import { PALETTE } from "./palette";
import type { Repo } from "./state";

interface Props {
  repo: Repo;
  selected: boolean;
}

const PR_COLOR: Record<string, string> = {
  approved: PALETTE.green,
  pending: PALETTE.yellow,
  failing: PALETTE.red,
  merged: PALETTE.purple,
};

export function RepoHeader({ repo, selected }: Props) {
  const counts = repo.sessions.reduce(
    (acc, s) => {
      if (s.status === "running" || s.status === "starting") acc.running += 1;
      if (s.status === "waiting") acc.waiting += 1;
      if (s.status === "error") acc.error += 1;
      return acc;
    },
    { running: 0, waiting: 0, error: 0 },
  );

  const fg = selected ? PALETTE.bg : PALETTE.text;
  return (
    <div
      role="treeitem"
      aria-selected={selected}
      className="tui-row"
      style={{
        backgroundColor: selected ? PALETTE.accent : "transparent",
        color: fg,
        padding: "0 0.6rem",
        whiteSpace: "pre",
        display: "flex",
        alignItems: "center",
        gap: "0.6ch",
        lineHeight: 1.55,
        fontWeight: 600,
      }}
    >
      <span style={{ width: "1ch" }}>{selected ? TREE.cursor : " "}</span>
      <span style={{ color: selected ? PALETTE.bg : PALETTE.text }}>
        {repo.collapsed ? TREE.collapsed : TREE.expanded}
      </span>
      <span>{repo.name}</span>
      <span style={{ color: selected ? PALETTE.bg : PALETTE.blue, marginLeft: "1ch" }}>
        {BRANCH_ICON ? `${BRANCH_ICON} ` : ""}
        {repo.branch}
      </span>
      {repo.dirty && (
        <span style={{ color: selected ? PALETTE.bg : PALETTE.yellow, fontWeight: 700 }}>
          *
        </span>
      )}
      {repo.sessions.length > 0 && (
        <span style={{ color: selected ? PALETTE.bg : PALETTE.textDim }}>
          ({repo.sessions.length})
        </span>
      )}
      {counts.running > 0 && (
        <span
          style={{
            color: selected ? PALETTE.bg : PALETTE.green,
            fontWeight: 700,
            marginLeft: "0.5ch",
          }}
        >
          {STATUS_ICON.running} {counts.running}
        </span>
      )}
      {counts.waiting > 0 && (
        <span style={{ color: selected ? PALETTE.bg : PALETTE.yellow, fontWeight: 700 }}>
          {STATUS_ICON.waiting} {counts.waiting}
        </span>
      )}
      {counts.error > 0 && (
        <span style={{ color: selected ? PALETTE.bg : PALETTE.red, fontWeight: 700 }}>
          {STATUS_ICON.error} {counts.error}
        </span>
      )}
      {repo.pr && (
        <span
          style={{
            color: selected ? PALETTE.bg : PR_COLOR[repo.pr.state] ?? PALETTE.textDim,
            marginLeft: "auto",
            fontWeight: 700,
          }}
        >
          #{repo.pr.number}{" "}
          {repo.pr.state === "approved"
            ? PR_ICON.approved
            : repo.pr.state === "failing"
              ? PR_ICON.failing
              : repo.pr.state === "merged"
                ? PR_ICON.merged
                : PR_ICON.pending}
        </span>
      )}
    </div>
  );
}
