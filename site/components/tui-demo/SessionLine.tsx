"use client";

import { STATUS_ICON, TREE, SPINNER_FRAMES } from "./glyphs";
import { PALETTE, statusColor } from "./palette";
import type { Session } from "./state";

interface Props {
  session: Session;
  isLast: boolean;
  selected: boolean;
  spinnerFrame: number;
}

export function SessionLine({ session, isLast, selected, spinnerFrame }: Props) {
  const connector = isLast ? TREE.last : TREE.branch;
  const icon =
    session.status === "starting"
      ? SPINNER_FRAMES[spinnerFrame % SPINNER_FRAMES.length]
      : STATUS_ICON[session.status];
  const iconColor =
    session.status === "starting" ? PALETTE.accent : statusColor(session.status);

  const titleStyle: React.CSSProperties = {
    color:
      session.status === "running" || session.status === "waiting"
        ? PALETTE.text
        : PALETTE.textDim,
    fontWeight:
      session.status === "running" || session.status === "waiting" ? 600 : 400,
    textDecoration: session.status === "error" ? "underline" : undefined,
  };

  return (
    <div
      role="treeitem"
      aria-selected={selected}
      className="tui-row"
      style={{
        backgroundColor: selected ? PALETTE.accent : "transparent",
        color: selected ? PALETTE.bg : undefined,
        padding: "0 0.6rem",
        whiteSpace: "pre",
        display: "flex",
        alignItems: "center",
        gap: "0.5ch",
        lineHeight: 1.55,
      }}
    >
      <span
        style={{
          color: selected ? PALETTE.bg : PALETTE.accent,
          fontWeight: 700,
          width: "1ch",
          display: "inline-block",
        }}
      >
        {selected ? TREE.cursor : " "}
      </span>
      <span style={{ color: selected ? PALETTE.bg : PALETTE.border }}>{connector}</span>
      <span
        style={{
          color: selected ? PALETTE.bg : iconColor,
          fontWeight: 700,
          minWidth: "1ch",
          textAlign: "center",
          textShadow:
            !selected && (session.status === "running" || session.status === "waiting")
              ? `0 0 10px ${iconColor}66`
              : undefined,
        }}
      >
        {icon}
      </span>
      <span style={selected ? { color: PALETTE.bg, fontWeight: 600 } : titleStyle}>
        {session.name}
      </span>
      {session.slot !== undefined && (
        <span
          style={{
            color: selected ? PALETTE.bg : PALETTE.orange,
            fontWeight: 700,
            marginLeft: "0.4ch",
          }}
        >
          [{session.slot}]
        </span>
      )}
      {session.pendingApprove && !selected && (
        <span
          aria-hidden
          style={{
            color: PALETTE.yellow,
            marginLeft: "0.6ch",
            opacity: 0.9,
            fontSize: "0.85em",
          }}
        >
          (⏎)
        </span>
      )}
    </div>
  );
}
