import "./globals.css";
import type { Metadata } from "next";
import type { ReactNode } from "react";
import Script from "next/script";
import { RootProvider } from "fumadocs-ui/provider";

const GA_MEASUREMENT_ID = "G-3PPLWJ1R08";

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
      <head>
        {/* Google tag (gtag.js) */}
        <Script
          src={`https://www.googletagmanager.com/gtag/js?id=${GA_MEASUREMENT_ID}`}
          strategy="afterInteractive"
        />
        <Script id="google-analytics" strategy="afterInteractive">
          {`window.dataLayer = window.dataLayer || [];
function gtag(){dataLayer.push(arguments);}
gtag('js', new Date());

gtag('config', '${GA_MEASUREMENT_ID}');`}
        </Script>
      </head>
      <body className="flex flex-col min-h-screen antialiased">
        <RootProvider
          theme={{
            // Dark is the brand default for a first visit, but the toggle
            // offers light and system too, so next-themes has to handle both.
            defaultTheme: "dark",
            enableSystem: true,
          }}
        >
          {children}
        </RootProvider>
      </body>
    </html>
  );
}
