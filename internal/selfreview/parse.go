package selfreview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrNoJSON means the reply held no review object at all — the case the
// runner's one repair retry exists for.
var ErrNoJSON = errors.New("reviewer reply holds no review JSON")

// Envelope is the part of `claude -p --output-format json`'s single result
// object fleet reads. The model's own text is in Result; StructuredOutput is
// filled instead when --json-schema was used.
type Envelope struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	SessionID        string          `json:"session_id"`
	NumTurns         int             `json:"num_turns"`
	DurationMS       int64           `json:"duration_ms"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
}

// ParseEnvelope reads claude's JSON envelope. ok is false when raw is not one
// (plain text output, or the model's JSON straight through), in which case the
// caller treats raw as the model's text.
//
// The single object (--output-format json), an array of events (what
// --verbose turns it into) and one event per line (stream-json) are all
// accepted; the last "result" event wins.
func ParseEnvelope(raw []byte) (env Envelope, ok bool) {
	events := replyEvents(raw)
	for i := len(events) - 1; i >= 0; i-- {
		var e Envelope
		if json.Unmarshal(events[i], &e) == nil && e.Type == "result" {
			return e, true
		}
	}
	return Envelope{}, false
}

// replyEvents splits claude's output into its JSON events: one object, an
// array of them, or a stream of them. Anything that does not start as JSON
// (the model's prose, a fenced block) yields none.
func replyEvents(raw []byte) []json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '{' && raw[0] != '[') {
		return nil
	}
	if raw[0] == '[' {
		var events []json.RawMessage
		if json.Unmarshal(raw, &events) != nil {
			return nil
		}
		return events
	}
	var events []json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var e json.RawMessage
		if dec.Decode(&e) != nil || len(e) == 0 || e[0] != '{' {
			return events
		}
		events = append(events, e)
	}
}

// toolEvent is the part of a verbose "assistant" event fleet reads: the
// tool_use blocks in its message.
type toolEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input struct {
				FilePath string `json:"file_path"`
				Path     string `json:"path"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// touch is one path a tool call named, in call order.
type touch struct {
	path string
	read bool
}

// readMeta builds the reviewer record from claude's events: the result
// event's figures, and every tool_use block's name and path. With the plain
// single-object envelope there are no tool events, so Tools stays empty —
// which is exactly why the runner asks for --verbose.
func readMeta(events []json.RawMessage) ReviewerMeta {
	var m ReviewerMeta
	for _, raw := range events {
		var e toolEvent
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		switch e.Type {
		case "assistant":
			for _, c := range e.Message.Content {
				if c.Type != "tool_use" || c.Name == "" {
					continue
				}
				if m.Tools == nil {
					m.Tools = map[string]int{}
				}
				m.Tools[c.Name]++
				switch c.Name {
				case "Read":
					m.touched = append(m.touched, touch{path: c.Input.FilePath, read: true})
				case "Grep", "Glob":
					m.touched = append(m.touched, touch{path: c.Input.Path})
				}
			}
		case "result":
			var r Envelope
			if json.Unmarshal(raw, &r) == nil {
				m.Turns, m.CostUSD, m.DurationMS = r.NumTurns, r.TotalCostUSD, int(r.DurationMS)
			}
		}
	}
	m.Relativize("")
	return m
}

// Relativize rewrites the touched paths relative to dir (the worktree the
// reviewer ran in) and recomputes Files and FilesRead. A path outside dir is
// kept as it was; dir "" only dedupes.
func (m *ReviewerMeta) Relativize(dir string) {
	m.Files, m.FilesRead = nil, 0
	seen := map[string]bool{}
	read := map[string]bool{}
	for _, t := range m.touched {
		p := cleanLine(t.path)
		if p == "" {
			continue
		}
		if dir != "" {
			if rel, err := filepath.Rel(dir, p); err == nil && filepath.IsAbs(p) && !strings.HasPrefix(rel, "..") {
				p = filepath.ToSlash(rel)
			}
		}
		if t.read && !read[p] {
			read[p] = true
			m.FilesRead++
		}
		if !seen[p] {
			seen[p] = true
			m.Files = append(m.Files, p)
		}
	}
}

// add folds another call's record into m (the repair retry): its turns, cost
// and time count, and its tool calls, if any, join the list.
func (m *ReviewerMeta) add(o ReviewerMeta) {
	m.Turns += o.Turns
	m.CostUSD += o.CostUSD
	m.DurationMS += o.DurationMS
	for k, v := range o.Tools {
		if m.Tools == nil {
			m.Tools = map[string]int{}
		}
		m.Tools[k] += v
	}
	m.touched = append(m.touched, o.touched...)
	m.Relativize("")
}

// Parse reads a reviewer reply into a Run, plus what the reviewer call did
// (ReviewerMeta: turns, cost, time, tool calls — the tool calls only when the
// reply is a verbose event list). It tolerates everything a model wraps JSON
// in — claude's envelope, ``` fences, a sentence before or after — but not a
// reply with no review object in it (ErrNoJSON), and not an envelope that
// reports an error.
//
// Parse does not apply fleet's rules; Enforce does.
func Parse(raw []byte) (*Run, ReviewerMeta, error) {
	text := raw
	events := replyEvents(raw)
	meta := readMeta(events)
	if env, ok := ParseEnvelope(raw); ok {
		if env.IsError {
			reason := firstLine(env.Result)
			if reason == "" {
				reason = env.Subtype
			}
			return nil, meta, fmt.Errorf("claude: %s", reason)
		}
		if len(env.StructuredOutput) > 0 && string(env.StructuredOutput) != "null" {
			text = env.StructuredOutput
		} else {
			text = []byte(env.Result)
		}
	}
	obj := extractObject(text)
	if obj == nil {
		return nil, meta, ErrNoJSON
	}
	var run Run
	if err := json.Unmarshal(obj, &run); err != nil {
		return nil, meta, fmt.Errorf("%w: %v", ErrNoJSON, err)
	}
	if run.V == 0 {
		run.V = SchemaVersion
	}
	if run.V > SchemaVersion {
		return nil, meta, fmt.Errorf("reviewer wrote schema v%d, fleet reads v%d", run.V, SchemaVersion)
	}
	return &run, meta, nil
}

// extractObject finds the first JSON object in text that looks like a review
// (has a verdict or stops key). Trying every '{' in turn is what makes a
// fenced block, leading prose, or a stray brace in that prose harmless.
func extractObject(text []byte) []byte {
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(text[i:]))
		var m map[string]json.RawMessage
		if err := dec.Decode(&m); err != nil {
			continue
		}
		_, hasVerdict := m["verdict"]
		_, hasStops := m["stops"]
		if hasVerdict || hasStops {
			return text[i : i+int(dec.InputOffset())]
		}
	}
	return nil
}

func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
