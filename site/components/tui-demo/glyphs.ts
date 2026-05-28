export const TREE = {
  branch: "├─",
  last: "└─",
  expanded: "▾",
  collapsed: "▸",
  cursor: "▶",
} as const;

export const STATUS_ICON = {
  running: "●",
  waiting: "◐",
  finished: "●",
  idle: "○",
  starting: "○",
  error: "✕",
} as const;

export const PR_ICON = {
  approved: "✓",
  pending: "•",
  failing: "✕",
  merged: "⇡",
  conflicts: "⚠",
  changes: "↩",
} as const;

export const SPINNER_FRAMES = [
  "⠋",
  "⠙",
  "⠹",
  "⠸",
  "⠼",
  "⠴",
  "⠦",
  "⠧",
  "⠇",
  "⠏",
] as const;

export const SPINNER_INTERVAL_MS = 100;

export const BRANCH_ICON = "";
