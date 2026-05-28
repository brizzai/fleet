export type Status =
  | "running"
  | "waiting"
  | "finished"
  | "idle"
  | "starting"
  | "error";

export interface Session {
  id: string;
  name: string;
  status: Status;
  slot?: number;
  /** Last preview line shown in the right pane when this session is focused. */
  activity?: string;
  /** When true, a fake permission prompt is shown and Y will approve it. */
  pendingApprove?: boolean;
  /**
   * Wall-clock ms when this session last had a *status* transition.
   * The auto-play script uses this to skip recently-flipped sessions, so the
   * demo doesn't visually cluster (everything turning waiting at once after
   * a wave of approvals, etc).
   */
  lastStatusChangeAt?: number;
}

export type PRState = "pending" | "approved" | "failing" | "merged";

export interface Repo {
  id: string;
  name: string;
  branch: string;
  dirty: boolean;
  pr?: { number: number; state: PRState };
  collapsed: boolean;
  sessions: Session[];
}

export interface Cursor {
  repoIdx: number;
  /** null = on the repo header. */
  sessionIdx: number | null;
}

export interface DemoState {
  repos: Repo[];
  cursor: Cursor;
  /** Becomes true after the user clicks/focuses the demo. */
  focused: boolean;
  filter: string;
  filterMode: boolean;
  /** Wall-clock ms of the last user interaction; the script pauses for 5s after. */
  lastUserActionAt: number;
  /** "Attached" preview state — set by Enter on a session. */
  attachedSessionId: string | null;
  /** Monotonic counter to flip Activity strings via setState. */
  tick: number;
  /** When true, the Claude prompt input captures keystrokes; sidebar keybinds are off. */
  inputFocused: boolean;
  /** Text currently typed into the Claude prompt input. */
  inputText: string;
  /**
   * Progressive coaching step for the banner under the demo.
   *  - `intro` : user hasn't focused the demo yet
   *  - `space` : focused; show "press SPACE to jump"
   *  - `enter` : Space pressed; show "now press ENTER to approve"
   *  - `done`  : approved; show celebratory message that fades out
   */
  coachStep: CoachStep;
}

export type CoachStep = "intro" | "space" | "enter" | "done";

export type Action =
  | { type: "cursor_up" }
  | { type: "cursor_down" }
  | { type: "toggle_repo" }
  | { type: "enter" }
  | { type: "add_session" }
  | { type: "approve" }
  | { type: "jump_attention" }
  | { type: "delete" }
  | { type: "filter_start" }
  | { type: "filter_input"; value: string }
  | { type: "filter_end" }
  | { type: "set_focused"; value: boolean }
  | { type: "focus_input" }
  | { type: "blur_input" }
  | { type: "input_change"; value: string }
  | { type: "input_submit" }
  | { type: "tick"; now: number }
  | { type: "script_event"; event: ScriptEvent };

export type ScriptEvent =
  | { kind: "flip_status"; sessionId: string; to: Status }
  | { kind: "set_waiting"; sessionId: string }
  | { kind: "spawn_session"; repoId: string; name: string }
  | { kind: "set_activity"; sessionId: string; text: string };

export const INITIAL_STATE: DemoState = {
  repos: [
    {
      id: "brizzai",
      name: "brizzai",
      branch: "brz-1234",
      dirty: true,
      pr: { number: 5234, state: "approved" },
      collapsed: false,
      sessions: [
        {
          id: "s1",
          name: "api-fix",
          status: "running",
          activity: "Editing internal/api/routes.go",
        },
        {
          id: "s2",
          name: "waiting-pr",
          status: "waiting",
          pendingApprove: true,
          activity: "Bash command: gh pr checks 5234",
        },
        {
          id: "s3",
          name: "seed-migration",
          status: "idle",
          activity: "Idle at prompt",
        },
      ],
    },
    {
      id: "brizz-code-docs",
      name: "brizz-code-docs",
      branch: "feat/demo",
      dirty: false,
      pr: { number: 1431, state: "pending" },
      collapsed: false,
      sessions: [
        {
          id: "s4",
          name: "setup-wizard",
          status: "running",
          activity: "Read 12 files, planning approach…",
        },
        {
          id: "s5",
          name: "market-rebuild",
          status: "waiting",
          pendingApprove: true,
          activity: "Tool use: WriteFile components/Hero.tsx",
        },
      ],
    },
    {
      id: "fleet",
      name: "fleet",
      branch: "fix/race-condition",
      dirty: true,
      pr: { number: 87, state: "failing" },
      collapsed: false,
      sessions: [
        {
          id: "s6",
          name: "flaky-test",
          status: "idle",
          activity: "Idle at prompt",
        },
      ],
    },
  ],
  cursor: { repoIdx: 0, sessionIdx: 0 },
  focused: false,
  filter: "",
  filterMode: false,
  lastUserActionAt: 0,
  attachedSessionId: null,
  tick: 0,
  inputFocused: false,
  inputText: "",
  coachStep: "intro",
};

const SESSION_NAME_POOL = [
  "auth-refactor",
  "fix-flaky-test",
  "perf-budget",
  "rate-limit",
  "graphql-schema",
  "redis-cache",
  "billing-webhooks",
  "ssr-fonts",
  "image-uploader",
  "search-index",
  "search-relevance",
  "i18n-strings",
  "dark-mode-tokens",
  "settings-dialog",
  "preflight-checks",
];

const ACTIVITY_POOL = [
  "Read 4 files, planning approach…",
  "Editing components/Auth.tsx",
  "Bash command: pnpm test --filter web",
  "Tool use: Grep \"useEffect cleanup\"",
  "Searched for 3 patterns, read 7 files",
  "Running migrations…",
  "Resolving 2 unresolved review threads",
  "Tool use: WebFetch \"Next.js streaming docs\"",
  "Bash command: gh pr ready 1432",
];

function randomFromPool<T>(pool: readonly T[]): T {
  return pool[Math.floor(Math.random() * pool.length)]!;
}

function flattenItems(state: DemoState): Array<
  | { kind: "repo"; repoIdx: number }
  | { kind: "session"; repoIdx: number; sessionIdx: number }
> {
  const items: Array<
    | { kind: "repo"; repoIdx: number }
    | { kind: "session"; repoIdx: number; sessionIdx: number }
  > = [];
  state.repos.forEach((repo, repoIdx) => {
    items.push({ kind: "repo", repoIdx });
    if (!repo.collapsed) {
      repo.sessions.forEach((_, sessionIdx) => {
        items.push({ kind: "session", repoIdx, sessionIdx });
      });
    }
  });
  return items;
}

function cursorIndex(state: DemoState): number {
  const items = flattenItems(state);
  return items.findIndex((item) => {
    if (item.kind === "repo")
      return (
        state.cursor.sessionIdx === null && item.repoIdx === state.cursor.repoIdx
      );
    return (
      item.repoIdx === state.cursor.repoIdx &&
      state.cursor.sessionIdx !== null &&
      item.sessionIdx === state.cursor.sessionIdx
    );
  });
}

function cursorFromItem(
  item:
    | { kind: "repo"; repoIdx: number }
    | { kind: "session"; repoIdx: number; sessionIdx: number },
): Cursor {
  if (item.kind === "repo") return { repoIdx: item.repoIdx, sessionIdx: null };
  return { repoIdx: item.repoIdx, sessionIdx: item.sessionIdx };
}

function withUserAction(state: DemoState, now: number): DemoState {
  return { ...state, lastUserActionAt: now };
}

export function reducer(state: DemoState, action: Action): DemoState {
  const now = action.type === "tick" ? action.now : Date.now();

  switch (action.type) {
    case "cursor_up": {
      const items = flattenItems(state);
      if (!items.length) return state;
      const cur = cursorIndex(state);
      const next = items[Math.max(0, cur - 1)]!;
      return withUserAction(
        { ...state, cursor: cursorFromItem(next) },
        now,
      );
    }
    case "cursor_down": {
      const items = flattenItems(state);
      if (!items.length) return state;
      const cur = cursorIndex(state);
      const next = items[Math.min(items.length - 1, cur + 1)]!;
      return withUserAction(
        { ...state, cursor: cursorFromItem(next) },
        now,
      );
    }
    case "toggle_repo": {
      const repos = state.repos.map((r, i) =>
        i === state.cursor.repoIdx ? { ...r, collapsed: !r.collapsed } : r,
      );
      return withUserAction({ ...state, repos }, now);
    }
    case "enter": {
      const repo = state.repos[state.cursor.repoIdx];
      if (!repo) return state;
      // header → toggle collapse
      if (state.cursor.sessionIdx === null) return reducer(state, { type: "toggle_repo" });
      const session = repo.sessions[state.cursor.sessionIdx];
      if (!session) return state;
      // waiting + pending approve → approve (Enter is the only confirm key in the demo)
      if (session.pendingApprove) return reducer(state, { type: "approve" });
      return withUserAction(
        { ...state, attachedSessionId: session.id },
        now,
      );
    }
    case "approve": {
      const repo = state.repos[state.cursor.repoIdx];
      if (!repo || state.cursor.sessionIdx === null) return state;
      const session = repo.sessions[state.cursor.sessionIdx];
      if (!session || !session.pendingApprove) return state;
      const repos = state.repos.map((r, ri) =>
        ri !== state.cursor.repoIdx
          ? r
          : {
              ...r,
              sessions: r.sessions.map((s, si) =>
                si !== state.cursor.sessionIdx
                  ? s
                  : {
                      ...s,
                      status: "running" as Status,
                      pendingApprove: false,
                      activity: "Approved — resuming…",
                      lastStatusChangeAt: now,
                    },
              ),
            },
      );
      // Advance the coach: once the user has approved at least once they
      // "get it" — graduate the banner to the wrap-up state.
      const coachStep: CoachStep =
        state.coachStep === "enter" || state.coachStep === "space"
          ? "done"
          : state.coachStep;
      return withUserAction({ ...state, repos, coachStep }, now);
    }
    case "add_session": {
      const repoIdx = state.cursor.repoIdx;
      const repo = state.repos[repoIdx];
      if (!repo) return state;
      const name = randomFromPool(SESSION_NAME_POOL);
      const id = `s-${Math.random().toString(36).slice(2, 8)}`;
      const session: Session = {
        id,
        name,
        status: "starting",
        activity: "Spawning Claude session…",
        lastStatusChangeAt: now,
      };
      const repos = state.repos.map((r, i) =>
        i === repoIdx ? { ...r, sessions: [...r.sessions, session], collapsed: false } : r,
      );
      return withUserAction(
        {
          ...state,
          repos,
          cursor: { repoIdx, sessionIdx: repo.sessions.length },
        },
        now,
      );
    }
    case "jump_attention": {
      // Cycle through waiting → finished across all repos.
      const items = flattenItems(state);
      const order: Status[] = ["waiting", "finished"];
      const cur = cursorIndex(state);
      const coachStep: CoachStep =
        state.coachStep === "space" ? "enter" : state.coachStep;
      for (const status of order) {
        for (let i = 1; i <= items.length; i++) {
          const item = items[(cur + i) % items.length]!;
          if (item.kind !== "session") continue;
          const s = state.repos[item.repoIdx]!.sessions[item.sessionIdx]!;
          if (s.status === status) {
            return withUserAction(
              { ...state, cursor: cursorFromItem(item), coachStep },
              now,
            );
          }
        }
      }
      return { ...state, coachStep };
    }
    case "delete": {
      const repoIdx = state.cursor.repoIdx;
      const sessionIdx = state.cursor.sessionIdx;
      if (sessionIdx === null) return state; // ignore repo delete
      const repos = state.repos.map((r, i) =>
        i !== repoIdx
          ? r
          : { ...r, sessions: r.sessions.filter((_, si) => si !== sessionIdx) },
      );
      const newCursor: Cursor = (() => {
        const repo = repos[repoIdx]!;
        if (repo.sessions.length === 0) return { repoIdx, sessionIdx: null };
        return {
          repoIdx,
          sessionIdx: Math.min(sessionIdx, repo.sessions.length - 1),
        };
      })();
      return withUserAction({ ...state, repos, cursor: newCursor }, now);
    }
    case "filter_start":
      return withUserAction({ ...state, filterMode: true, filter: "" }, now);
    case "filter_input":
      return withUserAction({ ...state, filter: action.value }, now);
    case "filter_end":
      return withUserAction({ ...state, filterMode: false, filter: "" }, now);
    case "set_focused": {
      const coachStep: CoachStep =
        action.value && state.coachStep === "intro" ? "space" : state.coachStep;
      return { ...state, focused: action.value, coachStep };
    }
    case "focus_input":
      return { ...state, inputFocused: true, lastUserActionAt: now };
    case "blur_input":
      return { ...state, inputFocused: false, inputText: "" };
    case "input_change":
      return { ...state, inputText: action.value, lastUserActionAt: now };
    case "input_submit": {
      const text = state.inputText.trim();
      const repo = state.repos[state.cursor.repoIdx];
      if (!repo || state.cursor.sessionIdx === null || !text) {
        return { ...state, inputFocused: false, inputText: "" };
      }
      const repos = state.repos.map((r, ri) =>
        ri !== state.cursor.repoIdx
          ? r
          : {
              ...r,
              sessions: r.sessions.map((s, si) =>
                si !== state.cursor.sessionIdx
                  ? s
                  : {
                      ...s,
                      status: "running" as Status,
                      pendingApprove: false,
                      activity:
                        text.length > 70 ? `${text.slice(0, 70)}…` : text,
                      lastStatusChangeAt: now,
                    },
              ),
            },
      );
      return {
        ...state,
        repos,
        inputFocused: false,
        inputText: "",
        lastUserActionAt: now,
      };
    }
    case "tick":
      return { ...state, tick: state.tick + 1 };
    case "script_event": {
      const ev = action.event;
      switch (ev.kind) {
        case "flip_status": {
          const repos = state.repos.map((r) => ({
            ...r,
            sessions: r.sessions.map((s) =>
              s.id === ev.sessionId
                ? {
                    ...s,
                    status: ev.to,
                    // Anything that isn't `waiting` shouldn't carry a pending
                    // approval hint — clear it so the (⏎) badge doesn't leak
                    // into running/finished/idle states.
                    pendingApprove:
                      ev.to === "waiting" ? s.pendingApprove : false,
                    activity:
                      ev.to === "running"
                        ? randomFromPool(ACTIVITY_POOL)
                        : ev.to === "finished"
                          ? "Done. Awaiting next prompt."
                          : s.activity,
                    lastStatusChangeAt: now,
                  }
                : s,
            ),
          }));
          return { ...state, repos };
        }
        case "set_waiting": {
          const repos = state.repos.map((r) => ({
            ...r,
            sessions: r.sessions.map((s) =>
              s.id === ev.sessionId
                ? {
                    ...s,
                    status: "waiting" as Status,
                    pendingApprove: true,
                    activity: "Permission requested: Tool use Bash",
                    lastStatusChangeAt: now,
                  }
                : s,
            ),
          }));
          return { ...state, repos };
        }
        case "spawn_session": {
          const repos = state.repos.map((r) =>
            r.id === ev.repoId
              ? {
                  ...r,
                  collapsed: false,
                  sessions: [
                    ...r.sessions,
                    {
                      id: `s-${Math.random().toString(36).slice(2, 8)}`,
                      name: ev.name,
                      status: "starting" as Status,
                      activity: "Spawning Claude session…",
                    },
                  ],
                }
              : r,
          );
          return { ...state, repos };
        }
        case "set_activity": {
          const repos = state.repos.map((r) => ({
            ...r,
            sessions: r.sessions.map((s) =>
              s.id === ev.sessionId ? { ...s, activity: ev.text } : s,
            ),
          }));
          return { ...state, repos };
        }
      }
    }
  }
}

export const SCRIPT_PAUSE_MS = 5_000;

export function isScriptPaused(state: DemoState, now: number): boolean {
  if (state.inputFocused) return true;
  return now - state.lastUserActionAt < SCRIPT_PAUSE_MS;
}
