"use client";

import { useEffect, useRef } from "react";

const FEATURES = [
  {
    icon: "●",
    title: "Real-time status",
    body:
      "Hook-based detection — no polling, no delay. running · waiting · finished · idle · error.",
  },
  {
    icon: "␣",
    title: "Jump + approve",
    body:
      "Space jumps to the next session that needs you. Y approves a prompt without attaching.",
  },
  {
    icon: "⎇",
    title: "Git-native sessions",
    body:
      "Sessions live under their repo. Branch, dirty state, full PR status — CI, reviews, threads.",
  },
  {
    icon: "⌥",
    title: "Worktrees, zero config",
    body:
      "w creates a worktree with a branch picker. Each worktree gets its own isolated session.",
  },
  {
    icon: "⑃",
    title: "Fork conversations",
    body:
      "f forks a session — branch the Claude conversation, try a different path, keep both alive.",
  },
  {
    icon: "↺",
    title: "Session resume",
    body:
      "Restart with r — Claude picks up exactly where it left off via session resume.",
  },
];

export function Features() {
  const gridRef = useRef<HTMLUListElement>(null);

  useEffect(() => {
    const grid = gridRef.current;
    if (!grid) return;
    const cards = Array.from(grid.querySelectorAll<HTMLLIElement>("li"));
    if (!("IntersectionObserver" in window)) {
      cards.forEach((c) => c.classList.add("is-in"));
      return;
    }
    const io = new IntersectionObserver(
      (entries) => {
        entries.forEach((e) => {
          if (e.isIntersecting) {
            (e.target as HTMLLIElement).classList.add("is-in");
            io.unobserve(e.target);
          }
        });
      },
      { threshold: 0.15, rootMargin: "0px 0px -8% 0px" },
    );
    cards.forEach((c) => io.observe(c));
    return () => io.disconnect();
  }, []);

  return (
    <section
      style={{
        maxWidth: 1100,
        margin: "clamp(3rem, 9vw, 5rem) auto clamp(2.5rem, 7vw, 4rem)",
        padding: "0 1.25rem",
      }}
    >
      <header style={{ textAlign: "center", marginBottom: "2.5rem" }}>
        <span
          style={{
            fontFamily: "var(--font-mono)",
            fontSize: "0.8rem",
            letterSpacing: "0.1em",
            textTransform: "uppercase",
            color: "var(--charm-pink)",
          }}
        >
          Why fleet
        </span>
        <h2
          className="font-mono"
          style={{
            fontSize: "clamp(1.6rem, 2.8vw, 2.1rem)",
            margin: "0.5rem 0 0.75rem",
            color: "var(--charm-text)",
          }}
        >
          A new kind of IDE —{" "}
          <span style={{ color: "var(--charm-pink)" }}>for when AI does the typing.</span>
        </h2>
        <p
          style={{
            maxWidth: "62ch",
            margin: "0 auto",
            color: "var(--charm-text-dim)",
            lineHeight: 1.55,
          }}
        >
          Cursor sits you in front of one file. fleet sits you in front of ten
          agents. Real-time status, one-key approve, PR-aware, git-native —
          everything you need to direct a team of AI coders without losing the plot.
        </p>
      </header>

      <ul
        ref={gridRef}
        role="list"
        style={{
          listStyle: "none",
          padding: 0,
          margin: 0,
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(260px, 1fr))",
          gap: "1rem",
        }}
      >
        {FEATURES.map((f, i) => (
          <li
            key={f.title}
            className="fleet-feature-card"
            style={{
              transitionDelay: `${i * 70}ms`,
              padding: "1.25rem",
              borderRadius: 10,
              background: "var(--charm-surface)",
              border: "1px solid var(--charm-border)",
              transition:
                "opacity 0.55s cubic-bezier(0.2,0.65,0.3,1), transform 0.55s cubic-bezier(0.2,0.65,0.3,1), border-color 0.18s ease",
              opacity: 0,
              transform: "translateY(14px)",
            }}
          >
            <span
              aria-hidden
              style={{
                display: "inline-flex",
                alignItems: "center",
                justifyContent: "center",
                width: 32,
                height: 32,
                borderRadius: 6,
                background: "rgba(244,143,177,0.14)",
                color: "var(--charm-pink)",
                fontFamily: "var(--font-mono)",
                fontSize: "1.1rem",
                marginBottom: "0.75rem",
              }}
            >
              {f.icon}
            </span>
            <h3
              className="font-mono"
              style={{
                fontSize: "1rem",
                margin: "0 0 0.4rem",
                color: "var(--charm-text)",
              }}
            >
              {f.title}
            </h3>
            <p
              style={{
                margin: 0,
                fontSize: "0.92rem",
                lineHeight: 1.5,
                color: "var(--charm-text-dim)",
              }}
            >
              {f.body}
            </p>
          </li>
        ))}
      </ul>

      <style>{`
        .fleet-feature-card.is-in {
          opacity: 1 !important;
          transform: translateY(0) !important;
        }
        .fleet-feature-card:hover {
          border-color: var(--charm-pink) !important;
        }
      `}</style>
    </section>
  );
}
