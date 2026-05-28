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
    </main>
  );
}
