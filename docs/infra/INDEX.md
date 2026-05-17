# Infra Tickets

Generated from the reconciled infra brainstorm (`infra-brainstorm.html`, 2026-05-15). Twelve tickets prioritized after two independent critic passes shifted the focus from dev-velocity to production resilience.

## Tier S — Resilience first (~3 days)

| # | Ticket | Effort |
|---|---|---|
| S1 | [Daemon singleton lock + db integrity + nightly backup](./s1-daemon-singleton-lock.md) | 1 day |
| S2 | [Mac top-level reconnect + crash-loop guard](./s2-mac-top-level-reconnect.md) | 0.5 day |
| S3 | [`fleet doctor` subcommand](./s3-fleet-doctor.md) | 0.5 day |
| S4 | [`make dev` + `make reset` + tiny Swift CI](./s4-make-dev-reset-ci.md) | 30 min |
| S5 | [Sparkle path scaffolding](./s5-sparkle-scaffolding.md) | 1 day |
| S6 | [Snapshot proto unification](./s6-snapshot-proto.md) | 2 hours |

## Tier A — High leverage, near-term

| # | Ticket | Effort |
|---|---|---|
| A7 | [Request IDs + per-client trace field](./a7-request-ids.md) | 1 day |
| A8 | [Schema versioning + migration playbook](./a8-schema-versioning.md) | 0.5 day |
| A9 | [Kill-daemon-while-attached chaos test](./a9-kill-daemon-chaos-test.md) | 0.5 day |
| A10 | [swift-async-algorithms adoption](./a10-swift-async-algorithms.md) | 1 day |
| A11 | [Logging vocabulary doc (skip the sweep)](./a11-logging-vocabulary.md) | 30 min |

## Framing decision (resolve before more infra)

| # | Ticket | Effort |
|---|---|---|
| F1 | [Is the in-process service fallback worth maintaining?](./f1-in-process-fallback-decision.md) | Decision + 1–2 days exec |

## Top 5 to do next

S1 → S2 → S4 → S3 → A7. Roughly 3 days of work, biggest resilience payoff per hour.

Then answer F1 before deciding what's next.
