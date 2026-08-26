package jira

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Atlassian Document Format, rendered to markdown.
//
// v3 of the API serves descriptions and comments as ADF — a JSON document tree —
// not as text. Something has to turn that into prose an agent can read, and the
// alternatives were both worse: `expand=renderedFields` returns HTML, which
// needs an HTML-to-markdown pass and loses the structured media references;
// v2 returns wiki markup, which is a different markup language to convert and
// which Atlassian is moving away from.
//
// Walking the tree is the only option that also solves the image problem, since
// media nodes are where inline screenshots are named.

// adfNode is one node of an ADF document. The shape is uniform — type, optional
// attrs, optional marks, optional children — which is what makes a single
// recursive walk possible.
type adfNode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text"`
	Attrs   map[string]any `json:"attrs"`
	Marks   []adfMark      `json:"marks"`
	Content []adfNode      `json:"content"`
}

type adfMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs"`
}

// mediaResolver turns an ADF media node into a markdown image link, or into ""
// when it cannot place one.
//
// It exists because the mapping is genuinely unavailable from the API: a media
// node's attrs.id is a Media Services file id, NOT the attachment id the REST
// attachment list is keyed by, and Atlassian has open requests to add the
// mapping (JRACLOUD-96383/96384). Alt text is the only bridge, and Jira often
// writes none. That is why fleet downloads every image attachment rather than
// only the referenced ones — see documentImages.
type mediaResolver func(attrs map[string]any) string

// renderADF converts an ADF document to markdown.
//
// An empty or unparseable document yields "" rather than an error: a ticket
// with no description is ordinary, and a description fleet cannot read is still
// a ticket worth materializing for its comments and its metadata.
func renderADF(raw json.RawMessage, media mediaResolver) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// A v2-style plain-string body, which Jira still returns for some fields
	// and which some integrations write. Already markdown-ish; pass it through.
	var asText string
	if json.Unmarshal(raw, &asText) == nil {
		return strings.TrimSpace(asText)
	}
	var doc adfNode
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	var b strings.Builder
	writeBlocks(&b, doc.Content, media, "")
	return strings.TrimSpace(b.String())
}

// writeBlocks renders a run of block-level nodes, separating them with a blank
// line. indent prefixes every emitted line, which is how nested list content
// and blockquotes stay inside their parent.
func writeBlocks(b *strings.Builder, nodes []adfNode, media mediaResolver, indent string) {
	first := true
	for _, n := range nodes {
		out := renderBlock(n, media, indent)
		if strings.TrimSpace(out) == "" {
			continue
		}
		if !first {
			b.WriteString("\n\n")
		}
		b.WriteString(out)
		first = false
	}
}

func renderBlock(n adfNode, media mediaResolver, indent string) string {
	switch n.Type {
	case "paragraph":
		return indent + renderInline(n.Content, media)

	case "heading":
		level := attrInt(n.Attrs, "level", 1)
		if level < 1 {
			level = 1
		}
		if level > 6 {
			level = 6
		}
		return indent + strings.Repeat("#", level) + " " + renderInline(n.Content, media)

	case "bulletList", "orderedList":
		return renderList(n, media, indent)

	case "codeBlock":
		lang := attrString(n.Attrs, "language")
		var body strings.Builder
		for _, c := range n.Content {
			body.WriteString(c.Text)
		}
		return indent + "```" + lang + "\n" + prefixLines(body.String(), indent) + "\n" + indent + "```"

	case "blockquote":
		var inner strings.Builder
		writeBlocks(&inner, n.Content, media, "")
		return prefixLines(inner.String(), indent+"> ")

	case "panel":
		// A panel is Jira's callout box. Its type ("info", "warning", "error",
		// "note", "success") is the only thing distinguishing it from a
		// blockquote, and it often carries the sentence that matters most.
		var inner strings.Builder
		writeBlocks(&inner, n.Content, media, "")
		label := strings.ToUpper(attrString(n.Attrs, "panelType"))
		if label == "" {
			label = "NOTE"
		}
		return prefixLines("**"+label+"**\n\n"+inner.String(), indent+"> ")

	case "rule":
		return indent + "---"

	case "table":
		return renderTable(n, media, indent)

	case "expand", "nestedExpand":
		// Collapsed content. The title is what the reader sees collapsed, so it
		// becomes a heading rather than being dropped — an expand is where
		// people put the log output that explains the bug.
		var inner strings.Builder
		writeBlocks(&inner, n.Content, media, indent)
		title := attrString(n.Attrs, "title")
		if title == "" {
			title = "Details"
		}
		return indent + "**" + title + "**\n\n" + inner.String()

	case "taskList", "decisionList":
		var lines []string
		for _, item := range n.Content {
			box := "- [ ] "
			if item.Type == "decisionItem" {
				box = "- "
			} else if attrString(item.Attrs, "state") == "DONE" {
				box = "- [x] "
			}
			lines = append(lines, indent+box+renderInline(item.Content, media))
		}
		return strings.Join(lines, "\n")

	case "mediaSingle", "mediaGroup":
		var parts []string
		for _, c := range n.Content {
			if link := renderBlock(c, media, indent); link != "" {
				parts = append(parts, link)
			}
		}
		return strings.Join(parts, "\n")

	case "media", "mediaInline":
		if media == nil {
			return ""
		}
		return indent + media(n.Attrs)

	case "text", "emoji", "mention", "inlineCard", "status", "date", "hardBreak":
		// An inline node reached where a block was expected. Render it rather
		// than dropping it: some producers put a bare text node at doc level.
		return indent + renderInline([]adfNode{n}, media)
	}

	// Unknown block: recurse rather than drop. ADF gains node types (bodied
	// sync blocks, multi-bodied extensions) faster than fleet will track them,
	// and their text usually lives in an ordinary content array underneath.
	if len(n.Content) > 0 {
		var inner strings.Builder
		writeBlocks(&inner, n.Content, media, indent)
		return inner.String()
	}
	return ""
}

func renderList(n adfNode, media mediaResolver, indent string) string {
	ordered := n.Type == "orderedList"
	start := attrInt(n.Attrs, "order", 1)
	var lines []string
	for i, item := range n.Content {
		marker := "- "
		if ordered {
			marker = fmt.Sprintf("%d. ", start+i)
		}
		// A list item's own content is blocks: the first is the item's text,
		// any others (a nested list, a code block) are indented under it.
		var head string
		var rest []adfNode
		if len(item.Content) > 0 {
			if item.Content[0].Type == "paragraph" {
				head = renderInline(item.Content[0].Content, media)
			} else {
				head = renderBlock(item.Content[0], media, "")
			}
			rest = item.Content[1:]
		}
		lines = append(lines, indent+marker+head)
		if len(rest) > 0 {
			var inner strings.Builder
			writeBlocks(&inner, rest, media, indent+strings.Repeat(" ", len(marker)))
			if s := inner.String(); strings.TrimSpace(s) != "" {
				lines = append(lines, s)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// renderTable emits a markdown table.
//
// Cell content is flattened to one line, because a markdown table cell cannot
// hold a block: a multi-paragraph cell becomes its paragraphs joined by a
// space. That loses formatting and keeps the data, which is the right trade for
// a reader who mostly wants to know what is in the grid.
func renderTable(n adfNode, media mediaResolver, indent string) string {
	var rows [][]string
	for _, r := range n.Content {
		if r.Type != "tableRow" {
			continue
		}
		var cells []string
		for _, c := range r.Content {
			var inner strings.Builder
			writeBlocks(&inner, c.Content, media, "")
			cell := strings.Join(strings.Fields(strings.ReplaceAll(inner.String(), "|", "\\|")), " ")
			cells = append(cells, cell)
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return ""
	}
	width := 0
	for _, r := range rows {
		if len(r) > width {
			width = len(r)
		}
	}
	pad := func(r []string) string {
		for len(r) < width {
			r = append(r, "")
		}
		return indent + "| " + strings.Join(r, " | ") + " |"
	}
	var b strings.Builder
	b.WriteString(pad(rows[0]))
	b.WriteString("\n" + indent + "|" + strings.Repeat(" --- |", width))
	for _, r := range rows[1:] {
		b.WriteString("\n" + pad(r))
	}
	return b.String()
}

// renderInline renders a run of inline nodes.
func renderInline(nodes []adfNode, media mediaResolver) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.Type {
		case "text":
			b.WriteString(applyMarks(n.Text, n.Marks))
		case "hardBreak":
			b.WriteString("\n")
		case "emoji":
			// text is the emoji itself; shortName ("​:tada:") is the fallback.
			if s := attrString(n.Attrs, "text"); s != "" {
				b.WriteString(s)
			} else {
				b.WriteString(attrString(n.Attrs, "shortName"))
			}
		case "mention":
			b.WriteString("@" + strings.TrimPrefix(attrString(n.Attrs, "text"), "@"))
		case "date":
			b.WriteString(attrString(n.Attrs, "timestamp"))
		case "status":
			b.WriteString("[" + strings.ToUpper(attrString(n.Attrs, "text")) + "]")
		case "inlineCard", "blockCard", "embedCard":
			if u := attrString(n.Attrs, "url"); u != "" {
				b.WriteString("<" + u + ">")
			}
		case "media", "mediaInline":
			if media != nil {
				b.WriteString(media(n.Attrs))
			}
		default:
			if len(n.Content) > 0 {
				b.WriteString(renderInline(n.Content, media))
			} else {
				b.WriteString(n.Text)
			}
		}
	}
	return b.String()
}

// applyMarks wraps text in its markdown equivalents.
//
// Order matters: code is applied innermost and suppresses the others, because
// `**x**` inside a code span is literal backtick-asterisk text rather than bold
// code. underline, textColor, subsup and border have no markdown form and are
// dropped — silently, since the words survive and a marker for "this was
// purple" helps nobody.
func applyMarks(text string, marks []adfMark) string {
	if text == "" {
		return ""
	}
	var link string
	code := false
	strong, em, strike := false, false, false
	for _, m := range marks {
		switch m.Type {
		case "code":
			code = true
		case "strong":
			strong = true
		case "em":
			em = true
		case "strike":
			strike = true
		case "link":
			link = attrString(m.Attrs, "href")
		}
	}
	out := text
	if code {
		out = "`" + out + "`"
	} else {
		if strong {
			out = "**" + out + "**"
		}
		if em {
			out = "_" + out + "_"
		}
		if strike {
			out = "~~" + out + "~~"
		}
	}
	if link != "" {
		out = "[" + out + "](" + link + ")"
	}
	return out
}

func prefixLines(s, prefix string) string {
	if prefix == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = strings.TrimRight(prefix, " ")
			continue
		}
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func attrString(attrs map[string]any, key string) string {
	if attrs == nil {
		return ""
	}
	s, _ := attrs[key].(string)
	return s
}

func attrInt(attrs map[string]any, key string, def int) int {
	if attrs == nil {
		return def
	}
	// JSON numbers decode into any as float64.
	if f, ok := attrs[key].(float64); ok {
		return int(f)
	}
	return def
}
