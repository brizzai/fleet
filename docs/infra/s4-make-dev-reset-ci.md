# S4 — `make dev` + `make reset` + tiny Swift CI

**Status:** open
**Tier:** S (dev-velocity, but cheap enough to land with resilience work)
**Effort:** 30 minutes
**Stack:** Repo

## Problem

Three-process dev loop today: build Go binary → start `fleet daemon --detach` → `swift run` the Mac app. Each step done by hand, repeated dozens of times a day. State-wipe is a manual `rm -rf ~/.config/fleet/`.

The Mac app has **zero CI**. `app/Package.resolved` is gitignored, so dep versions aren't locked. A Swift commit could fail to build and PRs would go green.

## Why now

Cheapest item on the list. Daily-use payoff. Both critics agreed this is real (pragmatist scoped it down from V1's 1-day estimate to 30 minutes by saying "no mprocs, just a bash script").

## Proposed solution

### `make dev`

Top-level Makefile target:
```make
dev: build
	@pkill -x fleet 2>/dev/null; sleep 0.3
	@./build/fleet daemon --detach
	@cd app && swift run
```

Add a trap so Ctrl-C kills the daemon when `swift run` exits.

### `make reset`

```make
reset:
	@ts=$$(date +%Y%m%d-%H%M%S); \
	cp -a ~/.config/fleet /tmp/fleet-backup-$$ts && \
	echo "Backed up to /tmp/fleet-backup-$$ts" && \
	rm -rf ~/.config/fleet && \
	mkdir -p ~/.config/fleet
```

Three lines. Used weekly. **Does not kill tmux** — that's intentional; tmux sessions can be rebuilt from scratch on next launch but you don't want to nuke 20 running Claude conversations.

### Swift CI

1. Remove `Package.resolved` from `app/.gitignore`. Commit current `Package.resolved`.
2. New workflow `.github/workflows/swift.yml`:

```yaml
name: Swift
on: [push, pull_request]
jobs:
  build:
    runs-on: macos-latest
    steps:
      - uses: actions/checkout@v4
      - run: cd app && swift build
      - run: cd app && swift-format lint --recursive Sources/Fleet
```

That's it. No `swift test` until [Later](./README.md) (tests aren't worth writing yet — AppModel still churning).

## Acceptance criteria

- [ ] `make dev` brings up daemon + Mac with one command.
- [ ] Ctrl-C in `make dev` kills both processes cleanly (no orphan daemon).
- [ ] `make reset` produces backup at `/tmp/fleet-backup-<ts>/`, existing TUI tmux sessions untouched.
- [ ] `Package.resolved` checked in.
- [ ] CI fails on a deliberately-broken Swift file (test with a draft PR).
- [ ] CI passes on current `fleet-ui` HEAD.

## Out of scope

- `mprocs`, `watchexec`, `act`, `mise` — flagged as cargo cult by pragmatist critic.
- Full pre-commit hook covering Swift — defer until there are external contributors.
- Caching `~/Library/Caches/org.swift.swiftpm` in CI — premature; first build is fast enough.
- `swift test` — defer (no tests to run; see "Later" in brainstorm).

## References

- `Makefile` — top-level (extend it)
- `app/.gitignore` — remove `Package.resolved`
- `.github/workflows/` — new file `swift.yml`
- `.github/workflows/ci.yml` — existing Go CI, don't disturb
