# A10 — swift-async-algorithms adoption

**Status:** open
**Tier:** A
**Effort:** 1 day
**Stack:** Swift (Mac app)

## Problem

AppModel filter input uses `onChange` workarounds for debounce. The stream consumer in `StreamConsumers.swift` is hand-rolled with manual `Task` management for two streams (`SessionUpdate`, `HookEventStream`). No standard AsyncSequence operators.

## Why now

Both critics flagged this as the **one Swift package that's genuinely useful, not cargo cult**:
- Pragmatist: *"swift-async-algorithms — actually useful, would get used."*
- Veteran: *"High value, not transformative ... cleaner code in StreamConsumers, not a bug-fixer."*

Apple-blessed, no real downsides.

## Proposed solution

1. **Add `swift-async-algorithms`** to `app/Package.swift`:
   ```swift
   .package(url: "https://github.com/apple/swift-async-algorithms.git", from: "1.0.0")
   ```
2. **Filter debounce** (`SidebarView.swift`):
   - Replace the current `onChange` + manual timer (if any) with `.debounce(for: .milliseconds(150))` on the filter text stream.
   - Reduces re-filter churn during fast typing.
3. **Stream merger** (`StreamConsumers.swift`):
   - Use `merge(sessionStream, hookEventStream)` to consume both as a single sequence.
   - Removes one of the two parallel `Task`s; cleanup gets simpler.
4. **Reconnect throttle** (potentially): if backoff schedule has a min interval, `.throttle(for: .seconds(0.25))` keeps things smooth.

Don't migrate every async path. Pick the three that benefit (filter, stream merge, maybe one reconnect path). Leave the rest alone.

## Acceptance criteria

- [ ] `swift-async-algorithms` added to Package.swift + Package.resolved.
- [ ] Filter input no longer flickers/relayouts on rapid typing.
- [ ] Stream consumer is shorter than current hand-rolled version (LOC delta visible in diff).
- [ ] No regression in stream reconnect ([S2](./s2-mac-top-level-reconnect.md)).
- [ ] `swift build` clean.

## Out of scope

- Full migration to AsyncSequence everywhere — some Combine-style uses are fine as-is.
- `AsyncChannel`-based event bus (over-engineering for current size).
- Backpressure tuning on the stream side.

## References

- `app/Package.swift` — add dep
- `app/Sources/Fleet/DaemonClient/StreamConsumers.swift` — stream merge candidate
- `app/Sources/Fleet/Views/Sidebar/SidebarView.swift` — filter debounce
- [swift-async-algorithms (Apple)](https://github.com/apple/swift-async-algorithms)
