import type { DemoState, ScriptEvent, Session } from "./state";

const RUNNING_ACTIVITY_POOL = [
  "Read 6 files, planning approach…",
  "Editing components/Auth.tsx",
  "Bash command: pnpm test --filter web",
  'Tool use: Grep "useEffect cleanup"',
  "Resolving 2 unresolved review threads",
];

function rng<T>(pool: readonly T[]): T {
  return pool[Math.floor(Math.random() * pool.length)]!;
}

/**
 * Sessions can't transition status again until this many ms after their last
 * status change. Stops the demo from feeling like everything flips at once
 * (e.g. after a wave of approvals, all the approved sessions would otherwise
 * head straight back to waiting on consecutive ticks).
 */
const STATUS_COOLDOWN_MS = 5_500;

function fresh(s: Session, now: number): boolean {
  return now - (s.lastStatusChangeAt ?? 0) >= STATUS_COOLDOWN_MS;
}

/**
 * Possible auto-play events with relative weights. Only running sessions
 * change status on their own — waiting/finished/idle sessions stay put
 * until the user does something (matches real fleet behavior, where
 * waiting agents need a human to approve before they keep going).
 *
 * Each recipe returns null when no eligible session exists this tick — the
 * dispatcher filters those out so the chosen event always has a valid target.
 */
const RECIPES: Array<{
  weight: number;
  make: (sessions: Session[], now: number) => ScriptEvent | null;
}> = [
  // running → waiting (the headline behavior — most "fun")
  {
    weight: 32,
    make: (sessions, now) => {
      const c = sessions.filter(
        (s) => s.status === "running" && fresh(s, now),
      );
      if (!c.length) return null;
      return { kind: "set_waiting", sessionId: rng(c).id };
    },
  },
  // running → finished
  {
    weight: 16,
    make: (sessions, now) => {
      const c = sessions.filter(
        (s) => s.status === "running" && fresh(s, now),
      );
      if (!c.length) return null;
      return { kind: "flip_status", sessionId: rng(c).id, to: "finished" };
    },
  },
  // refresh activity text on a running session (no cooldown — visual only)
  {
    weight: 17,
    make: (sessions) => {
      const c = sessions.filter((s) => s.status === "running");
      if (!c.length) return null;
      return {
        kind: "set_activity",
        sessionId: rng(c).id,
        text: rng(RUNNING_ACTIVITY_POOL),
      };
    },
  },
];

export function nextScriptEvent(state: DemoState): ScriptEvent | null {
  const sessions = state.repos.flatMap((r) => r.sessions);
  if (!sessions.length) return null;
  const now = Date.now();

  // Materialize only viable events (recipe + concrete event with a real target).
  const viable: Array<{ weight: number; event: ScriptEvent }> = [];
  for (const recipe of RECIPES) {
    const event = recipe.make(sessions, now);
    if (event) viable.push({ weight: recipe.weight, event });
  }
  if (!viable.length) return null;

  // Weighted pick among viable options.
  const total = viable.reduce((sum, v) => sum + v.weight, 0);
  let r = Math.random() * total;
  for (const v of viable) {
    r -= v.weight;
    if (r <= 0) return v.event;
  }
  return viable[viable.length - 1]!.event;
}

/**
 * After a session enters `starting`, transition it to `running` ~1.6s later.
 * Scheduled as a one-shot timer by TuiDemo whenever a `starting` session
 * appears.
 */
export function startingToRunningEvent(sessionId: string): ScriptEvent {
  return { kind: "flip_status", sessionId, to: "running" };
}
