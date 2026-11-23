package selfreview

import (
	"errors"
	"strings"
	"testing"
)

const minimalReview = `{"v":2,"verdict":"Ready once the default is settled.","summary":"Renames one helper.","risk":"low","intent_gap":"","stops":[],"skipped":[{"path":"a.go","reason":"rename only","scope":"file"}],"pending":[],"chapters":[{"id":"c1","title":"The rename","role":"heart","text":"One helper gets a clearer name.","files":["a.go"]}]}`

func TestParseToleratesWrapping(t *testing.T) {
	cases := map[string]string{
		"bare":           minimalReview,
		"fenced":         "```json\n" + minimalReview + "\n```",
		"prose around":   "Here you go:\n\n" + minimalReview + "\n\nHope that helps {really}.",
		"stray brace":    "Note {this} first.\n```\n" + minimalReview + "\n```",
		"envelope":       `{"type":"result","subtype":"success","is_error":false,"result":` + jsonString("```json\n"+minimalReview+"\n```") + `,"session_id":"x","total_cost_usd":0.1}`,
		"event array":    `[{"type":"system","subtype":"init"},{"type":"result","subtype":"success","is_error":false,"result":` + jsonString(minimalReview) + `}]`,
		"stream-json":    `{"type":"system","subtype":"init"}` + "\n" + `{"type":"result","subtype":"success","is_error":false,"result":` + jsonString(minimalReview) + `}` + "\n",
		"structured out": `{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":` + minimalReview + `}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			run, _, err := Parse([]byte(raw))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if run.Verdict != "Ready once the default is settled." || len(run.Skipped) != 1 {
				t.Errorf("got %+v", run)
			}
		})
	}
}

func TestParseEnvelopeErrorIsAnError(t *testing.T) {
	raw := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"You've hit your session limit · resets 3:50pm"}`
	_, _, err := Parse([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "session limit") {
		t.Fatalf("err = %v, want the envelope's reason", err)
	}
	if errors.Is(err, ErrNoJSON) {
		t.Error("an error envelope must not trigger the JSON repair retry")
	}
}

func TestParseNoJSON(t *testing.T) {
	for _, raw := range []string{"", "I could not review this.", `{"foo":1}`, "```json\n{\"verdict\": \"cut off"} {
		if _, _, err := Parse([]byte(raw)); !errors.Is(err, ErrNoJSON) {
			t.Errorf("Parse(%q) err = %v, want ErrNoJSON", raw, err)
		}
	}
}

func TestParseFutureSchemaRefused(t *testing.T) {
	if _, _, err := Parse([]byte(`{"v":3,"verdict":"x","stops":[]}`)); err == nil {
		t.Fatal("a v3 reply parsed as v2")
	}
	// A stored v1 run still reads: the v2 fields are simply absent.
	run, _, err := Parse([]byte(`{"v":1,"verdict":"x","stops":[{"id":"s1","title":"t","decide":"d?"}]}`))
	if err != nil || run.Summary != "" || run.Stops[0].Recommend != nil || run.Chapters != nil {
		t.Fatalf("v1 run: %v %+v", err, run)
	}
}

// One sloppy field must not cost the whole review.
func TestParseLenientFields(t *testing.T) {
	raw := `{"verdict":"x","stops":[{"id":"s1","title":"t","decide":"d?","checked":["bare string"],
	  "hunks":[{"path":"a.go","start":"12","end":14.0}]}]}`
	run, _, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if run.V != SchemaVersion {
		t.Errorf("missing v = %d, want %d", run.V, SchemaVersion)
	}
	h := run.Stops[0].Hunks[0]
	if h.Start != 12 || h.End != 14 {
		t.Errorf("hunk = %+v, want 12-14", h)
	}
	if c := run.Stops[0].Checked[0]; c.Text != "bare string" || c.Src != "" {
		t.Errorf("checked = %+v", c)
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
