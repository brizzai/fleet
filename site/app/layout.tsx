import "./globals.css";
import type { Metadata } from "next";
import type { ReactNode } from "react";
import { RootProvider } from "fumadocs-ui/provider";

export const metadata: Metadata = {
  title: {
    default: "fleet — Run 10 Claude Code agents. Stay sane.",
    template: "%s — fleet",
  },
  description:
    "A terminal cockpit for orchestrating Claude Code sessions in parallel. See which agents need you. Jump in, direct, jump out.",
  metadataBase: new URL("https://brizzai.github.io"),
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className="flex flex-col min-h-screen antialiased">
        <RootProvider
          theme={{
            defaultTheme: "dark",
            enableSystem: false,
          }}
        >
          {children}
        </RootProvider>
      </body>
    </html>
  );
}
