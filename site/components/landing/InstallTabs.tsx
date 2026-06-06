"use client";

import { useState } from "react";

interface Tab {
  id: string;
  label: string;
  cmd: string;
  note?: string;
}

const TABS: Tab[] = [
  {
    id: "brew",
    label: "Homebrew",
    cmd: "brew install brizzai/tap/fleet",
    note: "Recommended on macOS.",
  },
  {
    id: "curl",
    label: "Shell script",
    cmd: "curl -fsSL https://raw.githubusercontent.com/brizzai/fleet/master/install.sh | bash",
    note: "Requires gh CLI.",
  },
  {
    id: "go",
    label: "go install",
    cmd: "go install github.com/brizzai/fleet/cmd/fleet@latest",
    note: "Requires Go 1.26+.",
  },
];

export function InstallTabs() {
  const [active, setActive] = useState(TABS[0]!.id);
  const tab = TABS.find((t) => t.id === active) ?? TABS[0]!;

  return (
    <section
      style={{
        maxWidth: 760,
        margin: "clamp(3rem, 9vw, 5rem) auto clamp(3rem, 9vw, 5rem)",
        padding: "0 1.25rem",
        textAlign: "center",
      }}
    >
      <span
        style={{
          fontFamily: "var(--font-mono)",
          fontSize: "0.8rem",
          letterSpacing: "0.1em",
          textTransform: "uppercase",
          color: "var(--charm-pink)",
        }}
      >
        Install
      </span>
      <h2
        className="font-mono"
        style={{
          fontSize: "clamp(1.5rem, 2.6vw, 2rem)",
          margin: "0.5rem 0 1.5rem",
          color: "var(--charm-text)",
        }}
      >
        One line. Then <code style={{ color: "var(--charm-pink)" }}>fleet</code>.
      </h2>

      <div
        role="tablist"
        aria-label="Install method"
        style={{
          display: "inline-flex",
          gap: "0.25rem",
          padding: "0.25rem",
          borderRadius: 999,
          background: "var(--charm-surface)",
          border: "1px solid var(--charm-border)",
          marginBottom: "1rem",
        }}
      >
        {TABS.map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={t.id === active}
            onClick={() => setActive(t.id)}
            style={{
              padding: "0.45rem 1rem",
              borderRadius: 999,
              border: "none",
              background: t.id === active ? "var(--charm-pink)" : "transparent",
              color: t.id === active ? "#1a0a14" : "var(--charm-text-dim)",
              fontFamily: "var(--font-mono)",
              fontSize: "0.85rem",
              fontWeight: t.id === active ? 700 : 500,
              cursor: "pointer",
              transition: "all 0.15s ease",
            }}
          >
            {t.label}
          </button>
        ))}
      </div>

      <div
        style={{
          background: "var(--charm-surface)",
          border: "1px solid var(--charm-border)",
          borderRadius: 10,
          padding: "1rem 1.25rem",
          fontFamily: "var(--font-mono)",
          textAlign: "left",
          overflow: "hidden",
        }}
      >
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: "0.6rem",
            color: "var(--charm-text)",
            fontSize: "0.95rem",
            overflowX: "auto",
            whiteSpace: "nowrap",
          }}
        >
          <span style={{ color: "var(--charm-pink)", userSelect: "none" }}>$</span>
          <code style={{ background: "transparent", color: "var(--charm-text)" }}>
            {tab.cmd}
          </code>
        </div>
        {tab.note && (
          <div
            style={{
              color: "var(--charm-text-dim)",
              fontSize: "0.8rem",
              marginTop: "0.5rem",
              borderTop: "1px dashed var(--charm-border)",
              paddingTop: "0.5rem",
            }}
          >
            {tab.note}
          </div>
        )}
      </div>
    </section>
  );
}
