# fleet design system

Rules for fleet's TUI. Follow them; they are not suggestions, and most were paid
for by a bug. Implementation lives in `internal/ui/design.go` — the roles below
are functions there, and it is the only file allowed to construct an accent fill.

---

## 1. Weight order

Three facts are on screen at once and constantly get mistaken for each other:

| axis | question it answers | how many per screen |
|---|---|---|
| **focus** | where do my keystrokes go? | at most one |
| **selection** | where is this list's cursor? | one per list |
| **mode** | which option is in effect? | one per group |

**Their visual weight is fixed in that order, and a mode never fills.**

The command palette shipped with its active tab drawn as a filled accent chip —
the heaviest treatment fleet has, and the same one the sidebar uses for the row
you are standing on. So the *least* important of the three was the loudest thing
on screen, and none of them could be told apart. Each render was defensible
alone; the bug lived only in the relationship, which is why the guard is a
scarcity rule rather than a per-dialog assertion.

**A background fill is the scarcest thing in this UI.** Spend it on the cursor
and the caret. Everything else gets color and weight.

---

## 2. Roles

Call these. Do not build the style inline.

| role | means | renders |
|---|---|---|
| `FocusCaret()` | the block cursor | inverted accent |
| `SelectionPill(focused)` | list cursor, fill sized to its own label | inverted accent / muted band |
| `SelectionPillSecondary(focused)` | the same row's supporting text | matches the pill |
| `SelectionBand()` | list cursor, fill spans a full wide row | muted band |
| `SelectionBandSecondary()` | the band's supporting text | `ColorText` on band |
| `SelectionMarker(focused)` | the `▸` before a selected row | accent / dim |
| `ModeOn()` / `ModeOff()` | the option in effect / the rest | accent+underline / dim |
| `PrimaryAction()` | the one button Enter presses | inverted accent |
| `NewTextInput()` | every text input | themed prompt, placeholder, caret |

Disabled rows are `DimStyle` **plus the reason**. Structure (section headers,
tree connectors) is `DimStyle`, bolded if it must separate.

Adding a role is fine. Adding it **without saying what it outranks** is not.

### Pill or band

Sized by the region that gets filled, not the row's total width:

- **≤ `SelectionFillWidthGuide` (40) columns → `SelectionPill`.** The fill is a
  mark. The sidebar fills `symbol + glyph + title + slot` as one pill; a dropdown
  fills its label.
- **> 40 columns → `SelectionBand`.** A solid accent bar 96 columns wide is a
  wall of color. The band goes muted and the accent moves to `SelectionMarker`
  and the panel border, which is where the eye lands anyway.

A fill that stops mid-row reads as a rendering fault, not a selection. Carry it
across the gap and the right column, padded to the row.

### Tabs are not automatically modes

A tab is a **mode** when focus lives elsewhere (the command palette: Tab cycles
tabs, typing goes to the input, arrows to the list) — so it uses `ModeOn`.

A tab is a **selection** when switching it moves the keyboard (the terminal
drawer, which is always in typing mode when visible, so picking a tab picks the
shell you type into) — so it uses `SelectionPill(true)`.

Ask which one moves the keyboard. That is the whole test.

---

## 3. Presentation tiers

| tier | use when | how |
|---|---|---|
| **full-screen** | the task owns the user's attention for more than a moment, or needs the whole viewport: settings, help, release notes, bug report, onboarding, consent | `renderBody` returns the dialog's `View()` outright |
| **centered overlay** | a focused task the surrounding context still explains: the command palette | `dimBackdrop(base)` then `overlayAt` at center |
| **row-anchored dropdown** | the action belongs to one visible row and must stay attached to it: context menu, snooze, account picker, allowed accounts | `overlayAt` at the row, **no `dimBackdrop`** |

Rules that go with them:

- A dropdown **never dims the backdrop.** It is a small box beside the row it
  acts on; dimming the app for it reads as a modal takeover.
- A dropdown **acts on the row it was opened over, not the cursor.** Messages
  move the cursor while a menu is open (a finishing session rebuilds the list).
  Capture the row's identity on open and re-find it at dispatch.
- Anchored boxes hang below their row and **flip above** near the footer.
- Every modal surface registers in `modalOpen()`, `routeToModal`, `renderBody`
  and `SetSize`. `routeToModal` carries key and paste messages **only** — a
  dialog's async `tea.Cmd` results need their own case in `Home.Update`, or the
  feature is dead in the app while its unit tests pass.

---

## 4. Panels and borders

- Rounded borders throughout (`PanelStyle`, `DialogStyle`).
- **Accent border = this panel has the keyboard.** Muted border otherwise. This
  is the only border-level focus signal; do not invent a second one.
- Titles inset into the top border, status insets top-right, key hints inset
  into the bottom border. Use the `RenderBorderedPanel*` family — it guarantees
  output is exactly `width × height`.
- A dialog is **fixed size**. A hint long enough to wrap must not grow the box
  a row mid-keystroke.

---

## 5. Color

Every color comes from the palette (`internal/ui/palette.go`). No literals, no
`lipgloss.Color("240")`. Six themes must all work.

| token | for |
|---|---|
| `ColorAccent` | focus, selection, the brand |
| `ColorText` / `ColorTextDim` | content / everything secondary |
| `ColorBorder` | panel chrome, and the muted selection band |
| `ColorSurface` | raised backgrounds |
| `ColorGreen/Yellow/Blue/Red` | **semantic only** — status, PR state, priority |
| `ColorBrand` | the pink wordmark; deliberately theme-independent |

- **One thing owns a color.** The status dot owns status color; agent glyphs are
  monochrome and carry identity by *shape*. If a second column starts meaning
  green, one of them is wrong.
- Semantic color is not your accent and does not count against its budget.
- `ColorTextDim` on `ColorBorder` does not read. That pairing is why
  `SelectionBandSecondary` is `ColorText` — it drops the bold, not the color.
- Add a style to the table in `styles.go`? It is constructed **only** in
  `ApplyPalette`. Declare it bare. A style with its own initializer keeps
  default-pink under every other theme, silently.

### Glyphs

Width-1, from blocks base terminal fonts actually cover (Dingbats, Geometric
Shapes, Arrows). Menlo — macOS Terminal's default — has no U+23FE and no U+2B21,
and renders a fallback box. Check the font before picking a clever glyph.

---

## 6. Interaction

- **Exactly one highlight, and the caret lives with it.** If arrowing moves the
  highlight off a text field, blur the field. Typing returns both — and **keeps
  the keystroke**.
- **The highlight is the promise.** Enter acts on whatever carries it, always.
- **The footer names what Enter does now**, and changes as the highlight moves.
- **The highlight never moves on its own.** A picker that re-selects for you is
  ambiguity coming back through the window.
- **A disabled row renders dimmed with its reason, and the reason names the
  clause that actually failed.** Guards are conjunctions; a constant note
  contradicts the status dot rendered next to it. Dimmed rows are skipped by
  `j`/`k`, and a lit row may never dead-click — mirror the real handler exactly.
- **A row's shortcut must be the key that actually works** from inside that
  surface, not the one from the main screen.
- **Refuse, don't fall back.** An unparseable duration, an empty allowlist, an
  unset required field: block submit and say which field. Silently substituting
  a default does the opposite of what the screen appears to promise.

---

## 7. Text

- Build inputs with `NewTextInput()`. **The input owns its prompt** — draw a
  second `>` beside it and you ship `> >`.
- Truncate with `ansi.Truncate`, never by bytes. Pane content is dense with
  3-byte box-drawing runes, so byte slicing cuts at a third of the intended
  columns and can split a rune.
- **Pad raw text, then style.** Padding a styled string counts the ANSI bytes
  and the columns come out ragged.
- Measure with `lipgloss.Width`, not `len`.
- Highlighting fuzzy matches maps indexes back onto the source strings by
  offset, so a column composed with embedded ANSI lights up the wrong
  characters. Compose in parts or leave it unstyled.

---

## 8. Guards

In `internal/ui/design_test.go`:

- `TestAccentFillIsConstructedInOneFile` — `Background(ColorAccent)` outside
  `design.go` fails the build.
- `TestModeNeverFills` — a mode indicator with any background fails.
- `TestSelectionDistinguishesFocus` — the focused and blurred weights must differ.

Elsewhere: `TestNormalizedDialogsHoldNoTextInput`,
`TestContextMenuIDsAreDispatchable`,
`TestContextMenuDispatchFollowsTargetNotCursor`,
`TestSnoozeDialogHeightIsStable`.

When a rule here gets broken and no test caught it, add the test in the same
change. That is how this list got written.
