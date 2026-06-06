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
import { CoachBanner } from "./CoachBanner";
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
const SCRIPT_INTERVAL_MS = 1364;

// On narrow/touch screens the side-by-side cockpit can't fit at full size, so
// we render it at this fixed design width and CSS-scale the whole thing down to
// the available width — a faithful, compact mini-cockpit instead of a clipped one.
const DESIGN_WIDTH = 640;

export function TuiDemo() {
  const [state, dispatch] = useReducer(reducer, INITIAL_STATE);
  const [spinnerFrame, setSpinnerFrame] = useState(0);
  const containerRef = useRef<HTMLDivElement>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const stateRef = useRef<DemoState>(state);
  stateRef.current = state;
  // Mirrors `autoOnly` for the script ticker (which has a stable empty-deps
  // closure and can't read the latest state value directly).
  const autoOnlyRef = useRef(false);

  // Spinner ticker
  useEffect(() => {
    const id = setInterval(
      () => setSpinnerFrame((f) => (f + 1) % 1_000_000),
      SPINNER_INTERVAL_MS,
    );
    return () => clearInterval(id);
  }, []);

  // Script ticker — auto-play idle behavior. Uses jittered setTimeout chain
  // (instead of setInterval) so events don't fire on a perfect metronome,
  // which prevents the demo from looking like everything changes in lockstep.
  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = () => {
      if (cancelled) return;
      const now = Date.now();
      if (!isScriptPaused(stateRef.current, now)) {
        const ev = nextScriptEvent(stateRef.current, autoOnlyRef.current);
        if (ev) dispatch({ type: "script_event", event: ev });
      }
      // ±35% jitter around SCRIPT_INTERVAL_MS
      const next = SCRIPT_INTERVAL_MS * (0.65 + Math.random() * 0.7);
      timer = setTimeout(tick, next);
    };
    timer = setTimeout(tick, SCRIPT_INTERVAL_MS);
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
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
  autoOnlyRef.current = autoOnly;
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 720px), (pointer: coarse)");
    const apply = () => setAutoOnly(mq.matches);
    apply();
    mq.addEventListener("change", apply);
    return () => mq.removeEventListener("change", apply);
  }, []);

  // Scale-to-fit: in auto-only mode, shrink the fixed-width cockpit so it always
  // fits the available width (no horizontal clipping). Desktop renders untouched.
  const [scale, setScale] = useState(1);
  const [fitHeight, setFitHeight] = useState<number | null>(null);
  useEffect(() => {
    if (!autoOnly) {
      setScale(1);
      setFitHeight(null);
      return;
    }
    const measure = () => {
      const avail = wrapRef.current?.clientWidth ?? DESIGN_WIDTH;
      const s = Math.min(1, avail / DESIGN_WIDTH);
      const h = cardRef.current?.offsetHeight ?? 0;
      setScale(s);
      setFitHeight(h > 0 ? h * s : null);
    };
    measure();
    const ro = new ResizeObserver(measure);
    if (wrapRef.current) ro.observe(wrapRef.current);
    if (cardRef.current) ro.observe(cardRef.current);
    return () => ro.disconnect();
  }, [autoOnly]);

  const handleFocus = useCallback(() => {
    if (autoOnly) return;
    dispatch({ type: "set_focused", value: true });
    containerRef.current?.focus();
  }, [autoOnly]);

  const handleContainerClick = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      if (autoOnly) return;
      containerRef.current?.focus();
      const target = e.target as HTMLElement;
      // If the click landed outside the Claude prompt input area, exit input mode.
      if (!target.closest("[data-fleet-input]")) {
        if (stateRef.current.inputFocused) {
          dispatch({ type: "blur_input" });
        }
      }
    },
    [autoOnly],
  );

  const handleBlur = useCallback(() => {
    dispatch({ type: "set_focused", value: false });
  }, []);

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLDivElement>) => {
      if (autoOnly) return;
      const s = stateRef.current;

      // Input mode: typing the Claude prompt. Enter sends, Esc exits.
      if (s.inputFocused) {
        if (e.key === "Escape") {
          e.preventDefault();
          dispatch({ type: "blur_input" });
          return;
        }
        if (e.key === "Enter") {
          e.preventDefault();
          dispatch({ type: "input_submit" });
          return;
        }
        if (e.key === "Backspace") {
          e.preventDefault();
          dispatch({ type: "input_change", value: s.inputText.slice(0, -1) });
          return;
        }
        if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
          e.preventDefault();
          dispatch({ type: "input_change", value: s.inputText + e.key });
          return;
        }
        return;
      }

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
    if (!state.focused) return "click here, then press SPACE";
    return null;
  }, [autoOnly, state.focused]);

  return (
    <div
      ref={wrapRef}
      style={{
        position: "relative",
        margin: "clamp(2.5rem, 9vw, 5rem) auto 0",
        maxWidth: 1100,
        overflow: autoOnly ? "hidden" : "visible",
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
      {/* Fit container: collapses the layout box to the scaled height on mobile so
          the transformed (visually-smaller) cockpit leaves no empty space below. */}
      <div
        style={
          autoOnly
            ? { height: fitHeight ?? undefined, overflow: "hidden" }
            : undefined
        }
      >
      <div
        ref={cardRef}
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
          ...(autoOnly
            ? {
                width: DESIGN_WIDTH,
                marginInline: "auto",
                transform: scale < 1 ? `scale(${scale})` : undefined,
                transformOrigin: "top left",
              }
            : {}),
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
        onClick={handleContainerClick}
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
        <Preview state={state} spinnerFrame={spinnerFrame} dispatch={dispatch} />
      </div>

      <Footer focused={state.focused && !autoOnly} />
      </div>
      </div>

      {!autoOnly && <CoachBanner step={state.coachStep} />}

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
    </div>
  );
}
