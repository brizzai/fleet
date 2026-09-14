import "./globals.css";
import type { Metadata } from "next";
import type { ReactNode } from "react";
import Script from "next/script";
import { RootProvider } from "fumadocs-ui/provider";

// Set only by .github/workflows/deploy-site.yml, so the tag ships from the
// deployed Pages site and nowhere else: `npm run dev` and a local
// `npm run build` both leave it unset and send nothing.
const GA_MEASUREMENT_ID = process.env.NEXT_PUBLIC_GA_ID;

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
        {GA_MEASUREMENT_ID ? (
          <>
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
          </>
        ) : null}
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
