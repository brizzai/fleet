"use client";

import { PALETTE } from "./palette";

/**
 * Prominent "press SPACE to jump" callout shown at the bottom of the sidebar.
 * Space is the killer feature — cycles to the next waiting/finished session in
 * one keystroke. The hint lives outside the rotating tip carousel because we
 * want every visitor to discover it.
 */
export function Hint() {
  return (
    <div
      aria-live="polite"
      style={{
        borderTop: `1px solid ${PALETTE.border}`,
        background: `linear-gradient(180deg, ${PALETTE.bg} 0%, rgba(244,143,177,0.06) 100%)`,
        padding: "0.75rem 0.9rem 0.85rem",
        display: "flex",
        alignItems: "center",
        gap: "0.7rem",
        fontFamily: "var(--font-mono)",
        position: "relative",
      }}
    >
      <kbd
        style={{
          flex: "0 0 auto",
          display: "inline-flex",
          alignItems: "center",
          justifyContent: "center",
          padding: "0.55rem 0.95rem",
          borderRadius: 6,
          background: "#f48fb1",
          color: PALETTE.bg,
          fontWeight: 800,
          fontSize: "0.78rem",
          letterSpacing: "0.12em",
          boxShadow:
            "0 0 0 1px rgba(244,143,177,0.4), 0 0 24px -2px rgba(244,143,177,0.55)",
          animation: "fleet-space-pulse 2.2s ease-in-out infinite",
        }}
      >
        SPACE
      </kbd>
      <span
        style={{
          color: PALETTE.text,
          fontWeight: 700,
          fontSize: "0.92rem",
        }}
      >
        jump to the next{" "}
        <span
          aria-hidden
          style={{
            color: PALETTE.yellow,
            textShadow: `0 0 10px ${PALETTE.yellow}88`,
          }}
        >
          ◐
        </span>
      </span>

      <style>{`
        @keyframes fleet-space-pulse {
          0%, 100% {
            box-shadow:
              0 0 0 1px rgba(244,143,177,0.4),
              0 0 24px -2px rgba(244,143,177,0.55);
            transform: translateY(0);
          }
          50% {
            box-shadow:
              0 0 0 1px rgba(244,143,177,0.6),
              0 0 32px -2px rgba(244,143,177,0.75);
            transform: translateY(-1px);
          }
        }
      `}</style>
    </div>
  );
}
