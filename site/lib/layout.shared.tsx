import type { BaseLayoutProps } from "fumadocs-ui/layouts/shared";

export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      title: (
        <span className="font-mono font-semibold tracking-tight">
          <span className="text-[var(--charm-pink)]">▍</span>fleet
        </span>
      ),
    },
    links: [
      {
        text: "Docs",
        url: "/docs",
      },
      {
        text: "GitHub",
        url: "https://github.com/brizzai/fleet",
        external: true,
      },
    ],
  };
}
