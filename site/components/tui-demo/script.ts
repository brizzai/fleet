import type { DemoState, ScriptEvent } from "./state";

const SESSION_NAME_POOL = [
  "shadcn-port",
  "rate-limit",
  "graphql-types",
  "viewport-bug",
  "homepage-copy",
  "redis-eviction",
  "analytics-events",
];

const RUNNING_ACTIVITY_POOL = [
  "Read 6 files, planning approach…",
  "Editing components/Auth.tsx",
  "Bash command: pnpm test --filter web",
  "Tool use: Grep \"useEffect cleanup\"",
  "Resolving 2 unresolved review threads",
];

function rng<T>(pool: readonly T[]): T {
  return pool[Math.floor(Math.random() * pool.length)]!;
}

/**
 * Pick a single scripted event based on current state.
 * Called once per second by TuiDemo while the script is unpaused.
 */
export function nextScriptEvent(state: DemoState): ScriptEvent | null {
  const allSessions = state.repos.flatMap((r) =>
    r.sessions.map((s) => ({ session: s, repo: r })),
  );
  if (allSessions.length === 0) return null;

  const roll = Math.random();

  // ~10% chance: a running session finishes
  if (roll < 0.1) {
    const candidates = allSessions.filter((x) => x.session.status === "running");
    if (candidates.length) {
      const target = rng(candidates);
      return { kind: "flip_status", sessionId: target.session.id, to: "finished" };
    }
  }

  // ~10% chance: a finished/idle session starts running again
  if (roll < 0.2) {
    const candidates = allSessions.filter(
      (x) => x.session.status === "finished" || x.session.status === "idle",
    );
    if (candidates.length) {
      const target = rng(candidates);
      return { kind: "flip_status", sessionId: target.session.id, to: "running" };
    }
  }

  // ~8% chance: a running session becomes waiting (permission prompt)
  if (roll < 0.28) {
    const candidates = allSessions.filter((x) => x.session.status === "running");
    if (candidates.length) {
      const target = rng(candidates);
      return { kind: "set_waiting", sessionId: target.session.id };
    }
  }

  // ~5% chance: spawn a new session in a non-empty repo
  if (roll < 0.33) {
    const nonEmpty = state.repos.filter((r) => r.sessions.length < 5);
    if (nonEmpty.length) {
      const repo = rng(nonEmpty);
      return { kind: "spawn_session", repoId: repo.id, name: rng(SESSION_NAME_POOL) };
    }
  }

  // ~30% chance: refresh a running session's activity text
  if (roll < 0.63) {
    const running = allSessions.filter((x) => x.session.status === "running");
    if (running.length) {
      const target = rng(running);
      return {
        kind: "set_activity",
        sessionId: target.session.id,
        text: rng(RUNNING_ACTIVITY_POOL),
      };
    }
  }

  // ~37% — no event this tick
  return null;
}

/**
 * After a session enters `starting`, transition to `running` after 1.6s.
 * The TuiDemo container schedules these one-shots whenever a session
 * appears in `starting` state.
 */
export function startingToRunningEvent(sessionId: string): ScriptEvent {
  return { kind: "flip_status", sessionId, to: "running" };
}
