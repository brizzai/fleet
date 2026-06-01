---
type: improved
---

Sidebar and Preview now live in their own rounded-border cards with corner-inset titles (`╭─ Sessions ─...─╮` / `╭─ Preview ─...─╮`), so the two regions read as distinct surfaces instead of one stream split by a hairline. The focused panel switches its border to the accent color in focus mode.

New `fleet-pink` flagship theme (accent `#ff77c6`) is the default for first-run users. Tokyo Night, Catppuccin Mocha, Rosé Pine, Nord and Gruvbox remain available via the `S` settings dialog.

Sidebar cleanup: selected sessions drop the leading `▶` (it collided with the `▸` chevron used on collapsed headers — the inverted-background title was already carrying the selection signal). Worktree branch names render in italic so you can tell the main clone apart from its worktrees at a glance without an extra prefix column. In the default `icon` indicator mode, idle/starting sessions render a dim `·` anchor so the eye has a leftmost mark on every row (bar mode keeps them blank — the gutter bar carries the signal there). Selection background is one contiguous span across each row (PR badge and dirty marker sit inside the highlighted pill instead of bleeding out as separate boxes).

Sidebar width is now responsive: targets 65 absolute columns capped at 45% of terminal width. On a Mac 14" (~150 cols) that's ~43% so long titles fit; on a wide monitor it shrinks to ~26% so the preview keeps its share. Scroll indicators replaced the `⋮` glyph (which rendered as `:` in some fonts) with `… N more above/below`.

Visual rhythm pass: one blank row between origin groups carries the section break, and the indent tightened across the tree so long titles get more horizontal room. On boot, the cursor lands on the first session instead of the first origin, so your first keystroke does something useful.

New `Status style` setting (`icon` default, or `bar` for a VS-Code gutter-style `┃`) lets you pick how non-idle state is shown. Toggle live in the settings dialog.

Running/waiting/idle counts moved out of the top header and into a right-aligned pill embedded in the Sessions panel's top border, next to the title (`╭─ Sessions ── 2 RUN · 1 WAIT · 51 idle ─╮`). The header is just the `❯_ fleet` wordmark now.

Command palette dims the underlying UI via an SGR-faint backdrop so it visually lifts above the content instead of merging with the preview pane.
