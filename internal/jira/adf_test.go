package jira

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/ticket"
)

// TestRenderADF covers the node types a real Jira description actually uses.
//
// Table-driven against small documents rather than one golden file, because
// what matters is that each node type survives on its own: a golden file fails
// as one lump and says nothing about which node broke.
func TestRenderADF(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{
			"paragraph with marks",
			`{"type":"doc","content":[{"type":"paragraph","content":[
				{"type":"text","text":"plain "},
				{"type":"text","text":"bold","marks":[{"type":"strong"}]},
				{"type":"text","text":" and "},
				{"type":"text","text":"code","marks":[{"type":"code"}]}]}]}`,
			"plain **bold** and `code`",
		},
		{
			// code wins over every other mark: `**x**` inside a code span is
			// literal backtick-asterisk text, not bold code.
			"code suppresses other marks",
			`{"type":"doc","content":[{"type":"paragraph","content":[
				{"type":"text","text":"x","marks":[{"type":"code"},{"type":"strong"},{"type":"em"}]}]}]}`,
			"`x`",
		},
		{
			"link",
			`{"type":"doc","content":[{"type":"paragraph","content":[
				{"type":"text","text":"docs","marks":[{"type":"link","attrs":{"href":"https://x.example"}}]}]}]}`,
			"[docs](https://x.example)",
		},
		{
			"heading",
			`{"type":"doc","content":[{"type":"heading","attrs":{"level":3},
				"content":[{"type":"text","text":"Steps"}]}]}`,
			"### Steps",
		},
		{
			"bullet list",
			`{"type":"doc","content":[{"type":"bulletList","content":[
				{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},
				{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}]}]}`,
			"- one\n- two",
		},
		{
			"ordered list starts where Jira says",
			`{"type":"doc","content":[{"type":"orderedList","attrs":{"order":3},"content":[
				{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"third"}]}]}]}]}`,
			"3. third",
		},
		{
			"code block keeps its language",
			`{"type":"doc","content":[{"type":"codeBlock","attrs":{"language":"go"},
				"content":[{"type":"text","text":"func main() {}"}]}]}`,
			"```go\nfunc main() {}\n```",
		},
		{
			"blockquote",
			`{"type":"doc","content":[{"type":"blockquote","content":[
				{"type":"paragraph","content":[{"type":"text","text":"quoted"}]}]}]}`,
			"> quoted",
		},
		{
			// A panel's type is the only thing distinguishing it from a
			// blockquote, and it is often the sentence that matters most.
			"panel keeps its kind",
			`{"type":"doc","content":[{"type":"panel","attrs":{"panelType":"warning"},"content":[
				{"type":"paragraph","content":[{"type":"text","text":"careful"}]}]}]}`,
			"> **WARNING**\n>\n> careful",
		},
		{
			"rule",
			`{"type":"doc","content":[{"type":"rule"}]}`,
			"---",
		},
		{
			"hard break inside a paragraph",
			`{"type":"doc","content":[{"type":"paragraph","content":[
				{"type":"text","text":"a"},{"type":"hardBreak"},{"type":"text","text":"b"}]}]}`,
			"a\nb",
		},
		{
			"mention and emoji",
			`{"type":"doc","content":[{"type":"paragraph","content":[
				{"type":"mention","attrs":{"text":"@Dana"}},
				{"type":"text","text":" "},
				{"type":"emoji","attrs":{"text":"🎉","shortName":":tada:"}}]}]}`,
			"@Dana 🎉",
		},
		{
			"inline card becomes its url",
			`{"type":"doc","content":[{"type":"paragraph","content":[
				{"type":"inlineCard","attrs":{"url":"https://x.example/pr/1"}}]}]}`,
			"<https://x.example/pr/1>",
		},
		{
			"task list",
			`{"type":"doc","content":[{"type":"taskList","content":[
				{"type":"taskItem","attrs":{"state":"DONE"},"content":[{"type":"text","text":"done bit"}]},
				{"type":"taskItem","attrs":{"state":"TODO"},"content":[{"type":"text","text":"todo bit"}]}]}]}`,
			"- [x] done bit\n- [ ] todo bit",
		},
		{
			"table",
			`{"type":"doc","content":[{"type":"table","content":[
				{"type":"tableRow","content":[
					{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"env"}]}]},
					{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"status"}]}]}]},
				{"type":"tableRow","content":[
					{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"prod"}]}]},
					{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"down"}]}]}]}]}]}`,
			"| env | status |\n| --- | --- |\n| prod | down |",
		},
		{
			"expand keeps its title and body",
			`{"type":"doc","content":[{"type":"expand","attrs":{"title":"Logs"},"content":[
				{"type":"paragraph","content":[{"type":"text","text":"stacktrace"}]}]}]}`,
			"**Logs**\n\nstacktrace",
		},
		{
			// ADF gains node types faster than fleet will track them, and their
			// text usually lives in an ordinary content array underneath. Losing
			// the whole paragraph would be a silent hole in a bug report.
			"unknown block recurses instead of dropping",
			`{"type":"doc","content":[{"type":"bodiedSyncBlock","content":[
				{"type":"paragraph","content":[{"type":"text","text":"still here"}]}]}]}`,
			"still here",
		},
		{
			// Jira still returns plain strings for some bodies, and some
			// integrations write them. Already markdown-ish; pass them through.
			"plain string body",
			`"just text"`,
			"just text",
		},
		{
			"absent description renders empty, not \"null\"",
			`null`,
			"",
		},
		{
			"unparseable document degrades to empty",
			`{"type":"doc","content":`,
			"",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderADF(json.RawMessage(c.doc), nil)
			if got != c.want {
				t.Errorf("renderADF =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// TestRenderADFPlacesMedia pins the half of the image story that lives in ADF.
//
// A media node's attrs.id is a Media Services file id and is NOT the attachment
// id the REST list is keyed by (JRACLOUD-96383), so alt text is the only bridge
// — and Jira frequently writes none. Both outcomes have to be legible: a
// resolved image becomes a placeholder the downloader rewrites, and an
// unresolvable one still tells the agent that something visual belongs there.
func TestRenderADFPlacesMedia(t *testing.T) {
	media := func(attrs map[string]any) string {
		if alt, _ := attrs["alt"].(string); alt == "screenshot.png" {
			return "![screenshot.png](" + ticket.PlaceholderFor(0) + ")"
		}
		return "_(image — see Attachments)_"
	}

	doc := `{"type":"doc","content":[
		{"type":"mediaSingle","content":[{"type":"media","attrs":{"id":"uuid-1","alt":"screenshot.png"}}]},
		{"type":"mediaSingle","content":[{"type":"media","attrs":{"id":"uuid-2"}}]}]}`

	got := renderADF(json.RawMessage(doc), media)
	if !strings.Contains(got, ticket.PlaceholderFor(0)) {
		t.Errorf("a resolvable media node should become a placeholder:\n%s", got)
	}
	if !strings.Contains(got, "see Attachments") {
		t.Errorf("an unresolvable media node must still say an image belongs here:\n%s", got)
	}
	// No raw media id may reach ticket.md — it names nothing an agent can open.
	if strings.Contains(got, "uuid-2") {
		t.Errorf("a media services id leaked into the body:\n%s", got)
	}
}

// TestRenderBodyDownloadsEveryImageAttachment pins the decision the Atlassian
// limitation forces.
//
// Matching inline-only would silently drop the screenshot on exactly the bug
// reports that are mostly screenshot, because Jira often writes no alt text.
// Downloading everything and listing it under ## Attachments costs at worst an
// unreferenced image, inside caps ticket already enforces.
func TestRenderBodyDownloadsEveryImageAttachment(t *testing.T) {
	iss := &issue{
		Key: "BRZ-1",
		Fields: issueFields{
			Summary: "Broken",
			Description: json.RawMessage(`{"type":"doc","content":[
				{"type":"mediaSingle","content":[{"type":"media","attrs":{"alt":"named.png"}}]}]}`),
			Attachment: []attachment{
				{ID: "1", Filename: "named.png", MimeType: "image/png", Content: "https://acme.atlassian.net/a/1"},
				{ID: "2", Filename: "unreferenced.png", MimeType: "image/png", Content: "https://acme.atlassian.net/a/2"},
				{ID: "3", Filename: "notes.pdf", MimeType: "application/pdf", Content: "https://acme.atlassian.net/a/3"},
			},
		},
	}

	body, images := renderBody(iss, nil)

	if len(images) != 2 {
		t.Fatalf("got %d images, want both image attachments regardless of what the body references", len(images))
	}
	// The referenced one is inline where it belongs...
	if !strings.Contains(body, "![named.png]("+ticket.PlaceholderFor(0)+")") {
		t.Errorf("the resolvable image is not inline:\n%s", body)
	}
	// ...and reported as such in the list rather than repeated.
	if !strings.Contains(body, "named.png (shown above)") {
		t.Errorf("an inlined image should not be repeated in the list:\n%s", body)
	}
	// The unreferenced one is the whole point: it still reaches the agent.
	if !strings.Contains(body, "![unreferenced.png]("+ticket.PlaceholderFor(1)+")") {
		t.Errorf("an unreferenced image attachment must still be delivered:\n%s", body)
	}
	// A non-image is named, never fetched: an agent cannot read it, and pulling
	// a customer's spreadsheet into a worktree unasked is not fleet's call.
	if !strings.Contains(body, "notes.pdf (application/pdf, not downloaded)") {
		t.Errorf("non-image attachments should be listed, not fetched:\n%s", body)
	}
	for _, img := range images {
		if strings.HasSuffix(img.URL, "/a/3") {
			t.Error("a PDF was queued for download")
		}
	}
}

// TestRenderBodyShapeMatchesLinear keeps the two trackers producing the same
// ticket.md, so the seeded prompt describes both truthfully.
func TestRenderBodyShapeMatchesLinear(t *testing.T) {
	iss := &issue{Key: "BRZ-1", Fields: issueFields{
		Summary:     "Broken",
		Description: json.RawMessage(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"desc"}]}]}`),
		Subtasks:    []issue{{Key: "brz-2", Fields: issueFields{Summary: "sub"}}},
		IssueLinks: []issueLink{
			{Type: linkType{Outward: "blocks"}, OutwardIssue: &issue{Key: "brz-9", Fields: issueFields{Summary: "the release"}}},
			{Type: linkType{Inward: "is blocked by"}, InwardIssue: &issue{Key: "brz-8", Fields: issueFields{Summary: "the migration"}}},
		},
	}}
	comments := &commentPage{Comments: []comment{{
		Author: &struct {
			DisplayName string `json:"displayName"`
		}{DisplayName: "Dana"},
		Created: "2026-08-25T09:41:22.113+0000",
		Body:    json.RawMessage(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"still broken"}]}]}`),
	}}}

	body, _ := renderBody(iss, comments)

	for _, want := range []string{
		"## Description",
		"desc",
		"## Comments (1)",
		"### Dana — 2026-08-25 09:41",
		"still broken",
		"## Sub-tasks",
		"- BRZ-2 — sub",
		"## Links",
		// The DIRECTION is the whole content of a link: "blocks the release"
		// and "is blocked by the release" are opposite facts, and a bare list
		// of keys would drop the difference.
		"- blocks BRZ-9 — the release",
		"- is blocked by BRZ-8 — the migration",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, body)
		}
	}

	// An empty description must say so rather than leaving a bare heading.
	empty, _ := renderBody(&issue{Key: "BRZ-1"}, nil)
	if !strings.Contains(empty, "_(no description)_") {
		t.Errorf("an empty description should be named:\n%s", empty)
	}
	// A comment with no author is still a comment; it must not vanish.
	anon, _ := renderBody(&issue{Key: "BRZ-1"}, &commentPage{Comments: []comment{{
		Body: json.RawMessage(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"hi"}]}]}`),
	}}})
	if !strings.Contains(anon, "### someone — ") || !strings.Contains(anon, "hi") {
		t.Errorf("an authorless comment must survive:\n%s", anon)
	}
}

// TestFormatJiraTime pins the zone offset Go's RFC3339 layout refuses.
//
// Jira sends "2026-08-25T09:41:22.113+0000" — RFC 3339 except for the missing
// colon in the zone. Dropping the timestamp would take "when was this said" out
// of every comment, which is usually what decides whether a ticket is current.
func TestFormatJiraTime(t *testing.T) {
	if got := formatJiraTime("2026-08-25T09:41:22.113+0000"); got != "2026-08-25 09:41" {
		t.Errorf("Jira's own format = %q", got)
	}
	if got := formatJiraTime("2026-08-25T09:41:22Z"); got != "2026-08-25 09:41" {
		t.Errorf("RFC3339 = %q", got)
	}
	// An unparseable value is passed through rather than dropped: a raw
	// timestamp still tells the reader when.
	if got := formatJiraTime("whenever"); got != "whenever" {
		t.Errorf("unparseable = %q, want it passed through", got)
	}
}

// TestDuplicateAttachmentNamesEachGetTheirOwnImage pins the case the
// "download everything" branch exists for and used to break.
//
// Jira lets one issue carry two attachments both called image.png. A
// filename-keyed index collapsed them: both resolved to the second, the first
// became unreachable, and renderAttachments emitted the same placeholder twice
// — so one screenshot rendered double and the other was never downloaded at
// all. On a bug report that is mostly screenshots, which is precisely the case
// the surrounding comment claims to protect.
func TestDuplicateAttachmentNamesEachGetTheirOwnImage(t *testing.T) {
	iss := &issue{Key: "BRZ-1", Fields: issueFields{
		Summary: "Two shots, one name",
		Description: json.RawMessage(`{"type":"doc","content":[
			{"type":"mediaSingle","content":[{"type":"media","attrs":{"alt":"image.png"}}]},
			{"type":"mediaSingle","content":[{"type":"media","attrs":{"alt":"image.png"}}]}]}`),
		Attachment: []attachment{
			{ID: "9001", Filename: "image.png", MimeType: "image/png", Content: "https://acme.atlassian.net/a/9001"},
			{ID: "9002", Filename: "image.png", MimeType: "image/png", Content: "https://acme.atlassian.net/a/9002"},
		},
	}}

	body, images := renderBody(iss, nil)

	if len(images) != 2 {
		t.Fatalf("queued %d images, want both", len(images))
	}
	if images[0].URL == images[1].URL {
		t.Fatal("both entries point at the same attachment")
	}

	// Each placeholder appears, so collectImages downloads both files.
	for i := 0; i < 2; i++ {
		ref := "](" + ticket.PlaceholderFor(i) + ")"
		if n := strings.Count(body, ref); n != 1 {
			t.Errorf("placeholder %d appears %d time(s), want exactly 1:\n%s", i, n, body)
		}
	}

	// Two media nodes with the same name consume two different attachments,
	// rather than both landing on the last one.
	if !strings.Contains(body, "![image.png]("+ticket.PlaceholderFor(0)+")") ||
		!strings.Contains(body, "![image.png]("+ticket.PlaceholderFor(1)+")") {
		t.Errorf("the two media nodes did not resolve to two files:\n%s", body)
	}
	// Both were placed inline, so the Attachments list reports both as shown
	// rather than repeating either.
	if n := strings.Count(body, "image.png (shown above)"); n != 2 {
		t.Errorf("Attachments listed %d as shown above, want 2:\n%s", n, body)
	}
}

// TestRepeatedReferenceToOneAttachmentStillRenders: the same screenshot is
// often referenced twice in one description. Once every index for a name is
// claimed the resolver reuses the first, because replacing a perfectly good
// image with "see Attachments" would be a worse answer than showing it twice.
func TestRepeatedReferenceToOneAttachmentStillRenders(t *testing.T) {
	iss := &issue{Key: "BRZ-1", Fields: issueFields{
		Description: json.RawMessage(`{"type":"doc","content":[
			{"type":"mediaSingle","content":[{"type":"media","attrs":{"alt":"only.png"}}]},
			{"type":"mediaSingle","content":[{"type":"media","attrs":{"alt":"only.png"}}]}]}`),
		Attachment: []attachment{
			{ID: "1", Filename: "only.png", MimeType: "image/png", Content: "https://acme.atlassian.net/a/1"},
		},
	}}
	body, images := renderBody(iss, nil)
	if len(images) != 1 {
		t.Fatalf("queued %d images, want 1", len(images))
	}
	if n := strings.Count(body, "]("+ticket.PlaceholderFor(0)+")"); n != 2 {
		t.Errorf("both references should render, got %d:\n%s", n, body)
	}
	if strings.Contains(body, "see Attachments") {
		t.Errorf("a second reference to a present image should not degrade:\n%s", body)
	}
}
