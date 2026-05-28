/**
 * Tokyo-night palette tokens (matches internal/ui/palette.go default theme)
 * so the in-page demo reads as the same theme a new fleet user sees.
 */
export const PALETTE = {
  bg: "#1a1b26",
  surface: "#24283b",
  surfaceElev: "#2a2f43",
  border: "#414868",
  accent: "#7aa2f7",
  text: "#c0caf5",
  textDim: "#565f89",

  // status
  green: "#9ece6a",
  yellow: "#e0af68",
  blue: "#7dcfff",
  red: "#f7768e",
  gray: "#565f89",
  orange: "#ff9e64",
  purple: "#bb9af7",
} as const;

import type { Status } from "./state";

export function statusColor(status: Status): string {
  switch (status) {
    case "running":
      return PALETTE.green;
    case "waiting":
      return PALETTE.yellow;
    case "finished":
      return PALETTE.blue;
    case "idle":
      return PALETTE.gray;
    case "starting":
      return PALETTE.accent;
    case "error":
      return PALETTE.red;
  }
}
