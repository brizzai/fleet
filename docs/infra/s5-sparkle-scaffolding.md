# S5 — Sparkle path scaffolding now

**Status:** open
**Tier:** S (promoted from Tier B by veteran critic)
**Effort:** 1 day
**Stack:** Swift (Mac app)

## Problem

`swift build` produces a bare Mach-O. **Not an `.app` bundle.** No Info.plist (only inside `.build/checkouts/`). No codesign. No notarization. No DMG. No auto-update.

`FleetApp.swift` has scattered comments like *"when we ship a real bundle this can come out."* That day hasn't arrived but every week it gets harder to arrive — distribution touches build, signing, entitlements, and update logic all at once.

Veteran critic: *"Sparkle in Tier B treated it as quality-of-life. It isn't — it's the gate to 'do you have Mac users at all.' Without auto-update, every bug fix means a manual download. The day you put the Mac app in front of someone, you need it."*

## Why now

- Pragmatist agreed: real ("real, but only the day you ship").
- Veteran wants it promoted: distribution is the unlock.
- Scaffolding now ≠ shipping now. Get the bundle + Sparkle wiring in place so the first user release isn't a 2-week rabbit hole.

## Proposed solution

1. **Add Sparkle as SPM dep** in `app/Package.swift`.
2. **Make target `make app`** in top-level Makefile:
   - Run `xcodebuild -scheme Fleet -archivePath build/Fleet.xcarchive archive`
   - Run `xcodebuild -exportArchive -archivePath build/Fleet.xcarchive -exportOptionsPlist app/ExportOptions.plist -exportPath build/`
   - Result: `build/Fleet.app`
3. **Info.plist** at `app/Resources/Info.plist`:
   - `CFBundleShortVersionString` — from git describe (matches Go binary)
   - `CFBundleVersion` — same
   - `SUFeedURL` — placeholder (e.g., `https://example.com/appcast.xml` — replace at first release)
   - `LSMinimumSystemVersion` — `15.0` (matches Package.swift platforms)
   - `SUEnableInstallerLauncherService` — `true` (Sparkle 2.x XPC requirement)
4. **Entitlements** at `app/Resources/Fleet.entitlements`:
   - App Sandbox: **off** (we need to spawn tmux + read arbitrary filesystem paths). Document why.
   - Hardened Runtime: on (required for notarization).
   - Allow JIT/exec/etc as needed by SwiftTerm.
5. **Wire Sparkle in FleetApp.swift:**
   - `SUUpdater.shared()` init at startup
   - "Check for Updates…" menu item via `CommandGroup`
   - Gate behind `config.auto_update` (add to `AppConfig.swift` + proto, default `false` for now)
6. **`app/RELEASING.md`** documenting the manual flow (Developer ID signing → notarytool → staple → DMG → appcast.xml update). Don't run yet — but write it down before the steps fade.

## Acceptance criteria

- [ ] `make app` produces `build/Fleet.app` that launches and runs the full app.
- [ ] `Fleet.app/Contents/Info.plist` has correct CFBundleShortVersionString matching `git describe`.
- [ ] "Check for Updates…" menu item appears under the Fleet menu.
- [ ] Sparkle initialization logs to FleetLog at startup.
- [ ] `RELEASING.md` documents the full release flow (untested OK, but specific enough that future you can follow it).
- [ ] No regression in `swift run` development workflow.

## Out of scope

- Actually shipping an update (no Developer ID yet → no codesign → no notarization possible).
- Apple Developer Program account setup.
- GitHub release automation / appcast.xml generation.
- DMG creation (`create-dmg` script).
- Renaming the binary / SwiftPM target.

## References

- [Sparkle 2.x documentation](https://sparkle-project.org/documentation/)
- [Automating Sparkle releases with GitHub Actions (Medium)](https://medium.com/@alex.pera/automating-xcode-sparkle-releases-with-github-actions-bd14f3ca92aa) — for later
- `app/Package.swift` — current SPM manifest
- `app/Sources/Fleet/FleetApp.swift` — wire Sparkle init here
- `app/Sources/Fleet/Models/AppConfig.swift` — add `auto_update` field
- `proto/fleet/v1/fleet.proto` — extend Config message
