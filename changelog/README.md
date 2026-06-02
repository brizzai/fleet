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

## Examples

`changelog/unreleased/faster-status.md`:
```yaml
---
type: improved
---
Status updates now respond in ~150ms instead of up to 2s
```

`changelog/unreleased/fix-detach-stale.md`:
```yaml
---
type: fixed
---
Status showing stale data immediately after detaching from a session
```

## Skip

If your change doesn't need a changelog entry (CI, typos, deps), comment `/no-changelog` on the PR.

## Release

At release time (`/ship`), fragments are automatically merged into CHANGELOG.md and deleted.
