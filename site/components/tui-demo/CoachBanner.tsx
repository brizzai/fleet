"use client";

import { useEffect, useState } from "react";
import { PALETTE } from "./palette";
import type { CoachStep } from "./state";

/**
 * Big state-aware coaching banner under the demo.
 *
 * The headline interaction is `Space → Enter` (jump to the next ◐, then
 * approve). Visitors don't discover it from the keybind footer alone, so we
 * walk them through it in two beats, then fade away.
 */
export function CoachBanner({ step }: { step: CoachStep }) {
  // "shown" → "fading" (after holding `done` ~8s) → "gone" (unmounted, so the
  // retired banner leaves no permanent empty gap under the demo).
  const [phase, setPhase] = useState<"shown" | "fading" | "gone">("shown");

  useEffect(() => {
    if (step !== "done") {
      setPhase("shown");
      return;
    }
    const id = setTimeout(() => setPhase("fading"), 8_000);
    return () => clearTimeout(id);
  }, [step]);

  if (phase === "gone") return null;

  const fading = phase === "fading";
  return (
    <div
      aria-live="polite"
      onTransitionEnd={() => {
        if (fading) setPhase("gone");
      }}
      style={{
        margin: "1.5rem auto 0",
        maxWidth: 720,
        padding: "0 1rem",
        textAlign: "center",
        opacity: fading ? 0 : 1,
        transform: fading ? "translateY(-4px)" : "translateY(0)",
        transition: "opacity 0.5s ease, transform 0.5s ease",
        pointerEvents: fading ? "none" : undefined,
      }}
    >
      <BannerPanel step={step} />
      <style>{`
        @keyframes fleet-coach-pulse {
          0%, 100% {
            box-shadow:
              0 0 0 1px rgba(244,143,177,0.18),
              0 0 32px -6px rgba(244,143,177,0.45);
          }
          50% {
            box-shadow:
              0 0 0 1px rgba(244,143,177,0.35),
              0 0 44px -4px rgba(244,143,177,0.65);
          }
        }
        @keyframes fleet-key-press {
          0%, 88%, 100% { transform: translateY(0); }
          94% { transform: translateY(2px); }
        }
      `}</style>
    </div>
  );
}

function BannerPanel({ step }: { step: CoachStep }) {
  if (step === "intro") {
    return (
      <Panel
        eyebrow="how to play"
        body={
          <>
            click the demo above, then{" "}
            <KbdInline color={PALETTE.pink}>SPACE</KbdInline> to jump to the next{" "}
            <Glyph color={PALETTE.yellow}>◐</Glyph>
          </>
        }
      />
    );
  }

  if (step === "space") {
    return (
      <Panel
        eyebrow="step 1 of 2"
        emphasize
        body={
          <>
            press <Kbd color={PALETTE.pink}>SPACE</Kbd> to jump to the next{" "}
            <Glyph color={PALETTE.yellow}>◐</Glyph>
          </>
        }
      />
    );
  }

  if (step === "enter") {
    return (
      <Panel
        eyebrow="step 2 of 2"
        emphasize
        body={
          <>
            now press <Kbd color={PALETTE.pink}>ENTER</Kbd> to approve it
          </>
        }
      />
    );
  }

  // done
  return (
    <Panel
      eyebrow="that's the loop"
      body={
        <>
          repeat <KbdInline color={PALETTE.pink}>SPACE</KbdInline> →{" "}
          <KbdInline color={PALETTE.pink}>ENTER</KbdInline> through every waiting
          agent · try <KbdInline color={PALETTE.pink}>a</KbdInline> to spawn one
        </>
      }
    />
  );
}

function Panel({
  eyebrow,
  body,
  emphasize,
}: {
  eyebrow: string;
  body: React.ReactNode;
  emphasize?: boolean;
}) {
  return (
    <div
      style={{
        display: "inline-flex",
        flexDirection: "column",
        alignItems: "center",
        gap: "0.4rem",
        padding: "0.9rem 1.4rem",
        borderRadius: 14,
        border: `1px solid ${emphasize ? "rgba(244,143,177,0.55)" : "rgba(244,143,177,0.25)"}`,
        background: emphasize
          ? "linear-gradient(180deg, rgba(244,143,177,0.10) 0%, rgba(160,106,254,0.05) 100%)"
          : "rgba(20,22,31,0.6)",
        boxShadow: emphasize
          ? "0 0 0 1px rgba(244,143,177,0.18), 0 0 32px -6px rgba(244,143,177,0.45)"
          : "0 0 0 1px rgba(35,38,52,0.6)",
        animation: emphasize
          ? "fleet-coach-pulse 2.6s ease-in-out infinite"
          : undefined,
        fontFamily: "var(--font-mono)",
      }}
    >
      <span
        aria-hidden
        style={{
          color: PALETTE.pink,
          fontSize: "0.68rem",
          letterSpacing: "0.18em",
          textTransform: "uppercase",
        }}
      >
        ↑ {eyebrow}
      </span>
      <div
        style={{
          color: "var(--charm-text)",
          fontSize: "clamp(0.95rem, 1.8vw, 1.15rem)",
          lineHeight: 1.4,
          fontFamily: "var(--font-mono)",
          display: "flex",
          alignItems: "center",
          gap: "0.4ch",
          flexWrap: "wrap",
          justifyContent: "center",
        }}
      >
        {body}
      </div>
    </div>
  );
}

function Kbd({
  children,
  color,
}: {
  children: React.ReactNode;
  color: string;
}) {
  return (
    <kbd
      style={{
        display: "inline-flex",
        alignItems: "center",
        padding: "0.3rem 0.7rem",
        borderRadius: 6,
        background: color,
        color: PALETTE.bg,
        fontFamily: "var(--font-mono)",
        fontWeight: 800,
        fontSize: "0.88rem",
        letterSpacing: "0.1em",
        boxShadow: `0 2px 0 0 rgba(0,0,0,0.55), 0 0 18px -4px ${color}`,
        animation: "fleet-key-press 3.5s ease-in-out infinite",
      }}
    >
      {children}
    </kbd>
  );
}

function KbdInline({
  children,
  color,
}: {
  children: React.ReactNode;
  color: string;
}) {
  return (
    <kbd
      style={{
        display: "inline-block",
        padding: "0.05rem 0.5ch",
        borderRadius: 4,
        background: "rgba(244,143,177,0.18)",
        border: `1px solid ${color}`,
        color,
        fontFamily: "var(--font-mono)",
        fontWeight: 700,
        fontSize: "0.85em",
      }}
    >
      {children}
    </kbd>
  );
}

function Glyph({
  children,
  color,
}: {
  children: React.ReactNode;
  color: string;
}) {
  return (
    <span
      style={{
        color,
        textShadow: `0 0 10px ${color}88`,
        fontWeight: 700,
      }}
    >
      {children}
    </span>
  );
}
