package session

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// Turn is one exchange from an agent's own transcript: what the user asked and
// what the agent finally said. Read for the agent-facing CLI, where a
// transcript beats pane scraping — it holds the full reply rather than the
// screenful that happens to be visible, and a prompt is never confused with an
// unsent draft.
type Turn struct {
	Prompt   string
	PromptAt time.Time
	// Reply is the agent's closing text: every text block after the turn's last
	// tool call. Empty while the turn is still working through tools.
	Reply   string
	ReplyAt time.Time
	// Tools counts the turn's tool calls.
	Tools int
	// Done is true once the turn ended: Claude recorded its duration, the user
	// interrupted it, or a later prompt began.
	Done        bool
	Interrupted bool
}

// turnTailStart and turnTailMax bound how much of a transcript ReadClaudeTurns
// reads: it starts with the last 256KB and doubles until it holds enough
// prompts, up to 16MB. A transcript is append-only and routinely hundreds of MB,
// so reading it whole for the last few turns is never the right trade.
const (
	turnTailStart = 256 << 10
	turnTailMax   = 16 << 20
)

// ReadClaudeTurns returns up to n of the most recent turns in a Claude
// transcript, oldest first. A missing file is not an error: it returns nil, the
// same as a session with no transcript.
func ReadClaudeTurns(path string, n int) ([]Turn, error) {
	if path == "" || n <= 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()

	for window := int64(turnTailStart); ; window *= 2 {
		start := max(0, size-window)
		buf := make([]byte, size-start)
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return nil, err
		}
		if start > 0 {
			// The first line is almost certainly cut; drop it.
			if nl := bytes.IndexByte(buf, '\n'); nl >= 0 {
				buf = buf[nl+1:]
			}
		}
		turns := parseClaudeTurns(buf)
		// A Turn exists only once its prompt line is parsed, so every turn
		// returned started inside the window; n of them is enough.
		if len(turns) >= n || start == 0 || window >= turnTailMax {
			if len(turns) > n {
				turns = turns[len(turns)-n:]
			}
			return turns, nil
		}
	}
}

// claudeLine is the subset of a transcript line the turn reader decodes.
type claudeLine struct {
	Type        string                 `json:"type"`
	Subtype     string                 `json:"subtype"`
	Timestamp   string                 `json:"timestamp"`
	IsSidechain bool                   `json:"isSidechain"`
	IsMeta      bool                   `json:"isMeta"`
	IsCompact   bool                   `json:"isCompactSummary"`
	Origin      *struct{ Kind string } `json:"origin"`
	Message     *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func parseClaudeTurns(data []byte) []Turn {
	var turns []Turn
	var cur *Turn
	var replyParts []string
	flush := func() {
		if cur == nil {
			return
		}
		cur.Reply = strings.TrimSpace(strings.Join(replyParts, "\n\n"))
		turns = append(turns, *cur)
	}

	for len(data) > 0 {
		var line []byte
		if nl := bytes.IndexByte(data, '\n'); nl >= 0 {
			line, data = data[:nl], data[nl+1:]
		} else {
			line, data = data, nil
		}
		// Cheap prefilter: only three line types matter, and most of a
		// transcript is tool output and bookkeeping.
		if !bytes.Contains(line, pfType("user")) && !bytes.Contains(line, pfType("assistant")) &&
			!bytes.Contains(line, []byte(`"turn_duration"`)) {
			continue
		}
		// Claude records an interrupt as a text block opening with this marker.
		// Matched structurally — with the block's own unescaped quotes — because
		// the bare words turn up in tool output (a grep, a file that mentions
		// them), where they are escaped inside a JSON string.
		if bytes.Contains(line, pfInterrupt) {
			// Newer Claude Code also writes "api-request-blob" lines replaying
			// earlier messages, interrupt markers included; only a top-level user
			// line is this turn's own.
			var head struct {
				Type        string `json:"type"`
				IsSidechain bool   `json:"isSidechain"`
			}
			if cur != nil && json.Unmarshal(line, &head) == nil && head.Type == "user" && !head.IsSidechain {
				cur.Done, cur.Interrupted = true, true
			}
			continue
		}
		// Tool results are the bulk of a transcript (whole files, command output)
		// and never a prompt, so they are not decoded at all.
		if bytes.Contains(line, []byte(`"tool_result"`)) {
			continue
		}
		var cl claudeLine
		if json.Unmarshal(line, &cl) != nil || cl.IsSidechain {
			continue
		}
		at := parseTranscriptTime(cl.Timestamp)

		switch cl.Type {
		case "system":
			if cl.Subtype == "turn_duration" && cur != nil {
				cur.Done = true
			}
		case "user":
			if cl.Message == nil || cl.IsMeta || cl.IsCompact {
				continue
			}
			text, isHuman := humanPromptText(cl, line)
			if !isHuman {
				continue
			}
			if cur != nil {
				cur.Done = true
			}
			flush()
			cur = &Turn{Prompt: text, PromptAt: at}
			replyParts = nil
		case "assistant":
			if cur == nil || cl.Message == nil {
				continue
			}
			var blocks []contentBlock
			if json.Unmarshal(cl.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				switch b.Type {
				case "tool_use":
					cur.Tools++
					replyParts = nil // only text after the last tool call is the reply
				case "text":
					if t := strings.TrimSpace(b.Text); t != "" {
						replyParts = append(replyParts, t)
						cur.ReplyAt = at
					}
				}
			}
		}
	}
	flush()
	return turns
}

func pfType(t string) []byte { return []byte(`"type":"` + t + `"`) }

// humanPromptText returns a user line's text and whether a person typed it, as
// opposed to a tool result or a slash command's bookkeeping. A slash command is
// a prompt too and reads back as "/name args".
func humanPromptText(cl claudeLine, line []byte) (string, bool) {
	text := contentText(cl.Message.Content)
	if cl.Origin != nil && cl.Origin.Kind != "human" {
		return text, false
	}
	if cl.Origin == nil && bytes.Contains(line, pfToolResultKey) {
		return text, false
	}
	t := strings.TrimSpace(text)
	switch {
	case t == "":
		return "", false
	case strings.HasPrefix(t, "<local-command-"), strings.HasPrefix(t, "[Request interrupted"):
		return t, false
	case strings.HasPrefix(t, "<command-name>") || strings.HasPrefix(t, "<command-message>"):
		name := between(t, "<command-name>", "</command-name>")
		if name == "" {
			return "", false
		}
		if args := strings.TrimSpace(between(t, "<command-args>", "</command-args>")); args != "" {
			name += " " + args
		}
		return name, true
	}
	return t, true
}

var (
	pfToolResultKey = []byte(`"toolUseResult"`)
	pfInterrupt     = []byte(`"text":"[Request interrupted by user`)
)

// contentText flattens message content — a plain string or a list of blocks —
// into its text.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func between(s, open, close string) string {
	_, rest, ok := strings.Cut(s, open)
	if !ok {
		return ""
	}
	inner, _, ok := strings.Cut(rest, close)
	if !ok {
		return ""
	}
	return inner
}

func parseTranscriptTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
