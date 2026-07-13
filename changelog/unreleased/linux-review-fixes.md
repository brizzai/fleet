---
type: fixed
---
- **Copies land on Linux.** Copy-mode selections now check that a clipboard tool's display server is actually reachable (`WAYLAND_DISPLAY`/`DISPLAY`) instead of silently piping into a tool that can't connect — headless and SSH sessions fall back to OSC 52 as intended.
- **Idle-suspend works on Linux.** The memory-pressure probe behind idle-session suspend now reads Linux's PSI (`/proc/pressure/memory`) and swap stats; previously it was macOS-only and Linux sessions were never protected from OOM.
