# fleet site

Marketing landing + docs site for fleet. **Next.js 15 + Fumadocs**, statically exported to GitHub Pages at `brizzai.github.io/fleet`.

## Stack pins (don't bump without checking)

- `fumadocs-core`: ^15.0.0
- `fumadocs-ui`: ^15.0.0
- `fumadocs-mdx`: **^13.0.8** — do NOT upgrade to 14.x or 15.x. 14.3.2 imports `fumadocs-core/dist/content/md/frontmatter.js` (only exists in core 16+). 15.x peer-deps `fumadocs-core ^16.7.0` which then drags `fumadocs-ui ^16` which peer-deps `next ^16`. Stay on the 15.x/13.x/15.x triple for now.
- `next`: ^15
- `react`: ^19
- `tailwindcss`: ^4 (via `@tailwindcss/postcss`)

## Deploy

- `next.config.mjs` sets `output: 'export'`, `basePath: '/fleet'`, `trailingSlash: true`, `images: { unoptimized: true }`.
- `.github/workflows/deploy-site.yml` builds `site/` on every push to `master` that touches `site/**` and publishes `site/out/` to Pages. Pages source is set to **GitHub Actions** in repo settings — don't change it.

## basePath gotcha (real bug we hit)

Raw `<a href="/docs/...">` does NOT get the `/fleet` basePath prepended. Only `next/link`'s `Link` and components built on it do. Always use `Link` for internal hrefs. External links (`https://...`) are unaffected. Fumadocs nav `links` go through Fumadocs' own `Link` internally — no fix needed there.

## Theme

`app/globals.css` defines `--charm-*` and `--tn-*` palette tokens and overrides every `--color-fd-*` Fumadocs token. To restyle docs, edit the token overrides, not the Fumadocs components.

**Every `--charm-*` token is declared twice** — light under `:root`, dark under `.dark` (the class next-themes puts on `<html>`). Source order matters: `:root` and `.dark` have equal specificity, so the dark block must stay *after* the light one. The `--color-fd-*` mapping below them is written once and mode-agnostic, because each value is a `--charm-*` token that already switches.

**Never hardcode a color in a component.** The theme toggle was dead for exactly this reason: the palette lived only in `:root`, so flipping the class changed nothing, and the landing components carried literal `rgba(244,143,177,…)` glows and `#1a0a14` on-pink text that no theme could reach. If you need a new color, add a token pair — that includes glows (`--charm-glow`, `--charm-glow-soft`), tints (`--charm-tint`), translucent slabs (`--charm-panel`, `--charm-panel-ring`), and the page washes (`--charm-wash-1..3`). `--charm-on-pink` is the text color that sits *on* a pink fill; it is near-black in dark and white in light, because the light-mode pink is dark enough to need it.

The **TUI demo is the deliberate exception**: `components/tui-demo/palette.ts` is hardcoded tokyo-night and stays dark in both modes — a terminal doesn't turn white. Only demo chrome *outside* the terminal frame (`CoachBanner`'s panel and inline `kbd`s) reads `--charm-*`.

## TUI demo (`components/tui-demo/`)

Pure-React recreation of the fleet sidebar+preview. Reads as one feature; touch it carefully.

- **`palette.ts`** mirrors `internal/ui/palette.go` (tokyo-night). The `pink: "#f48fb1"` token was added late; don't sneak `?? "#f48fb1"` fallbacks back in.
- **`state.ts`** is a `useReducer` machine. `dispatch` is threaded down to `Preview`/`PromptInput` for the typing flow; don't lift `useState` to children.
- **`script.ts`** drives auto-play. Invariants:
  - **Only `running` sessions transition on their own.** Waiting/finished/idle sit until the user acts — matches real fleet, and is what the `SPACE → ENTER` coaching banner (`CoachBanner.tsx`, rendered under the demo) teaches.
  - **Per-session 5.5s cooldown** (`STATUS_COOLDOWN_MS`) on status changes prevents visual clustering after batches of approvals. `set_activity` (text-only) is exempt.
  - **`pendingApprove` MUST be cleared** whenever a session leaves `waiting`. The reducer's `flip_status` case does this; `SessionLine` also requires `status === "waiting"` to render the `(⏎)` hint as a safety net.
- **`TuiDemo.tsx`** uses a jittered `setTimeout` chain (not `setInterval`) so events don't fire on a metronome.
- **Input vs keybind mode** (`state.inputFocused`): clicking the `❯` prompt switches to typing-the-prompt mode; sidebar keybinds (j/k/Enter/Space/a/d/`/`) are gated off until Esc/click-outside.
- **Mobile/touch**: auto-play only, no keyboard. `(max-width: 720px) | (pointer: coarse)` triggers this.

## Local dev

```bash
npm install   # uses package-lock.json — do not regenerate without intent
npm run dev   # http://localhost:3000/fleet
npm run build # produces site/out/ — verify before pushing site/** changes
```

If CSS 404s after many hot reloads: kill dev, `rm -rf .next`, restart.

## Content

- MDX under `content/docs/` with `meta.json` per directory controlling sidebar order.
- Internal cross-links in MDX: write `/docs/...` (Fumadocs handles basePath via its own Link renderer).
- The landing page is `app/(home)/page.tsx` — not MDX, just composed React components from `components/landing/`.

## Changelog

This subproject is the marketing site; the repo's `changelog/unreleased/` is for fleet-binary user impact. Site-only PRs should comment `/no-changelog` on the PR (the label workflow then needs an empty commit or a label re-toggle to re-trigger Changelog Check — pushing the same PR's branch is enough).
