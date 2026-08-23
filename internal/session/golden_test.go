package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/debuglog"
)

// Golden file tests: real ANSI pane captures from tmux, tested through the
// full detection pipeline (StripANSI → extractRecentLines → detectStatus).
//
// To add a regression test when a bug is found:
//   1. tmux capture-pane -t <session> -p -e > internal/session/testdata/<name>.txt
//   2. Add an entry to goldenTests with the expected status
//   3. Run tests — should FAIL with the bug, PASS after fix

var goldenTests = []struct {
	fixture  string // filename in testdata/
	expected Status
	desc     string // what this fixture tests
}{
	{"pane_waiting_permission_3opt.txt", StatusWaiting, "3-option permission menu with Esc to cancel"},
	{"pane_waiting_permission_cursor_opt2.txt", StatusWaiting, "permission menu with cursor on option 2 (not option 1)"},
	{"pane_running_subagent_spelunking_up_arrow.txt", StatusRunning, "sub-agent (Explore) with whimsical `· ↑ tokens` output counter"},
	{"pane_finished_idle_prompt.txt", StatusFinished, "idle Claude prompt (❯)"},
	{"pane_finished_permission_mode.txt", StatusFinished, "permission mode bar (⏵⏵)"},
	{"pane_finished_conversation_whimsical_markers.txt", StatusFinished, "idle pane with scrollback text mentioning `· ↓`/`· ↑` + `tokens` (meta false-positive guard)"},
	{"pane_running_extended_thinking.txt", StatusRunning, "extended thinking with `· ↓ tokens · thinking with high effort)` format"},
	{"pane_running_stale_waiting_spinner.txt", StatusRunning, "active spinner (✳) with idle prompt — Claude running after permission approved"},
	{"pane_finished_cli_spinners_in_scrollback.txt", StatusFinished, "idle prompt with braille spinner chars from CLI tool output in scrollback"},
	{"pane_running_plan_checklist_deep.txt", StatusRunning, "plan execution with whimsical activity line pushed deep by expanding checklist"},
	{"pane_finished_quoted_crashdump_whimsical.txt", StatusFinished, "idle prompt with embedded crash-dump example containing a whimsical activity line in scrollback (~30 lines from bottom)"},
	{"pane_waiting_askuserquestion_checkbox_focus.txt", StatusWaiting, "AskUserQuestion dialog with focus on checkbox question header (no `❯ N.` cursor in numbered options)"},
	{"pane_waiting_askuserquestion_single_preview.txt", StatusWaiting, "single-question AskUserQuestion dialog with a side-by-side preview panel — the panel pushes `❯ 1.` to recentLines[19], past the menu check's 15-line window, and a one-question dialog omits the `Tab to switch questions` hint, so only the `n to add notes` + `Esc to cancel` footer pair identifies it"},
	{"pane_waiting_exitplanmode_approve.txt", StatusWaiting, "ExitPlanMode plan-approval menu — numbered options with `shift+tab to approve with this feedback` footer (no `Esc to cancel`)"},
	{"finished_idle_background_agent.txt", StatusRunning, "lead parked (Stop) but a background sub-agent is still in-flight in the dock (`◯ databento-operator … 27m 30s · ↓ 93.8k tokens`) → running"},
	{"pane_running_background_agents_multi.txt", StatusRunning, "three in-flight background Explore agents in the dock (`◯ Explore … · ↓ tokens`) while the lead is parked → running"},
	{"pane_finished_quoted_dock_row.txt", StatusFinished, "idle pane whose conversation quotes a dock row verbatim (`◯ Explore … · ↓ tokens`) above the chrome — must NOT read as running (position guard: dock only scanned in bottom 3 lines)"},
	{"pane_waiting_permission_with_dock.txt", StatusWaiting, "permission menu with live dock rows below it — waiting must win over the dock's running (detectWaiting precedes the dock check)"},
	{"pane_finished_prose_mentions_askuserquestion_footer.txt", StatusFinished, "idle pane whose conversation summary describes the AskUserQuestion footer in prose (`… footer (n to add notes + Esc to cancel), so a dialog whose …`) — both hints on one line, 11 lines from the bottom. Substring matching read this as a live dialog and pinned the finished session to waiting; the hints must be whole fields of the footer's `·`-separated list"},
	{"pane_running_compacting.txt", StatusRunning, "mid-`/compact` pane — the compaction activity line (`· Compacting conversation… (2m 27s)`) is matched now that the activity check no longer requires a token counter, so the pane reads running on its own instead of leaning entirely on the PreCompact hook. Safe because the line is replaced by the `⎿  Compacted (ctrl+o …)` result row on completion rather than left in scrollback — no captured pane holds both"},
}

func TestGoldenDetection(t *testing.T) {
	debuglog.Init()

	for _, tt := range goldenTests {
		t.Run(tt.fixture, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tt.fixture))
			if err != nil {
				t.Fatalf("failed to load fixture %s: %v", tt.fixture, err)
			}

			raw := string(data)
			stripped := StripANSI(raw)

			log := debuglog.Logger
			result := detectStatus(stripped, log)

			if result != tt.expected {
				t.Errorf("fixture %s (%s):\n  expected: %q\n  got:      %q", tt.fixture, tt.desc, tt.expected, result)

				// Dump debug info for diagnosis.
				lines := strings.Split(strings.TrimRight(stripped, "\n"), "\n")
				recent := extractRecentLines(lines, 50)
				limit := 10
				if limit > len(recent) {
					limit = len(recent)
				}
				t.Logf("bottom %d recent lines:", limit)
				for i := 0; i < limit; i++ {
					t.Logf("  [%d] %q", i, recent[i])
				}
			}
		})
	}
}

// TestGoldenANSIStripping verifies that StripANSI produces clean output
// from real ANSI captures (no escape sequences remain).
func TestGoldenANSIStripping(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("failed to read testdata: %v", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", entry.Name()))
			if err != nil {
				t.Fatalf("failed to load %s: %v", entry.Name(), err)
			}

			stripped := StripANSI(string(data))

			if strings.ContainsRune(stripped, '\x1b') {
				t.Errorf("stripped output still contains ESC (\\x1b)")
			}
			if strings.ContainsRune(stripped, '\x9B') {
				t.Errorf("stripped output still contains C1 CSI (\\x9B)")
			}
		})
	}
}
