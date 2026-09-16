import { Hero } from "@/components/landing/Hero";
import { Features } from "@/components/landing/Features";
import { InstallTabs } from "@/components/landing/InstallTabs";
import { CursorAura } from "@/components/landing/CursorAura";
import { TuiDemo } from "@/components/tui-demo/TuiDemo";

export default function Page() {
  return (
    <main>
      <CursorAura />
      <Hero />
      <div style={{ padding: "0 1.25rem" }}>
        <TuiDemo />
      </div>
      <Features />
      <InstallTabs />
      <footer
        style={{
          padding: "3rem 1.25rem 2.5rem",
          textAlign: "center",
          fontFamily: "var(--font-mono)",
          fontSize: "0.8rem",
          color: "var(--charm-text-faint)",
        }}
      >
        Built by{" "}
        <a
          href="https://www.brizz.ai/"
          style={{ color: "var(--charm-text-dim)", textDecoration: "underline", textUnderlineOffset: 3 }}
        >
          Brizz
        </a>
        , the agent analytics platform
      </footer>
    </main>
  );
}
