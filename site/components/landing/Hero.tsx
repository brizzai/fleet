"use client";

import Link from "next/link";

const ASCII_WORDMARK = [
  "███████╗██╗     ███████╗███████╗████████╗",
  "██╔════╝██║     ██╔════╝██╔════╝╚══██╔══╝",
  "█████╗  ██║     █████╗  █████╗     ██║   ",
  "██╔══╝  ██║     ██╔══╝  ██╔══╝     ██║   ",
  "██║     ███████╗███████╗███████╗   ██║   ",
  "╚═╝     ╚══════╝╚══════╝╚══════╝   ╚═╝   ",
].join("\n");

export function Hero() {
  return (
    <section
      className="fleet-hero"
      style={{
        position: "relative",
        padding: "5rem 1.25rem 2rem",
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        textAlign: "center",
        maxWidth: 1100,
        margin: "0 auto",
      }}
    >
      <a
        href="https://github.com/brizzai/fleet/releases/latest"
        className="fleet-hero__pill"
        style={{
          display: "inline-flex",
          alignItems: "center",
          gap: "0.55rem",
          padding: "0.4rem 0.9rem",
          borderRadius: 999,
          border: "1px solid var(--charm-border)",
          background: "var(--charm-surface)",
          color: "var(--charm-text-dim)",
          fontSize: "0.85rem",
          fontFamily: "var(--font-mono)",
          textDecoration: "none",
          animation: "fleet-rise 0.6s cubic-bezier(0.2,0.65,0.3,1) 0.05s both",
        }}
      >
        <span
          aria-hidden
          style={{
            width: 6,
            height: 6,
            borderRadius: "50%",
            background: "var(--charm-pink)",
            boxShadow: "0 0 8px var(--charm-pink)",
            animation: "fleet-pulse 2s ease-in-out infinite",
          }}
        />
        See what&apos;s new in v2.2.0
        <span aria-hidden>→</span>
      </a>

      <pre
        aria-label="fleet"
        className="font-mono"
        style={{
          background: "transparent",
          margin: "1.25rem 0 0",
          padding: 0,
          color: "var(--charm-pink)",
          fontSize: "clamp(0.55rem, 1.6vw, 1rem)",
          lineHeight: 1.05,
          textShadow: "0 0 24px rgba(244,143,177,0.35)",
          backgroundImage:
            "linear-gradient(110deg, var(--charm-pink) 0%, var(--charm-pink-bright) 40%, var(--charm-pink) 60%, var(--charm-pink) 100%)",
          backgroundSize: "200% 100%",
          WebkitBackgroundClip: "text",
          backgroundClip: "text",
          WebkitTextFillColor: "transparent",
          animation:
            "fleet-rise 0.6s cubic-bezier(0.2,0.65,0.3,1) 0.15s both, fleet-shimmer 8s ease-in-out infinite",
        }}
      >
        <code>{ASCII_WORDMARK}</code>
      </pre>

      <h1
        className="font-mono"
        style={{
          fontSize: "clamp(1.6rem, 3.6vw, 2.5rem)",
          lineHeight: 1.15,
          letterSpacing: "-0.01em",
          margin: "1rem 0 0",
          color: "var(--charm-text)",
          maxWidth: "26ch",
          animation: "fleet-rise 0.6s cubic-bezier(0.2,0.65,0.3,1) 0.25s both",
        }}
      >
        Run 10 Claude Code agents. Stay sane.
      </h1>

      <p
        style={{
          fontSize: "clamp(1rem, 1.4vw, 1.15rem)",
          lineHeight: 1.55,
          color: "var(--charm-text-dim)",
          margin: "1rem 0 0",
          maxWidth: "56ch",
          animation: "fleet-rise 0.6s cubic-bezier(0.2,0.65,0.3,1) 0.35s both",
        }}
      >
        A terminal cockpit for orchestrating Claude Code sessions in parallel. See
        which agents need you. Jump in, direct, jump out.
      </p>

      <div
        className="fleet-hero__actions"
        style={{
          display: "flex",
          gap: "0.75rem",
          flexWrap: "wrap",
          justifyContent: "center",
          marginTop: "1.5rem",
          animation: "fleet-rise 0.6s cubic-bezier(0.2,0.65,0.3,1) 0.45s both",
        }}
      >
        <Link
          href="/docs/getting-started/install"
          className="fleet-cta fleet-cta--primary"
        >
          <span>Install fleet</span>
          <span aria-hidden>↓</span>
        </Link>
        <Link href="/docs" className="fleet-cta">
          <span>Read the docs</span>
          <span aria-hidden>→</span>
        </Link>
      </div>

      <div
        aria-label="Install command"
        style={{
          display: "inline-flex",
          alignItems: "center",
          gap: "0.65rem",
          padding: "0.6rem 1rem",
          borderRadius: 6,
          background: "var(--charm-surface)",
          border: "1px solid var(--charm-border)",
          fontFamily: "var(--font-mono)",
          fontSize: "0.95rem",
          marginTop: "1.5rem",
          animation: "fleet-rise 0.6s cubic-bezier(0.2,0.65,0.3,1) 0.55s both",
        }}
      >
        <span style={{ color: "var(--charm-pink)", userSelect: "none" }}>$</span>
        <code style={{ color: "var(--charm-text)", background: "transparent" }}>
          brew install brizzai/tap/fleet
          <span
            aria-hidden
            style={{
              color: "var(--charm-pink)",
              marginLeft: "0.35em",
              animation: "fleet-blink 1.05s steps(2,start) infinite",
            }}
          >
            ▋
          </span>
        </code>
      </div>

      <style>{`
        .fleet-cta {
          display: inline-flex;
          align-items: center;
          gap: 0.55rem;
          padding: 0.7rem 1.2rem;
          border-radius: 8px;
          border: 1px solid var(--charm-border);
          background: var(--charm-surface);
          color: var(--charm-text);
          text-decoration: none;
          font-family: var(--font-mono);
          font-size: 0.95rem;
          transition: transform 0.18s ease, border-color 0.18s ease, background 0.18s ease, box-shadow 0.18s ease;
        }
        .fleet-cta:hover {
          transform: translateY(-1px);
          border-color: var(--charm-pink);
          background: var(--charm-surface-elev);
          box-shadow: 0 6px 18px -10px rgba(244,143,177,0.6);
        }
        .fleet-cta--primary {
          background: var(--charm-pink);
          border-color: var(--charm-pink);
          color: #1a0a14;
          font-weight: 600;
        }
        .fleet-cta--primary:hover {
          background: #f8b8cd;
          border-color: #f8b8cd;
          color: #1a0a14;
          box-shadow: 0 8px 24px -10px rgba(244,143,177,0.5);
        }
        @keyframes fleet-rise {
          from { opacity: 0; transform: translateY(8px); }
          to { opacity: 1; transform: translateY(0); }
        }
        @keyframes fleet-shimmer {
          0%, 100% { background-position: 0% 50%; }
          50% { background-position: 100% 50%; }
        }
        @keyframes fleet-pulse {
          0%, 100% { box-shadow: 0 0 0 0 rgba(244,143,177,0.6); }
          50% { box-shadow: 0 0 0 6px rgba(244,143,177,0); }
        }
        @keyframes fleet-blink {
          50% { opacity: 0; }
        }
      `}</style>
    </section>
  );
}
