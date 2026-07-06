---
type: added
---
Fleet can now auto-suspend idle sessions under memory pressure — freeing their memory (and the shared tmux server's buffers) so a big fleet can't OOM-crash all your sessions at once. Suspended sessions resume instantly on Enter with the conversation intact. Tune it in Settings › Behavior › "Idle-session suspend" (off/light/balanced/aggressive; default light).
