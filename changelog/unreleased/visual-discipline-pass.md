---
type: improved
---

Sidebar and Preview now live in their own rounded-border cards with corner-inset titles (`╭─ Sessions ─...─╮` / `╭─ Preview ─...─╮`), so the two regions read as distinct surfaces instead of one stream split by a hairline. The focused panel switches its border to the accent color in focus mode.

New `fleet-pink` flagship theme (Bubblegum `#ff77c6`) is the default for first-run users. Tokyo Night, Catppuccin Mocha, Rosé Pine, Nord and Gruvbox remain available via the `S` settings dialog.

Sidebar cleanup: selected sessions drop the leading `▶` (it collided with the `▸` chevron used on collapsed headers — the inverted-background title was already carrying the selection signal). Worktree checkouts now show a dim `wt·` prefix so you can tell the main clone apart from its worktrees at a glance. Dirty branches lose their `*` glyph and instead tint the branch name orange. Idle and Starting sessions render no glyph — only RUN/WAIT/ERR/FIN get an indicator, so the screen no longer scans as "rows of empty circles."

New `Status style` setting (`icon` default, or `bar` for a VS-Code gutter-style `┃`) lets you pick how non-idle state is shown. Toggle live in the settings dialog.

Running/waiting/idle counts moved out of the top header and into a right-aligned pill embedded in the Sessions panel's bottom border (`╰── 2 RUN · 1 WAIT · 51 idle ─╯`). The header is just the `❯_ fleet` wordmark now.

Command palette dims the underlying UI via an SGR-faint backdrop so it visually lifts above the content instead of merging with the preview pane.
