---
type: added
---
Per-repo `pr_checks.ignore` config in `.fleet.json` / `.fleet.local.json` (path.Match globs) to drop noisy CI checks like gitstream's `minimum-review/default_reviewers` from the PR-badge rollup, so a single non-actionable failure no longer turns the whole badge red.
