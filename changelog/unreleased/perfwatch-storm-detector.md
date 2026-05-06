---
type: added
---
Storm detector in perfwatch: dumps a snapshot when sustained Update() throughput exceeds 200/s, catching tea.Cmd loops that flood the loop without any single Update going slow (the stall watchdog can't see those)
