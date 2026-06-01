---
type: improved
---

Session titles now use Claude's own model-generated title. fleet reads Claude Code's `ai-title` from the conversation transcript — the same title that evolves as the work shifts — instead of guessing from your first prompt and re-running a heuristic every few prompts. An in-session `/rename` (`custom-title`) still takes precedence, and fleet's `R` rename always wins. When Claude writes no title (e.g. title generation disabled), the old prompt heuristic remains as a fallback. Claude's occasional kebab-case slug titles (e.g. `native-ai-title-integration`) are spaced out for readability (`native ai title integration`) with their casing preserved, so acronyms like `API` survive.
