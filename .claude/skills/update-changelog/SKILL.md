---
name: update-changelog
description: Adds a changelog fragment under changelog/unreleased/ (type: frontmatter). Use when the user says "changelog", "update changelog", or after completing a feature/fix that needs a changelog entry. Can also skip via /no-changelog.
disable-model-invocation: true
argument-hint: "[description or 'skip']"
---

## Current Branch Context
!`git log --oneline $(git merge-base HEAD master)..HEAD 2>/dev/null || echo "no commits"`
!`git diff --name-only $(git merge-base HEAD master)..HEAD 2>/dev/null || echo "no changes"`

## Task

Add a changelog **fragment** for this PR, or skip with `/no-changelog`.

This repo does **not** hand-edit `CHANGELOG.md`. Each PR drops a small fragment
file in `changelog/unreleased/`; at release time (`/ship`) they're merged into
`CHANGELOG.md` and deleted automatically. Full guide + voice: `changelog/README.md`.

### If argument is "skip", "no-changelog", or "none"
Comment `/no-changelog` on the PR and stop:
```bash
gh pr comment --body "/no-changelog"
```

### Otherwise, add a fragment

1. **Create** `changelog/unreleased/<short-name>.md` (any unique kebab-case name).
2. **Frontmatter** with exactly one `type:` — one of: `added`, `improved`,
   `fixed`, `changed`, `removed`, `deprecated`, `security`.
3. **Optionally** add `highlight: true` to also surface the entry in the in-app
   **What's New** reel (the animated top-right badge, `W` / `Ctrl+K` →
   "What's New"). The entry still renders in its normal type section; the flag
   *also* copies it into a `### Highlights` block leading the release. Reserve it
   for changes a user would be glad to discover — **most fragments are not
   highlights**.
4. **Body** — 1–2 sentences, reader-first.

```yaml
---
type: added
highlight: true
---
**Bold headline.** Reader-first description of what changed for the user.
```

### Writing style
- **Open with a bold 2–4 word headline**, then the detail — so someone reading
  only the bold text gets the whole release.
- **Write for the user, not the developer** — lead with the outcome, in second
  person ("You can now…", "Sessions now…"), not "Added support for…".
- **Numbers over adjectives** ("~150ms instead of up to 2s", never "much faster").
- **`code`-format the concretes** — keys, commands, paths (`Ctrl+K`, `~/Documents`).
- **One idea per bullet**; ban hype words (seamless, powerful, delightful, blazing).
- **Skip internal-only changes** — refactors, new internal packages, docs files
  are not changelog-worthy on their own.
