# Changelog Fragments

Each PR that makes user-facing changes should add a fragment file in `changelog/unreleased/`.

## Format

Create a file: `changelog/unreleased/<any-name>.md`

```yaml
---
type: added
---
Description of the change for end users
```

## Keep it concise

Aim for **1–2 sentences** that lead with the user-facing change. A reader
skimming the release notes should grasp it at a glance:

- Drop implementation detail, marketing tone, and exhaustive sub-point lists.
- If a change is genuinely large, link the PR rather than narrating every facet.
- Prefer "what changed for the user" over "how it was built."

## Voice — make it fun to read

Fragments are rendered in-app by the Release Notes viewer (`Ctrl+K` → "Release
Notes"), which formats `` `code` `` and `**bold**`. Write for someone skimming
that dialog. The delight comes from **honest specificity, not marketing** —
being vivid and true about what changed, never hype.

- **Open with a bold headline.** Start each entry with a 2–4 word `**bold**`
  hook, then the detail — so someone reading only the bold text gets the whole
  release. e.g. `**Worktree names stop snowballing.** …`
- **Lead with the payoff, in second person.** "You can now…", "Sessions now…" —
  not "Added support for…".
- **Name the pain you killed.** The wit is in being honest about the annoyance:
  `…instead of a cryptic "Operation not permitted".`
- **Numbers, not adjectives.** "~150ms instead of up to 2s" — never "much faster".
- **`code`-format the concretes.** Keys, commands, paths: `` `Ctrl+K` ``,
  `` `npm run dev` ``, `` `~/Documents` ``.
- **One idea per bullet.** Split compound changes; scannability beats completeness.
- **Ban hype words:** _seamless, powerful, delightful, robust, blazing_. A dry
  wink is welcome; a sales pitch is not.

Before → after (same fix, feature-first vs. reader-first):

> ~~Creating a new worktree from an existing worktree no longer snowballs the name (repo-a → repo-a-b → repo-a-b-c).~~
>
> **Worktree names stop snowballing.** New worktrees are named as siblings of the main repo — a worktree-of-a-worktree is `repo-b`, not `repo-a-b-c`.

## Valid Types

| Type | Section |
|------|---------|
| `added` | ### Added |
| `improved` | ### Improved |
| `fixed` | ### Fixed |
| `changed` | ### Changed |
| `removed` | ### Removed |
| `deprecated` | ### Deprecated |
| `security` | ### Security |

## Highlights (What's New)

Add `highlight: true` to a fragment's frontmatter to also surface it in the
in-app **What's New** reel (the animated top-right badge, `W` or `Ctrl+K` →
"What's New") — a curated feed of the release's most notable changes:

```yaml
---
type: added
highlight: true
---
**Live terminal drawer.** The `` ` `` drawer now streams shell output live.
```

The item still appears in its normal section (Added/Fixed/…); the flag only
*also* copies it into a `### Highlights` block at the top of the release. Reserve
it for changes a user would be glad to discover — **most fragments are not
highlights**. No flag (or `highlight: false`) means changelog-only, as before.

## Examples

`changelog/unreleased/faster-status.md`:
```yaml
---
type: improved
---
**Status keeps pace with your sessions.** Updates now land in ~150ms, down from up to 2s.
```

`changelog/unreleased/fix-detach-stale.md`:
```yaml
---
type: fixed
---
**No more stale status after detaching.** The sidebar refreshes the moment you leave, instead of showing the last frame.
```

## Skip

If your change doesn't need a changelog entry (CI, typos, deps), comment `/no-changelog` on the PR.

## Release

At release time (`/ship`), fragments are automatically merged into CHANGELOG.md and deleted.
