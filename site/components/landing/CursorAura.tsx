"use client";

import { useEffect, useRef } from "react";

/**
 * Full-viewport pink+purple aura that follows the cursor. Sits at z-index -1
 * behind everything else, ignores pointer events, and respects reduced motion.
 * Mount this once at the top of a route (e.g. the home page).
 */
export function CursorAura() {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    const el = ref.current;
    if (!el) return;
    // Start at the top-center so the gradient is visible before any movement.
    el.style.setProperty("--mx", "50vw");
    el.style.setProperty("--my", "20vh");
    let raf = 0;
    let nextX = window.innerWidth / 2;
    let nextY = window.innerHeight * 0.2;
    const onMove = (e: PointerEvent) => {
      nextX = e.clientX;
      nextY = e.clientY;
      if (raf) return;
      raf = requestAnimationFrame(() => {
        el.style.setProperty("--mx", `${nextX}px`);
        el.style.setProperty("--my", `${nextY}px`);
        raf = 0;
      });
    };
    window.addEventListener("pointermove", onMove, { passive: true });
    return () => {
      window.removeEventListener("pointermove", onMove);
      if (raf) cancelAnimationFrame(raf);
    };
  }, []);

  return (
    <div
      ref={ref}
      aria-hidden
      style={{
        position: "fixed",
        inset: 0,
        zIndex: 9999,
        pointerEvents: "none",
        mixBlendMode: "screen",
        background:
          "radial-gradient(640px 480px at var(--mx, 50vw) var(--my, 20vh), rgba(244,143,177,0.12), transparent 70%), radial-gradient(900px 680px at var(--mx, 50vw) var(--my, 20vh), rgba(160,106,254,0.07), transparent 75%)",
      }}
    />
  );
}
