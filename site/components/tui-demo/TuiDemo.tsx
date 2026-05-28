"use client";

import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
} from "react";
import { Sidebar } from "./Sidebar";
import { Preview } from "./Preview";
import { Footer } from "./Footer";
import { PALETTE } from "./palette";
import { SPINNER_INTERVAL_MS } from "./glyphs";
import {
  INITIAL_STATE,
  isScriptPaused,
  reducer,
  type DemoState,
} from "./state";
import { nextScriptEvent, startingToRunningEvent } from "./script";

const TICK_MS = 100;
const SCRIPT_INTERVAL_MS = 1500;

export function TuiDemo() {
  const [state, dispatch] = useReducer(reducer, INITIAL_STATE);
  const [spinnerFrame, setSpinnerFrame] = useState(0);
  const containerRef = useRef<HTMLDivElement>(null);
  const stateRef = useRef<DemoState>(state);
  stateRef.current = state;

  // Spinner ticker
  useEffect(() => {
    const id = setInterval(
      () => setSpinnerFrame((f) => (f + 1) % 1_000_000),
      SPINNER_INTERVAL_MS,
    );
    return () => clearInterval(id);
  }, []);

  // Script ticker — auto-play idle behavior
  useEffect(() => {
    const id = setInterval(() => {
      const now = Date.now();
      if (isScriptPaused(stateRef.current, now)) return;
      const ev = nextScriptEvent(stateRef.current);
      if (ev) dispatch({ type: "script_event", event: ev });
    }, SCRIPT_INTERVAL_MS);
    return () => clearInterval(id);
  }, []);

  // Promote any `starting` sessions to `running` after ~1.6s
  useEffect(() => {
    const startingSessions = state.repos.flatMap((r) =>
      r.sessions.filter((s) => s.status === "starting").map((s) => s.id),
    );
    if (startingSessions.length === 0) return;
    const timers = startingSessions.map((id) =>
      setTimeout(
        () => dispatch({ type: "script_event", event: startingToRunningEvent(id) }),
        1600,
      ),
    );
    return () => timers.forEach((t) => clearTimeout(t));
  }, [state.repos]);

  // tick — used to refresh memoized things if needed
  useEffect(() => {
    const id = setInterval(() => dispatch({ type: "tick", now: Date.now() }), TICK_MS * 10);
    return () => clearInterval(id);
  }, []);

  // Detect touch / narrow viewport: auto-play only mode
  const [autoOnly, setAutoOnly] = useState(false);
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 720px), (pointer: coarse)");
    const apply = () => setAutoOnly(mq.matches);
    apply();
    mq.addEventListener("change", apply);
    return () => mq.removeEventListener("change", apply);
  }, []);

  const handleFocus = useCallback(() => {
    if (autoOnly) return;
    dispatch({ type: "set_focused", value: true });
    containerRef.current?.focus();
  }, [autoOnly]);

  const handleBlur = useCallback(() => {
    dispatch({ type: "set_focused", value: false });
  }, []);

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLDivElement>) => {
      if (autoOnly) return;
      const s = stateRef.current;

      // Filter mode captures everything
      if (s.filterMode) {
        if (e.key === "Escape" || e.key === "Enter") {
          e.preventDefault();
          dispatch({ type: "filter_end" });
          return;
        }
        if (e.key === "Backspace") {
          e.preventDefault();
          dispatch({ type: "filter_input", value: s.filter.slice(0, -1) });
          return;
        }
        if (e.key.length === 1) {
          e.preventDefault();
          dispatch({ type: "filter_input", value: s.filter + e.key });
          return;
        }
        return;
      }

      const key = e.key;

      // Don't hijack modified shortcuts
      if (e.ctrlKey || e.metaKey || e.altKey) return;

      switch (key) {
        case "ArrowUp":
        case "k":
          e.preventDefault();
          dispatch({ type: "cursor_up" });
          break;
        case "ArrowDown":
        case "j":
          e.preventDefault();
          dispatch({ type: "cursor_down" });
          break;
        case "Enter":
          e.preventDefault();
          dispatch({ type: "enter" });
          break;
        case " ":
          e.preventDefault();
          dispatch({ type: "jump_attention" });
          break;
        case "a":
          e.preventDefault();
          dispatch({ type: "add_session" });
          break;
        case "d":
          e.preventDefault();
          dispatch({ type: "delete" });
          break;
        case "/":
          e.preventDefault();
          dispatch({ type: "filter_start" });
          break;
        default:
          break;
      }
    },
    [autoOnly],
  );

  const hint = useMemo(() => {
    if (autoOnly) return "auto-playing — drive on desktop!";
    if (!state.focused) return "click here to drive!";
    return null;
  }, [autoOnly, state.focused]);

  return (
    <div
      style={{
        position: "relative",
        margin: "5rem auto 0",
        maxWidth: 1100,
        overflow: "visible",
      }}
    >
      {/* Focus aura — large blurred pink+purple glow behind the demo */}
      <div
        aria-hidden
        style={{
          position: "absolute",
          inset: -32,
          zIndex: 0,
          pointerEvents: "none",
          borderRadius: 28,
          background:
            "radial-gradient(60% 50% at 50% 50%, rgba(244,143,177,0.45), transparent 70%), radial-gradient(70% 60% at 80% 30%, rgba(160,106,254,0.30), transparent 75%), radial-gradient(50% 40% at 20% 80%, rgba(244,143,177,0.25), transparent 75%)",
          filter: "blur(36px)",
          opacity: state.focused ? 1 : 0,
          transform: state.focused ? "scale(1.02)" : "scale(0.96)",
          transition: "opacity 0.45s ease, transform 0.6s ease",
        }}
      />
      {hint && <ComicHint hint={hint} onClick={handleFocus} dimmed={state.focused} />}
      <div
        style={{
          position: "relative",
          zIndex: 1,
          border: `1px solid ${
            state.focused ? "rgba(244,143,177,0.55)" : PALETTE.border
          }`,
          borderRadius: 12,
          overflow: "hidden",
          background: PALETTE.bg,
          boxShadow: state.focused
            ? "0 30px 80px -30px rgba(0,0,0,0.6), 0 0 80px -10px rgba(244,143,177,0.45), 0 0 140px -20px rgba(160,106,254,0.35)"
            : "0 30px 80px -30px rgba(0,0,0,0.6), 0 0 60px -20px rgba(244,143,177,0.25)",
          transition:
            "box-shadow 0.45s ease, border-color 0.45s ease",
        }}
      >
      {/* macOS-style chrome bar */}
      <div
        aria-hidden
        style={{
          display: "flex",
          alignItems: "center",
          gap: "0.4rem",
          padding: "0.55rem 0.8rem",
          background: PALETTE.surface,
          borderBottom: `1px solid ${PALETTE.border}`,
          fontFamily: "var(--font-mono)",
          fontSize: "0.72rem",
          color: PALETTE.textDim,
        }}
      >
        <span
          style={{
            width: 10,
            height: 10,
            borderRadius: "50%",
            background: "#ff5f57",
          }}
        />
        <span
          style={{
            width: 10,
            height: 10,
            borderRadius: "50%",
            background: "#febc2e",
          }}
        />
        <span
          style={{
            width: 10,
            height: 10,
            borderRadius: "50%",
            background: "#28c840",
          }}
        />
        <span style={{ marginLeft: "0.6rem" }}>fleet — ~/code</span>
      </div>

      <div
        ref={containerRef}
        role="application"
        aria-label="Interactive fleet TUI demo"
        tabIndex={0}
        onClick={handleFocus}
        onFocus={handleFocus}
        onBlur={handleBlur}
        onKeyDown={onKeyDown}
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(220px, 38%) 1fr",
          height: 460,
          outline: state.focused
            ? `2px solid ${PALETTE.accent}55`
            : "2px solid transparent",
          outlineOffset: -2,
          cursor: autoOnly ? "default" : state.focused ? "default" : "pointer",
        }}
      >
        <Sidebar state={state} spinnerFrame={spinnerFrame} />
        <Preview state={state} spinnerFrame={spinnerFrame} />
      </div>

      <Footer focused={state.focused && !autoOnly} />
      </div>

      <style>{`
        @keyframes fleet-blink {
          50% { opacity: 0; }
        }
        @keyframes fleet-comic-bob {
          0%, 100% { transform: rotate(-4deg) translateY(0); }
          50%      { transform: rotate(-4deg) translateY(-3px); }
        }
        .tui-row:hover {
          background: ${PALETTE.surface};
        }
      `}</style>
    </div>
  );
}

function ComicHint({
  hint,
  onClick,
  dimmed,
}: {
  hint: string;
  onClick: () => void;
  dimmed: boolean;
}) {
  return (
    <div
      aria-hidden
      onClick={onClick}
      className="fleet-comic-hint"
      style={{
        position: "absolute",
        top: -56,
        right: 16,
        zIndex: 4,
        display: "flex",
        flexDirection: "column",
        alignItems: "flex-end",
        gap: 4,
        pointerEvents: dimmed ? "none" : "auto",
        opacity: dimmed ? 0 : 1,
        transition: "opacity 0.3s ease",
        cursor: "pointer",
      }}
    >
      {/* Sticky note */}
      <div
        style={{
          padding: "0.5rem 0.95rem",
          background: "#f48fb1",
          color: "#1a0a14",
          fontFamily: "var(--font-mono)",
          fontSize: "0.85rem",
          fontWeight: 700,
          borderRadius: 6,
          boxShadow:
            "0 0 0 1px rgba(0,0,0,0.2), 0 14px 30px -10px rgba(244,143,177,0.55), 0 0 22px -2px rgba(244,143,177,0.45)",
          animation: "fleet-comic-bob 2.6s ease-in-out infinite",
          whiteSpace: "nowrap",
          letterSpacing: "0.01em",
          transformOrigin: "center bottom",
        }}
      >
        {hint}
      </div>

      {/* Curved arrow pointing down into the demo */}
      <svg
        width="86"
        height="68"
        viewBox="0 0 86 68"
        fill="none"
        style={{
          filter: "drop-shadow(0 2px 6px rgba(244,143,177,0.45))",
          marginRight: 26,
          marginTop: -2,
        }}
      >
        <defs>
          <marker
            id="fleet-arrow-head"
            viewBox="0 0 12 12"
            refX="9"
            refY="6"
            markerWidth="7"
            markerHeight="7"
            orient="auto-start-reverse"
          >
            <path d="M 0 0 L 12 6 L 0 12 z" fill="#f48fb1" />
          </marker>
        </defs>
        <path
          d="M76 4 C 70 24, 44 30, 22 58"
          fill="none"
          stroke="#f48fb1"
          strokeWidth="2.6"
          strokeLinecap="round"
          markerEnd="url(#fleet-arrow-head)"
        />
      </svg>
    </div>
  );
}
