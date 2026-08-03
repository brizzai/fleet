---
type: fixed
---
**OpenCode question prompts show as waiting again.** OpenCode 1.14 moved `AskUserQuestion` prompts onto a separate `question.asked` event, so fleet's status plugin — which only watched `permission.asked` — left those sessions stuck on running. It now listens for both.
