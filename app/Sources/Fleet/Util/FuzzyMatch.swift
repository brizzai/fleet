import Foundation

// FuzzyMatch ports the TUI's fuzzy-search scoring from
// internal/ui/command_palette.go:256-281. Characters in the query must appear
// in order in the target. Score gets bonuses for consecutive matches (+2)
// and word-boundary matches (+3). Used by the command palette to rank actions.
enum FuzzyMatch {
    static func match(query: String, target: String) -> (matches: Bool, score: Int) {
        if query.isEmpty { return (true, 0) }
        let q = Array(query.lowercased())
        let t = Array(target.lowercased())
        var qi = 0
        var score = 0
        var prevMatch = false
        for ti in 0..<t.count where qi < q.count {
            if t[ti] == q[qi] {
                qi += 1
                score += 1
                if prevMatch {
                    score += 2
                }
                if ti == 0 || t[ti - 1] == " " {
                    score += 3
                }
                prevMatch = true
            } else {
                prevMatch = false
            }
        }
        return (qi == q.count, score)
    }
}
