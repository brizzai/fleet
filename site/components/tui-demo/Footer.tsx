"use client";

import { PALETTE } from "./palette";

const KEYS: Array<{ key: string; label: string }> = [
  { key: "↑↓", label: "Nav" },
  { key: "⏎", label: "Open / Approve" },
  { key: "␣", label: "Next" },
  { key: "a", label: "New" },
  { key: "d", label: "Del" },
  { key: "/", label: "Filter" },
  { key: "?", label: "Help" },
  { key: "⌃C", label: "Quit" },
];

export function Footer({ focused }: { focused: boolean }) {
  return (
    <div
      style={{
        borderTop: `1px solid ${PALETTE.border}`,
        background: PALETTE.bg,
        padding: "0.35rem 0.75rem",
        display: "flex",
        gap: "0.5ch",
        flexWrap: "wrap",
        fontFamily: "var(--font-mono)",
        fontSize: "0.72rem",
        alignItems: "center",
        color: PALETTE.textDim,
        opacity: focused ? 1 : 0.5,
        transition: "opacity 0.25s ease",
      }}
    >
      {KEYS.map((k, i) => (
        <span key={k.key} style={{ display: "inline-flex", alignItems: "center", gap: "0.4ch" }}>
          <kbd
            style={{
              background: PALETTE.accent,
              color: PALETTE.bg,
              padding: "0 0.55ch",
              borderRadius: 3,
              fontWeight: 700,
              fontSize: "0.72rem",
            }}
          >
            {k.key}
          </kbd>
          <span>{k.label}</span>
          {i < KEYS.length - 1 && (
            <span style={{ color: PALETTE.border, marginLeft: "0.5ch" }}>│</span>
          )}
        </span>
      ))}
    </div>
  );
}
