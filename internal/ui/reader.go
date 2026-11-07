package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/review"
)

// reviewCommentMsg carries a saved comment out of the reader.
//
// A message rather than a direct write: the reader owns no storage, so a
// comment leaves it the same way every other dialog result does and the app
// decides where it goes.
type reviewCommentMsg struct {
	key  reviewKey
	file string
	line int
	kind review.CommentKind
	body string
	// headSHA is filled in by the app, which knows which commit the diff on
	// screen came from. The reader does not, and giving it that fact would make
	// it the second place the anchor lives.
	headSHA string
}

// reviewSubmitRequestMsg asks the app to post the review.
//
// The reader owns the comments and the sheet, and nothing else: it does not
// know the head SHA, does not hold the queue, and must never shell out to gh
// from the Update goroutine. It says what the user chose; the app knows where
// that goes, exactly as reviewCommentMsg already works.
type reviewSubmitRequestMsg struct {
	key   reviewKey
	event github.ReviewEvent
	body  string
}

// reviewSubmitResultMsg carries the outcome back.
type reviewSubmitResultMsg struct {
	key   reviewKey
	event github.ReviewEvent
	count int
	err   error
}

// reviewTourMsg carries a generated tour back to the app.
type reviewTourMsg struct {
	key     reviewKey
	headSHA string
	tour    *review.Tour
	err     error
}

// readerTab is which of the three views the reader is showing.
//
// A tab and not a mode, by the design system's own test: switching one does not
// move the keyboard — the tour and the diff both leave it wherever it was, and
// the session tab hands it over only when you ask with ⏎. So they render as a
// selection, not as a mode indicator.
type readerTab int

const (
	tabTour readerTab = iota
	tabDiff
	tabSession
)

// readerTabs is the bar, in the order the digits address them.
//
// The tour leads because it is the answer to "where do I start", which is the
// question you have when the reader opens. The diff is where the work happens
// and the session is where the agent is, so they sit either side of it in the
// order you reach for them.
var readerTabs = []struct {
	tab   readerTab
	digit string
	label string
}{
	{tabTour, "1", "tour"},
	{tabDiff, "2", "diff"},
	{tabSession, "3", "session"},
}

// tourState is how far along the one Claude call fleet makes for itself is.
type tourState int

const (
	tourAbsent tourState = iota
	tourWorking
	tourReady
	tourFailed
)

// tourItem is one row of the tour panel: a step, or one of its anchors.
//
// The anchors of the SELECTED step only are ever listed. A tour of five steps
// with four anchors each is twenty rows in a panel a quarter of the screen
// wide, which is a file tree again — the point of steps is that you see the
// route before you see the stops.
type tourItem struct {
	step   int
	anchor int // -1 for the step's own row
}

// splitMinWidth is where side-by-side starts being readable.
//
// Two code columns plus a file tree plus four borders leaves ~55 columns a
// side at 160, which is about where a wrapped line stops being the common case.
// Below it the reader falls back to unified rather than showing two columns too
// narrow to read — `|` overrides either way.
const splitMinWidth = 160

// treeMinWidth is where the file tree stops earning its columns.
const treeMinWidth = 90

type readerMode int

const (
	modeNormal readerMode = iota
	modeCompose
	modeSearch
	modeSubmit
)

func (m readerMode) String() string {
	switch m {
	case modeCompose:
		return "COMMENT"
	case modeSearch:
		return "SEARCH"
	case modeSubmit:
		return "SUBMIT"
	}
	return "NORMAL"
}

// submitEvents is the verdict cycle, and COMMENT leads it deliberately.
//
// Approve is the most consequential of the three and the one a stray keypress
// must not reach: COMMENT refuses to send without a summary, so `S` followed by
// `⏎` on an untouched sheet does nothing at all.
var submitEvents = []github.ReviewEvent{
	github.EventComment, github.EventApprove, github.EventRequestChanges,
}

// submitLabels are those events in the words a person uses.
var submitLabels = []string{"comment", "approve", "request changes"}

// dispRow is one rendered row of the diff panel.
//
// The cursor and the scroll position both index THIS list rather than the
// stream, because side-by-side pairs two stream lines onto one row and unified
// does not — an index that meant a stream line would move under the user every
// time they pressed `|`. Toggling remaps through the stream index instead, so
// the cursor stays on the same line of code.
type dispRow struct {
	left  int  // stream index, or -1
	right int  // stream index in split mode, or -1
	full  bool // spans the whole panel (headers, comments, folds)
}

func (r dispRow) primary() int {
	if r.left >= 0 {
		return r.left
	}
	return r.right
}

// ReaderDialog is the full-screen diff reader.
//
// A pager, not a browser. There is a scroll position and there are jump
// targets; nothing on screen is selected and nothing is picked. You fall down
// the whole changeset and skip what you do not care about — the way a PR
// actually gets read.
type ReaderDialog struct {
	visible bool
	width   int
	height  int

	pr     int
	title  string
	author string
	repo   string

	doc  *review.Doc
	tree []review.TreeNode
	rows []dispRow
	adds int
	dels int

	// shown is what salience kept, all is everything the PR changed, and
	// showAll says which is on screen. Both are carried so `s` can swap them
	// here: the tree footer advertises that key, and a footer naming a key
	// nothing handles is the exact failure this reader already shipped once.
	shown    []github.PRFile
	all      []github.PRFile
	showAll  bool
	worktree string

	// cursor is the row the keys act from, and top is the first row drawn.
	// Two values rather than one because a jump has to be able to place the
	// target near the top of the viewport rather than wherever it lands.
	cursor int
	top    int

	// split is side-by-side. splitPinned records that the user chose it, so a
	// terminal resize never undoes a decision they made with a keystroke.
	split       bool
	splitPinned bool

	// read records which files this user has finished, fleet's own record
	// rather than GitHub's. Keyed by path.
	read map[string]bool

	seed []review.Comment

	// treeFocus says which panel the keyboard drives, exactly as focusMode does
	// for the sidebar and preview on the main screen. treeCursor is the tree's
	// own selection, which only exists while it is focused — with the diff
	// focused the tree is a readout and follows.
	treeFocus bool

	// treeCursor indexes treeView, NOT tree: a collapsed directory hides rows,
	// and a cursor indexing the full list would sit inside a subtree nobody can
	// see. treeView is the rows actually on screen.
	treeCursor int
	treeView   []review.TreeNode

	// collapsed folds a directory away, keyed by its own path. Survives `s`
	// and a resize; reset when the reader opens on a different PR.
	collapsed map[string]bool

	// help is the `?` sheet. A pager has no menus and no visible chrome
	// explaining itself, so the key list has to be one keystroke away or the
	// only keys anyone finds are the six the footer has room for.
	help bool

	mode      readerMode
	draft     review.Comment
	editingID int // >0 while editing a saved comment rather than writing one

	query  string
	hits   []int
	hitIdx int

	// The tour: fleet's own reading of the pull request, and where you are in
	// it. The tab decides whether the left panel shows the route or the files.
	tour      *review.Tour
	tourState tourState
	tourErr   string
	tourItems []tourItem
	tourCur   int

	// session is what the app knows about the agent working on this pull
	// request, refreshed while the third tab is showing.
	session readerSession

	// tab is which of the three views is showing. Diff by default: the tour
	// takes tens of seconds to arrive, and opening onto an empty panel that
	// says "reading the pull request…" would put a wait in front of the thing
	// you already asked for.
	tab readerTab

	// The submit sheet. submitEvent indexes submitEvents; submitBody is the
	// review summary. submitErr is why the last attempt was refused — held on
	// the sheet rather than shown as a toast, because the fix (type a summary,
	// pick another verdict) happens on the sheet and a notice that vanished
	// behind the next keystroke would take the reason with it.
	submitEvent int
	submitBody  string
	submitting  bool
	submitErr   string

	// toast is the last thing that happened, shown bottom-right. One slot: a
	// second event replaces the first, because a stack of notices in a footer
	// is a log, and a log nobody asked for.
	toast string
}

// Show opens the reader on a changeset. worktree is the review checkout that
// context expansion reads from; empty disables expansion.
func (d *ReaderDialog) Show(pr int, title, author, repo, worktree string,
	shown, all []github.PRFile, read map[string]bool, seed []review.Comment) {

	d.visible = true
	d.pr, d.title, d.author, d.repo = pr, title, author, repo
	d.shown, d.all, d.worktree = shown, all, worktree
	d.showAll = false
	d.collapsed = map[string]bool{}
	d.treeFocus, d.treeCursor = false, 0
	d.seed = seed
	d.load()

	d.splitPinned = false
	d.split = d.width >= splitMinWidth
	d.cursor, d.top = 0, 0
	d.mode = modeNormal
	d.draft = review.Comment{}
	d.editingID = 0
	d.query, d.hits, d.hitIdx = "", nil, 0
	d.tour, d.tourState, d.tourErr = nil, tourAbsent, ""
	d.tab, d.tourItems, d.tourCur = tabDiff, nil, 0
	d.session = readerSession{}
	d.resetSubmit()
	d.toast = ""
	d.read = read
	if d.read == nil {
		d.read = map[string]bool{}
	}
	d.rebuildRows()
}

// files is what is on screen right now.
func (d *ReaderDialog) files() []github.PRFile {
	if d.showAll {
		return d.all
	}
	return d.shown
}

// hiddenCount is how many files salience folded away.
func (d *ReaderDialog) hiddenCount() int {
	if d.showAll {
		return 0
	}
	return len(d.all) - len(d.shown)
}

// load rebuilds the document and tree from whichever file set is showing.
//
// Comments survive the swap: they are anchored to file and line, and a file
// that folds back out of view still has the comment waiting when it returns.
func (d *ReaderDialog) load() {
	files := d.files()
	d.adds, d.dels = 0, 0
	tf := make([]review.TreeFile, 0, len(files))
	for _, f := range files {
		tf = append(tf, review.TreeFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Status: f.Status})
		d.adds += f.Additions
		d.dels += f.Deletions
	}
	d.tree = review.BuildTree(tf)
	d.rebuildTreeView()

	carried := d.seed
	if d.doc != nil {
		carried = d.doc.Comments()
	}
	d.doc = review.NewDoc(files, d.worktree, d.commentWrapWidth())
	if len(carried) > 0 {
		d.doc.SeedComments(carried)
	}
}

func (d *ReaderDialog) Hide() {
	d.visible = false
	d.mode = modeNormal
	if d.doc != nil {
		d.doc.SetDraft(nil)
	}
}

func (d *ReaderDialog) Visible() bool { return d.visible }

// tourMode reports whether the left panel is showing the route.
func (d *ReaderDialog) tourMode() bool { return d.tab == tabTour }

// setTab switches views, refusing a tab that has nothing behind it yet.
//
// A refusal states why, because a digit that silently does nothing is
// indistinguishable from a key that is not bound — and the tour genuinely is
// not there for the first half-minute of every review.
func (d *ReaderDialog) setTab(t readerTab) {
	if t == d.tab {
		return
	}
	if t == tabTour {
		switch {
		case d.tourState == tourWorking:
			d.toast = "still reading the pull request…"
			return
		case d.tourState == tourFailed:
			d.toast = ansi.Truncate("no tour — "+d.tourErr, max(d.width-24, 20), "…")
			return
		case d.tour == nil || len(d.tour.Steps) == 0:
			d.toast = "no tour for this pull request"
			return
		}
	}
	d.tab = t
	switch t {
	case tabTour:
		// The route takes the keyboard: you switched to it to be led.
		d.treeFocus = true
		d.rebuildTourItems()
		d.showTourItem()
	case tabDiff:
		d.treeFocus = false
	}
}

// SessionTabActive reports whether the agent's pane is the view on screen. The
// app polls this to decide whether to hold a tmux stream open at all — a
// terminal nobody is looking at is a subprocess and a PTY for nothing.
func (d *ReaderDialog) SessionTabActive() bool { return d.visible && d.tab == tabSession }

// SessionPaneSize is the panel the pane is rendered into, so the emulator and
// the real tmux pane can be resized to the same shape. Wrapping at a different
// width than the agent drew at is what makes a pane look corrupted.
func (d *ReaderDialog) SessionPaneSize() (int, int) {
	return max(d.width-2, 1), max(d.body(), 1)
}

// key identifies the pull request being read. The reader already holds both
// halves; naming them together keeps every message it emits addressable.
func (d *ReaderDialog) key() reviewKey { return reviewKey{repo: d.repo, pr: d.pr} }

// Comments is what the reader has to hand back when it closes.
func (d *ReaderDialog) Comments() []review.Comment {
	if d.doc == nil {
		return nil
	}
	return d.doc.Comments()
}

// SetSize records the viewport and re-flows anything measured in columns.
func (d *ReaderDialog) SetSize(w, h int) {
	if w == d.width && h == d.height {
		return
	}
	d.width, d.height = w, h
	if !d.splitPinned {
		d.split = w >= splitMinWidth
	}
	if d.doc != nil {
		d.doc.SetWrapWidth(d.commentWrapWidth())
		d.rebuildRows()
	}
}

// body is how many rows of diff fit inside the panel border.
func (d *ReaderDialog) body() int {
	// One header row, one tab bar, one footer row, and the panel's own two
	// border rows.
	n := d.height - 5
	if n < 1 {
		return 1
	}
	return n
}

// diffWidth is the diff panel's outer width.
func (d *ReaderDialog) diffWidth() int {
	w := d.width - d.treeWidth()
	if w < 10 {
		w = 10
	}
	return w
}

// treeWidth scales with the terminal and is capped: past ~34 columns the tree
// is whitespace, and below 22 the filenames truncate to nothing useful — so a
// narrow terminal drops the tree entirely rather than showing a broken one.
func (d *ReaderDialog) treeWidth() int {
	if d.width < treeMinWidth {
		return 0
	}
	w := d.width / 4
	// 34 is sized for FILENAMES. A step title is a sentence fragment, and at 34
	// every one of them wrapped — which reads as breakage rather than as a
	// list, and left no room to put an anchor's note beside its path. The cap
	// only bites on a wide terminal: at 160 columns a quarter is already 40.
	limit := 34
	if d.tourMode() {
		limit = tourPanelWidth
	}
	if w > limit {
		w = limit
	}
	if w < 22 {
		return 0
	}
	return w
}

// commentWrapWidth is how wide a comment box's text may be.
//
// Measured against the narrower of the two layouts, so switching to
// side-by-side never reflows every comment on screen — a box that changes shape
// when you press `|` reads as the comment itself having changed.
func (d *ReaderDialog) commentWrapWidth() int {
	w := d.diffWidth() - commentIndent - 8
	if half := (d.diffWidth()/2 - commentIndent - 8); d.split && half < w {
		w = half
	}
	if w < 20 {
		w = 20
	}
	if w > 96 {
		w = 96
	}
	return w
}

// rebuildRows re-derives the display list from the stream, keeping the cursor
// on the same line of code.
func (d *ReaderDialog) rebuildRows() {
	anchor := d.streamAt(d.cursor)
	d.rows = buildRows(d.doc.Stream, d.split)
	d.cursor = d.rowOfStream(anchor)
	d.clampScroll()
}

// buildRows flattens the stream into rendered rows.
//
// In split mode a run of deletions is paired with the run of additions that
// follows it, index for index, so the two sides stay level and your eye tracks
// straight across. The shorter run is padded with empty cells rather than
// letting the sides drift apart, which is the whole reason side-by-side is
// worth having.
func buildRows(s review.Stream, split bool) []dispRow {
	rows := make([]dispRow, 0, len(s.Lines))
	if !split {
		for i := range s.Lines {
			rows = append(rows, dispRow{left: i, right: -1, full: true})
		}
		return rows
	}

	i := 0
	for i < len(s.Lines) {
		switch s.Lines[i].Kind {
		case review.LineDel:
			delStart := i
			for i < len(s.Lines) && s.Lines[i].Kind == review.LineDel {
				i++
			}
			addStart := i
			for i < len(s.Lines) && s.Lines[i].Kind == review.LineAdd {
				i++
			}
			nDel, nAdd := addStart-delStart, i-addStart
			for k := range max(nDel, nAdd) {
				r := dispRow{left: -1, right: -1}
				if k < nDel {
					r.left = delStart + k
				}
				if k < nAdd {
					r.right = addStart + k
				}
				rows = append(rows, r)
			}

		case review.LineAdd:
			// An addition with no deletion before it: right side only.
			rows = append(rows, dispRow{left: -1, right: i})
			i++

		case review.LineContext:
			rows = append(rows, dispRow{left: i, right: i})
			i++

		default:
			// Headers, folds, notes and comment boxes span the whole panel.
			rows = append(rows, dispRow{left: i, right: -1, full: true})
			i++
		}
	}
	return rows
}

// streamAt returns the stream index the row under i stands on.
func (d *ReaderDialog) streamAt(i int) int {
	if i < 0 || i >= len(d.rows) {
		return 0
	}
	return d.rows[i].primary()
}

// rowOfStream maps a stream index back to the row that shows it.
func (d *ReaderDialog) rowOfStream(streamIdx int) int {
	for i, r := range d.rows {
		if r.left == streamIdx || r.right == streamIdx {
			return i
		}
	}
	// The line is not shown in this layout. Land on the nearest row before it
	// rather than at the top: losing your place is the thing the reader is for.
	best := 0
	for i, r := range d.rows {
		if r.primary() <= streamIdx {
			best = i
		}
	}
	return best
}

// lineAt returns the stream line under the cursor.
func (d *ReaderDialog) lineAt(i int) (review.Line, bool) {
	s := d.streamAt(i)
	if d.doc == nil || s < 0 || s >= len(d.doc.Stream.Lines) {
		return review.Line{}, false
	}
	return d.doc.Stream.Lines[s], true
}

// Update handles a key.
func (d *ReaderDialog) Update(msg tea.KeyPressMsg) (*ReaderDialog, tea.Cmd) {
	switch d.mode {
	case modeCompose:
		return d.updateCompose(msg)
	case modeSearch:
		return d.updateSearch(msg)
	case modeSubmit:
		return d.updateSubmit(msg)
	}

	if d.help {
		d.help = false
		return d, nil
	}

	d.toast = ""
	switch msg.String() {
	case "tab", "shift+tab":
		d.toggleTreeFocus()

	case "?":
		d.help = true

	case "ctrl+q":
		// Ctrl+Q because that is what leaving a full-screen surface means
		// everywhere else in fleet — attach detaches on it, and a reader that
		// wanted a different key would be the odd one out. It quits from
		// either panel, so there is always one key that gets you out.
		d.Hide()
		return d, nil

	case "esc":
		// Esc steps back one level before it leaves, the way it does on the
		// main screen: it drops preview focus first and only then means "no".
		if d.treeFocus {
			d.treeFocus = false
			return d, nil
		}
		d.Hide()
		return d, nil

	case "j", "down":
		if d.treeFocus {
			d.moveLeftPanel(1)
			break
		}
		d.moveTo(d.cursor + 1)
	case "k", "up":
		if d.treeFocus {
			d.moveLeftPanel(-1)
			break
		}
		d.moveTo(d.cursor - 1)

	case "space":
		// The "I am done with this file" verb, and the one you press most.
		// Bound as "space" and NOT " ": KeyPressMsg.String() reports the key's
		// NAME, so the old `case " "` matched nothing and space did nothing at
		// all — the same trap that ate every space typed into a comment.
		d.markReadAndAdvance()

	case "pgdown":
		d.moveTo(d.cursor + d.body())
	case "pgup":
		d.moveTo(d.cursor - d.body())
	case "ctrl+d":
		d.moveTo(d.cursor + d.body()/2)
	case "ctrl+u":
		d.moveTo(d.cursor - d.body()/2)

	case "g", "home":
		d.moveTo(0)
	case "G", "end":
		d.moveTo(len(d.rows) - 1)

	case "left", "h":
		// ←/→ fold and unfold, the way every file tree does. Free here because
		// the diff panel puts its file jumps on SHIFT+arrows. In the tour they
		// mean the same shape of thing: out to the step, in to its stops.
		if d.treeFocus && d.tourMode() {
			d.stepOutOfAnchor()
			break
		}
		if d.treeFocus {
			d.collapseAtCursor(true)
		}
	case "right", "l":
		if d.treeFocus && d.tourMode() {
			d.stepIntoAnchor()
			break
		}
		if d.treeFocus {
			d.collapseAtCursor(false)
		}

	// Shift+arrow walks the structure, matching the sidebar, where Shift+↑/↓
	// already means "jump to the next boundary". The bracket and punctuation
	// forms stay as aliases for anyone who came from a vim-shaped pager.
	case "shift+down", "]":
		d.jumpStream(review.NextIndex(d.doc.Stream.HunkStarts, d.streamAt(d.cursor)))
	case "shift+up", "[":
		d.jumpStream(review.PrevIndex(d.doc.Stream.HunkStarts, d.streamAt(d.cursor)))
	case "shift+right", ".":
		d.jumpStream(review.NextIndex(d.doc.Stream.FileStarts, d.streamAt(d.cursor)))
	case "shift+left", ",":
		d.jumpStream(review.PrevIndex(d.doc.Stream.FileStarts, d.streamAt(d.cursor)))

	case "|", "\\":
		// Pinned, so a later resize cannot undo a choice made by keystroke.
		d.split = !d.split
		d.splitPinned = true
		d.doc.SetWrapWidth(d.commentWrapWidth())
		d.rebuildRows()
		d.toast = "unified"
		if d.split {
			d.toast = "side by side"
		}

	case "enter":
		if d.tab == tabSession {
			// Attach when there is one, start one when there is not. Both are
			// the same gesture from here: ⏎ means "put me with the agent".
			key, start := d.key(), !d.session.Present
			if start && d.session.Starting {
				break
			}
			if start {
				d.session.Starting = true
			}
			return d, func() tea.Msg { return reviewSessionMsg{key: key, start: start} }
		}
		if d.treeFocus && d.tourMode() {
			// On a step, ⏎ goes to its first stop; on a stop it hands the
			// keyboard to the diff. Same promise as the file tree: you picked
			// it, now read it.
			if _, an, ok := d.tourSelection(); ok && an == nil {
				d.stepIntoAnchor()
				break
			}
			d.treeFocus = false
			break
		}
		if d.treeFocus {
			// On a directory, Enter folds — the only verb a folder has. On a
			// file the diff already moved as the selection did, so Enter's job
			// is handing the keyboard back: you picked it, now read it.
			if d.toggleCollapse() {
				break
			}
			d.treeFocus = false
			break
		}
		d.expandAt(false)
	case "+":
		d.expandAt(true)

	case "s":
		// Swap between what salience kept and everything the PR changed. The
		// tree footer names this key, so it has to be the key that works.
		if d.hiddenCount() == 0 && !d.showAll {
			d.toast = "nothing folded"
			break
		}
		anchor := d.doc.Stream.FileAt(d.streamAt(d.cursor))
		d.showAll = !d.showAll
		d.load()
		d.rows = buildRows(d.doc.Stream, d.split)
		d.cursor, d.top = 0, 0
		for i, f := range d.doc.Stream.Files {
			if f == anchor {
				d.jumpStream(d.doc.Stream.FileStarts[i])
				break
			}
		}
		d.toast = "showing " + plural(len(d.files()), "file")

	case "m":
		// Toggles and stays put — the correction, where space is the flow.
		// Two verbs because un-marking is a thing you do deliberately, and a
		// single key that both advanced and toggled would un-read a file every
		// time you passed back over it.
		if f := d.doc.Stream.FileAt(d.streamAt(d.cursor)); f != "" {
			d.read[f] = !d.read[f]
			d.toast = "marked unread"
			if d.read[f] {
				d.toast = "marked read"
			}
		}

	case "1":
		d.setTab(tabTour)
	case "2":
		d.setTab(tabDiff)
	case "3":
		d.setTab(tabSession)

	case "t":
		// Kept as the toggle it was before the bar existed: it is muscle memory
		// now, and it is still the fastest way between the two views you swap
		// between most.
		d.toggleTour()

	case "c":
		d.startComment()
	case "e":
		d.editComment()
	case "d":
		d.deleteComment()
	case "S":
		d.openSubmit()

	case "/":
		d.mode = modeSearch
		d.query = ""
	case "n":
		d.stepHit(1)
	case "N":
		d.stepHit(-1)
	}
	return d, nil
}

// updateSearch handles keys while the search line has the caret.
func (d *ReaderDialog) updateSearch(msg tea.KeyPressMsg) (*ReaderDialog, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Abandons the search and its highlights. A query that survived being
		// cancelled would keep painting the screen with nothing saying why.
		d.mode, d.query, d.hits = modeNormal, "", nil
	case "enter":
		d.mode = modeNormal
		if len(d.hits) == 0 && d.query != "" {
			d.toast = "no match for " + d.query
		}
	case "backspace":
		if n := len([]rune(d.query)); n > 0 {
			d.query = string([]rune(d.query)[:n-1])
			d.runSearch()
		}
	default:
		if t := typedText(msg); t != "" {
			d.query += t
			d.runSearch()
		}
	}
	return d, nil
}

// typedText is the character a key produced, or "" if it produced none.
//
// Reads Text and NEVER String(): String() returns the key's NAME, so the space
// bar arrives as the four-letter word "space" and a length-1 test on it drops
// every space the user types — a comment box you cannot put a space in. Text
// carries what the layout actually produced, which is also what makes a Hebrew
// or Greek comment come out in Hebrew or Greek.
//
// Chords are excluded outright: Ctrl+J is a command here, and some terminals
// set Text on a modified key anyway.
func typedText(msg tea.KeyPressMsg) string {
	if msg.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper|tea.ModHyper) != 0 {
		return ""
	}
	if msg.Text == "" || strings.ContainsAny(msg.Text, "\r\n\t\x1b") {
		return ""
	}
	return msg.Text
}

// runSearch re-matches as you type and moves to the first hit at or after the
// cursor, so searching from where you are reads forward rather than jumping to
// the top of the changeset.
func (d *ReaderDialog) runSearch() {
	d.hits = d.doc.Stream.Search(d.query)
	if len(d.hits) == 0 {
		return
	}
	from := d.streamAt(d.cursor)
	d.hitIdx = 0
	for i, h := range d.hits {
		if h >= from {
			d.hitIdx = i
			break
		}
	}
	d.jumpStream(d.hits[d.hitIdx])
}

// stepHit walks the match list, wrapping — unlike hunk and file jumps, which
// hold at the ends. A search is a set you asked for by name and expect to cycle;
// a hunk jump is progress through a document you expect to stop at the end of.
func (d *ReaderDialog) stepHit(delta int) {
	if len(d.hits) == 0 {
		if d.query != "" {
			d.toast = "no match for " + d.query
		}
		return
	}
	d.hitIdx = (d.hitIdx + delta + len(d.hits)) % len(d.hits)
	d.jumpStream(d.hits[d.hitIdx])
}

// markReadAndAdvance marks the current file read and drops you on the next one
// you have not read.
//
// SETS read rather than toggling: this is "done with this file", and it has to
// be idempotent — pressing it on a file you already finished should carry you
// forward, not silently un-read it.
//
// It skips FORWARD over files already read, because spacing through a PR is a
// queue-draining motion: landing on something you finished an hour ago is a
// keystroke that did nothing. It never searches backwards — a jump that moves
// the wrong way is worse than one that stops.
func (d *ReaderDialog) markReadAndAdvance() {
	cur := d.doc.Stream.FileAt(d.streamAt(d.cursor))
	if cur == "" {
		return
	}
	d.read[cur] = true

	files := d.doc.Stream.Files
	at := -1
	for i, f := range files {
		if f == cur {
			at = i
			break
		}
	}

	target := -1
	for i := at + 1; i < len(files); i++ {
		if !d.read[files[i]] {
			target = i
			break
		}
	}
	// Nothing unread ahead: fall through to the next file so the key still
	// moves, rather than appearing to have been swallowed.
	if target < 0 && at+1 < len(files) {
		target = at + 1
	}

	unread := 0
	for _, f := range files {
		if !d.read[f] {
			unread++
		}
	}

	switch {
	case target >= 0:
		d.jumpStream(d.doc.Stream.FileStarts[target])
		d.toast = plural(unread, "file") + " left"
		if unread == 0 {
			d.toast = "all " + plural(len(files), "file") + " read"
		}
	case unread == 0:
		d.toast = "all " + plural(len(files), "file") + " read"
	default:
		// At the last file with earlier ones still unread. Say which, rather
		// than holding still with no explanation.
		d.toast = plural(unread, "file") + " still unread — ⇧← to go back"
	}
}

// rebuildTreeView recomputes which tree rows are on screen.
//
// A collapsed directory hides everything beneath it, which is every following
// row at a greater depth — the flat list is already in depth-first order, so
// this is one pass and no recursion.
func (d *ReaderDialog) rebuildTreeView() {
	d.treeView = d.treeView[:0]
	skipBelow := -1
	for _, n := range d.tree {
		if skipBelow >= 0 {
			if n.Depth > skipBelow {
				continue
			}
			skipBelow = -1
		}
		d.treeView = append(d.treeView, n)
		if n.IsDir && d.collapsed[n.Path] {
			skipBelow = n.Depth
		}
	}
	if d.treeCursor >= len(d.treeView) {
		d.treeCursor = max(len(d.treeView)-1, 0)
	}
}

// toggleCollapse folds the directory under the tree cursor.
//
// The selection is restored by IDENTITY rather than by index: folding a
// directory removes rows above nothing but shifts everything below it, so an
// index kept across the rebuild lands on a different file. Collapsing the
// directory you are standing in must leave you standing on it.
func (d *ReaderDialog) toggleCollapse() bool {
	if d.treeCursor < 0 || d.treeCursor >= len(d.treeView) {
		return false
	}
	n := d.treeView[d.treeCursor]
	if !n.IsDir {
		return false
	}
	d.collapsed[n.Path] = !d.collapsed[n.Path]
	d.rebuildTreeView()
	for i, v := range d.treeView {
		if v.IsDir && v.Path == n.Path {
			d.treeCursor = i
			break
		}
	}
	d.showTreeRow(n)
	if d.collapsed[n.Path] {
		d.toast = "folded " + n.Label
	} else {
		d.toast = ""
	}
	return true
}

// collapseAtCursor folds or unfolds directionally.
//
// ← on a FILE jumps to its parent directory rather than doing nothing: that is
// what ← means in a tree, and it is the fastest way to fold the folder you are
// standing in — press it twice.
func (d *ReaderDialog) collapseAtCursor(fold bool) {
	if d.treeCursor < 0 || d.treeCursor >= len(d.treeView) {
		return
	}
	n := d.treeView[d.treeCursor]

	if !n.IsDir {
		if !fold {
			return
		}
		for i := d.treeCursor - 1; i >= 0; i-- {
			if d.treeView[i].IsDir && d.treeView[i].Depth < n.Depth {
				d.treeCursor = i
				return
			}
		}
		return
	}
	if d.collapsed[n.Path] == fold {
		// Already in that state. ← on a folded directory climbs to its parent,
		// so repeated presses walk out of the tree rather than stalling.
		if fold {
			for i := d.treeCursor - 1; i >= 0; i-- {
				if d.treeView[i].IsDir && d.treeView[i].Depth < n.Depth {
					d.treeCursor = i
					return
				}
			}
		}
		return
	}
	d.toggleCollapse()
}

// toggleTreeFocus moves the keyboard between the two panels.
//
// The same shape as the main screen's sidebar-and-preview: one panel holds the
// keyboard, it is the one wearing the accent border, and the other keeps
// showing what it shows. Refused rather than silently ignored when the terminal
// is too narrow to draw a tree at all.
func (d *ReaderDialog) toggleTreeFocus() {
	if d.treeWidth() == 0 {
		d.toast = "no room for the file tree at this width"
		return
	}
	d.treeFocus = !d.treeFocus
	if d.treeFocus {
		d.syncTreeCursor()
	}
}

// syncTreeCursor puts the tree's selection on the file the diff is showing, so
// taking focus never moves you somewhere you were not already.
//
// Expands whatever is folded over that file first: taking focus and landing on
// a directory because your own file is hidden inside it is the cursor moving on
// its own, which is the ambiguity the design system forbids.
func (d *ReaderDialog) syncTreeCursor() {
	cur := d.doc.Stream.FileAt(d.streamAt(d.cursor))
	d.revealFile(cur)
	for i, n := range d.treeView {
		if !n.IsDir && n.Path == cur {
			d.treeCursor = i
			return
		}
	}
	d.treeCursor = 0
}

// revealFile unfolds every directory hiding path.
func (d *ReaderDialog) revealFile(path string) {
	if path == "" {
		return
	}
	changed := false
	for dir := range d.collapsed {
		if d.collapsed[dir] && strings.HasPrefix(path, dir+"/") {
			d.collapsed[dir] = false
			changed = true
		}
	}
	if changed {
		d.rebuildTreeView()
	}
}

// moveTree walks the tree's selection over every visible row.
//
// Directories are included because they are actionable — ⏎ and ←→ fold them —
// so a selection can land on one without becoming a dead click. Landing on a
// directory deliberately does NOT move the diff: a folder is not a file, and
// scrolling somewhere arbitrary because you passed over one would make the two
// panels disagree.
func (d *ReaderDialog) moveTree(delta int) {
	i := d.treeCursor + delta
	if i < 0 || i >= len(d.treeView) {
		return // hold at the ends, like every other jump in the reader
	}
	d.treeCursor = i
	d.showTreeRow(d.treeView[i])
}

// showTreeRow scrolls the diff to whatever the tree row names.
//
// A directory carries you to the FIRST file under it rather than nowhere: the
// tree is a picture of where you are, and a selection that left the diff
// somewhere else would make the two panels disagree about the answer.
func (d *ReaderDialog) showTreeRow(n review.TreeNode) {
	want := n.Path
	if n.IsDir {
		want = ""
		for _, f := range d.doc.Stream.Files {
			if strings.HasPrefix(f, n.Path+"/") {
				want = f
				break
			}
		}
	}
	if want == "" {
		return
	}
	for fi, f := range d.doc.Stream.Files {
		if f == want {
			d.jumpStream(d.doc.Stream.FileStarts[fi])
			return
		}
	}
}

// treeSelected is the FILE the tree's selection names, or "" on a directory.
func (d *ReaderDialog) treeSelected() string {
	if d.treeCursor < 0 || d.treeCursor >= len(d.treeView) {
		return ""
	}
	if n := d.treeView[d.treeCursor]; !n.IsDir {
		return n.Path
	}
	return ""
}

// expandAt opens the fold under the cursor.
func (d *ReaderDialog) expandAt(all bool) {
	l, ok := d.lineAt(d.cursor)
	if !ok || l.Kind != review.LineExpand || l.Gap == nil {
		return
	}
	before := l.Gap.Hidden()
	anchor := l.Gap
	if !d.doc.Expand(anchor, all) {
		return
	}
	d.rows = buildRows(d.doc.Stream, d.split)
	// Land on the fold's new position, or on the code that replaced it.
	d.cursor = d.rowOfStream(d.gapRow(anchor))
	d.clampScroll()
	if anchor.Hidden() == 0 {
		d.toast = "expanded " + plural(before, "line")
	} else {
		d.toast = plural(anchor.Hidden(), "line") + " still folded"
	}
}

// gapRow finds where a gap's marker now sits, or where it used to.
func (d *ReaderDialog) gapRow(g *review.Gap) int {
	for i, l := range d.doc.Stream.Lines {
		if l.Kind == review.LineExpand && l.Gap == g {
			return i
		}
	}
	for i, l := range d.doc.Stream.Lines {
		if l.File == g.File && l.New >= g.Lo {
			return i
		}
	}
	return 0
}

// startComment opens the composer against the row under the cursor.
//
// Only a real code row inside the patch can carry one: GitHub anchors a review
// comment to a position in the diff it was sent, so a header, a fold, or a
// context line we expanded out of the worktree has nothing to attach to. The
// refusal is stated rather than silent — a comment that lands somewhere else is
// worse than one that was never written.
func (d *ReaderDialog) startComment() {
	l, ok := d.lineAt(d.cursor)
	if !ok {
		return
	}
	if l.Expanded {
		d.toast = "expanded context can't carry a comment — GitHub anchors inside the diff"
		return
	}
	if l.Kind != review.LineAdd && l.Kind != review.LineContext {
		d.toast = "comments attach to a line of the new side"
		return
	}
	d.mode = modeCompose
	d.editingID = 0
	d.draft = review.Comment{File: l.File, Line: l.New, Kind: review.CommentIssue}
	d.doc.SetDraft(&d.draft)
	d.syncDraftCursor()
}

// editComment reopens the comment under the cursor.
func (d *ReaderDialog) editComment() {
	l, ok := d.lineAt(d.cursor)
	if !ok || !l.Kind.IsComment() || l.Comment == nil {
		return
	}
	if l.Comment.Sent {
		d.toast = "already submitted — can't edit from here"
		return
	}
	d.mode = modeCompose
	d.editingID = l.Comment.ID
	d.draft = *l.Comment
	d.doc.DeleteComment(d.editingID)
	d.doc.SetDraft(&d.draft)
	d.syncDraftCursor()
}

// deleteComment drops the comment under the cursor.
func (d *ReaderDialog) deleteComment() {
	l, ok := d.lineAt(d.cursor)
	if !ok || !l.Kind.IsComment() || l.Comment == nil {
		return
	}
	if l.Comment.Sent {
		d.toast = "already submitted — can't delete from here"
		return
	}
	d.doc.DeleteComment(l.Comment.ID)
	d.rebuildRows()
	d.toast = "comment deleted"
}

// SetTour installs a generated tour, or records why there is none.
func (d *ReaderDialog) SetTour(t *review.Tour, err error) {
	if err != nil {
		// A tour already on screen survives a failed regeneration: a route
		// built against the previous commit still leads somewhere, and blanking
		// it would cost more than the staleness does.
		if d.tour == nil {
			d.tourState, d.tourErr = tourFailed, err.Error()
		}
		return
	}
	d.tour, d.tourState, d.tourErr = t, tourReady, ""
	if d.doc != nil {
		// Bound here rather than at generation, because anchors only mean
		// anything against the stream the reader is actually showing — which
		// changes with `s`, and would have to be re-resolved anyway.
		d.tour.Bind(&d.doc.Stream)
	}
	d.tourCur = 0
	d.rebuildTourItems()
}

// TourWorking marks the call as in flight, so the panel can say so.
func (d *ReaderDialog) TourWorking() {
	if d.tourState == tourAbsent {
		d.tourState = tourWorking
	}
}

// toggleTour swaps the left panel between the file tree and the route.
//
// Turning it ON takes the keyboard with it: you pressed `t` because you want to
// be led, so the panel that leads gets the arrows and the diff follows — the
// same relationship the file tree already has, which is why the tour costs no
// new movement keys at all.
func (d *ReaderDialog) toggleTour() {
	if d.tourMode() {
		d.setTab(tabDiff)
		d.toast = "files"
		return
	}
	switch d.tourState {
	case tourWorking:
		d.toast = "still reading the pull request…"
		return
	case tourFailed:
		// Truncated at the door: the footer is one line, and an error long
		// enough to wrap would push the keys off the screen — including the
		// one that gets you out.
		d.toast = ansi.Truncate("no tour — "+d.tourErr, max(d.width-24, 20), "…")
		return
	}
	if d.tour == nil || len(d.tour.Steps) == 0 {
		d.toast = "no tour for this pull request"
		return
	}
	d.tab, d.treeFocus = tabTour, true
	d.rebuildTourItems()
	d.showTourItem()
}

// moveLeftPanel walks whichever list the left panel is showing.
func (d *ReaderDialog) moveLeftPanel(delta int) {
	if d.tourMode() {
		d.moveTour(delta)
		return
	}
	d.moveTree(delta)
}

// stepIntoAnchor moves from a step onto its first stop.
func (d *ReaderDialog) stepIntoAnchor() {
	if _, an, ok := d.tourSelection(); !ok || an != nil {
		return
	}
	if d.tourCur+1 < len(d.tourItems) && d.tourItems[d.tourCur+1].anchor == 0 {
		d.tourCur++
		d.showTourItem()
	}
}

// stepOutOfAnchor moves from a stop back to the step it belongs to.
func (d *ReaderDialog) stepOutOfAnchor() {
	if _, an, ok := d.tourSelection(); !ok || an == nil {
		return
	}
	for i := d.tourCur; i >= 0; i-- {
		if d.tourItems[i].anchor == -1 {
			d.tourCur = i
			d.showTourItem()
			return
		}
	}
}

// rebuildTourItems flattens the route, expanding the selected step's anchors.
func (d *ReaderDialog) rebuildTourItems() {
	if d.tour == nil {
		d.tourItems = nil
		return
	}
	// Which step is selected has to survive the rebuild, or opening a step's
	// anchors would move the selection off the step that opened them.
	sel := 0
	if d.tourCur < len(d.tourItems) {
		sel = d.tourItems[d.tourCur].step
	}
	var items []tourItem
	cur := 0
	for i, st := range d.tour.Steps {
		if i == sel {
			cur = len(items)
		}
		items = append(items, tourItem{step: i, anchor: -1})
		if i != sel {
			continue
		}
		for a := range st.Anchors {
			items = append(items, tourItem{step: i, anchor: a})
		}
	}
	// Hold the selection on the same thing it was on: the step row when a step
	// was selected, the same anchor when one was.
	if d.tourCur < len(d.tourItems) && d.tourItems[d.tourCur].anchor >= 0 {
		cur += 1 + d.tourItems[d.tourCur].anchor
	}
	d.tourItems = items
	d.tourCur = min(max(cur, 0), max(len(items)-1, 0))
}

// moveTour walks the route, re-expanding as the selected step changes.
func (d *ReaderDialog) moveTour(delta int) {
	if len(d.tourItems) == 0 {
		return
	}
	next := min(max(d.tourCur+delta, 0), len(d.tourItems)-1)
	stepChanged := d.tourItems[next].step != d.tourItems[d.tourCur].step
	d.tourCur = next
	if stepChanged {
		// Moving onto a new step opens it and closes the last, so the panel
		// always shows exactly one step's stops. Rebuilding re-finds the row.
		step := d.tourItems[next].step
		d.tourCur = 0
		d.tourItems = nil
		d.tourItems = []tourItem{{step: step, anchor: -1}}
		d.rebuildTourItems()
		for i, it := range d.tourItems {
			if it.step == step && it.anchor == -1 {
				d.tourCur = i
				break
			}
		}
		// Entering a step from below lands on its last anchor, so ↑ keeps
		// walking backwards through the route rather than skipping its stops.
		if delta < 0 {
			for i := len(d.tourItems) - 1; i >= 0; i-- {
				if d.tourItems[i].step == step {
					d.tourCur = i
					break
				}
			}
		}
	}
	d.showTourItem()
}

// showTourItem scrolls the diff to whatever the route is pointing at.
//
// A step with no anchors of its own borrows its first one, so selecting a step
// still moves the diff — and a step with none at all (an overview) leaves the
// diff alone, because it takes over the panel itself.
func (d *ReaderDialog) showTourItem() {
	st, an, ok := d.tourSelection()
	if !ok {
		return
	}
	if an != nil {
		d.jumpStream(an.Row)
		return
	}
	if len(st.Anchors) > 0 {
		d.jumpStream(st.Anchors[0].Row)
	}
}

// tourSelection returns the selected step, and the anchor within it if the
// selection is on one.
func (d *ReaderDialog) tourSelection() (*review.Step, *review.Anchor, bool) {
	if d.tour == nil || d.tourCur >= len(d.tourItems) {
		return nil, nil, false
	}
	it := d.tourItems[d.tourCur]
	if it.step >= len(d.tour.Steps) {
		return nil, nil, false
	}
	st := &d.tour.Steps[it.step]
	if it.anchor >= 0 && it.anchor < len(st.Anchors) {
		return st, &st.Anchors[it.anchor], true
	}
	return st, nil, true
}

// tourStepIndex is which step the route is on, for the "2/5" in the band.
func (d *ReaderDialog) tourStepIndex() int {
	if d.tourCur < len(d.tourItems) {
		return d.tourItems[d.tourCur].step
	}
	return 0
}

// openSubmit raises the submit sheet.
//
// Refused without an owner/repo, which is the state a PR fleet never saw in the
// review queue is in: there is nowhere to post to, and finding that out after
// typing a summary is worse than being told now.
func (d *ReaderDialog) openSubmit() {
	if d.repo == "" {
		d.toast = "no GitHub repo for this review — nothing to submit to"
		return
	}
	d.mode = modeSubmit
	d.submitErr = ""
}

// resetSubmit clears the sheet.
func (d *ReaderDialog) resetSubmit() {
	d.submitEvent, d.submitBody = 0, ""
	d.submitting, d.submitErr = false, ""
}

// submitEvent is the verdict currently chosen.
func (d *ReaderDialog) chosenEvent() github.ReviewEvent {
	return submitEvents[d.submitEvent%len(submitEvents)]
}

// submitBlocker is why ⏎ will not send, or "".
//
// One function consulted by both the key handler and the sheet's own footer, so
// the key can never act on a state the footer calls unready — the same rule the
// bug-report dialog's submit follows.
func (d *ReaderDialog) submitBlocker() string {
	if d.submitting {
		return "posting…"
	}
	if d.chosenEvent().NeedsBody() && strings.TrimSpace(d.submitBody) == "" {
		// GitHub refuses these two without one. Saying so here means the
		// refusal arrives while the caret is still in the box, rather than as a
		// 422 after the keystroke that was meant to end the review.
		return "a " + submitLabels[d.submitEvent] + " review needs a summary"
	}
	return ""
}

// pendingComments is what would be posted.
func (d *ReaderDialog) pendingComments() []review.Comment {
	if d.doc == nil {
		return nil
	}
	var out []review.Comment
	for _, c := range d.doc.Comments() {
		if !c.Sent {
			out = append(out, c)
		}
	}
	return out
}

// updateSubmit handles keys while the submit sheet is up.
func (d *ReaderDialog) updateSubmit(msg tea.KeyPressMsg) (*ReaderDialog, tea.Cmd) {
	if d.submitting {
		// The request is already on its way to GitHub; nothing typed here can
		// recall it, and letting esc close the sheet would leave the user
		// believing they cancelled a review that is about to land.
		return d, nil
	}

	switch msg.String() {
	case "esc":
		d.mode = modeNormal
		d.resetSubmit()
		d.toast = "nothing submitted"

	case "tab", "right":
		d.submitEvent = (d.submitEvent + 1) % len(submitEvents)
		d.submitErr = ""
	case "shift+tab", "left":
		d.submitEvent = (d.submitEvent + len(submitEvents) - 1) % len(submitEvents)
		d.submitErr = ""

	case "ctrl+j":
		d.submitBody += "\n"

	case "enter":
		if reason := d.submitBlocker(); reason != "" {
			d.submitErr = reason
			return d, nil
		}
		d.submitting, d.submitErr = true, ""
		key, event, body := d.key(), d.chosenEvent(), strings.TrimSpace(d.submitBody)
		return d, func() tea.Msg {
			return reviewSubmitRequestMsg{key: key, event: event, body: body}
		}

	case "backspace":
		if n := len([]rune(d.submitBody)); n > 0 {
			d.submitBody = string([]rune(d.submitBody)[:n-1])
		}

	default:
		// Text and never String(), for the same reason the comment box reads
		// it: a summary you cannot put a space in is not a summary.
		if t := typedText(msg); t != "" {
			d.submitBody += t
		}
	}
	return d, nil
}

// SubmitDone reports the outcome of a submission back to the sheet.
//
// A failure keeps the sheet open with the reason on it: a rate limit or a line
// GitHub would not anchor are both things you retry after a change, and closing
// the sheet would throw away the summary that has to be retyped.
func (d *ReaderDialog) SubmitDone(err error, count int) {
	d.submitting = false
	if err != nil {
		d.submitErr = err.Error()
		return
	}
	label := submitLabels[d.submitEvent]
	d.mode = modeNormal
	d.resetSubmit()
	if d.doc != nil {
		d.doc.MarkSent()
		d.rebuildRows()
	}
	d.toast = label + " submitted"
	if count > 0 {
		d.toast = label + " submitted with " + plural(count, "comment")
	}
}

// updateCompose handles keys while the comment box is open.
func (d *ReaderDialog) updateCompose(msg tea.KeyPressMsg) (*ReaderDialog, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Discards. A draft that survived being cancelled would be a second,
		// invisible place comments live.
		d.doc.SetDraft(nil)
		d.mode, d.draft, d.editingID = modeNormal, review.Comment{}, 0
		d.rebuildRows()
		d.toast = "discarded"

	case "tab":
		d.draft.Kind = d.draft.Kind.Next()
		d.doc.SetDraft(&d.draft)
	case "shift+tab":
		d.draft.Kind = d.draft.Kind.Prev()
		d.doc.SetDraft(&d.draft)

	case "ctrl+j":
		d.draft.Body += "\n"
		d.doc.SetDraft(&d.draft)
		d.syncDraftCursor()

	case "enter":
		text := strings.TrimSpace(d.draft.Body)
		kind, file, line := d.draft.Kind, d.draft.File, d.draft.Line
		d.doc.SetDraft(nil)
		d.mode, d.draft, d.editingID = modeNormal, review.Comment{}, 0
		if text == "" {
			d.rebuildRows()
			d.toast = "empty — nothing saved"
			return d, nil
		}
		d.doc.AddComment(review.Comment{File: file, Line: line, Kind: kind, Body: text})
		d.rebuildRows()
		d.toast = strings.ToLower(kind.String()) + " added on line " + itoa(line)
		return d, func() tea.Msg {
			return reviewCommentMsg{key: d.key(), file: file, line: line, kind: kind, body: text}
		}

	case "backspace":
		if n := len([]rune(d.draft.Body)); n > 0 {
			d.draft.Body = string([]rune(d.draft.Body)[:n-1])
			d.doc.SetDraft(&d.draft)
			d.syncDraftCursor()
		}

	default:
		if t := typedText(msg); t != "" {
			d.draft.Body += t
			d.doc.SetDraft(&d.draft)
			d.syncDraftCursor()
		}
	}
	return d, nil
}

// syncDraftCursor keeps the box being typed into on screen as it grows.
func (d *ReaderDialog) syncDraftCursor() {
	d.rows = buildRows(d.doc.Stream, d.split)
	for i, r := range d.rows {
		s := r.primary()
		if s < 0 || s >= len(d.doc.Stream.Lines) {
			continue
		}
		if l := d.doc.Stream.Lines[s]; l.Draft && l.Kind == review.LineCommentBottom {
			d.cursor = i
			break
		}
	}
	d.clampScroll()
}

// moveTo scrolls, keeping the cursor inside the viewport.
func (d *ReaderDialog) moveTo(i int) {
	if i < 0 {
		i = 0
	}
	if n := len(d.rows); i >= n {
		i = n - 1
	}
	if i < 0 {
		return
	}
	d.cursor = i
	d.clampScroll()
	d.followDiff()
}

// followDiff keeps the tree's selection on the file the diff is showing.
//
// The tree is a picture of where you are, so the two must never disagree —
// scrolling into a new file with ↑↓, ]/[, space or a search hit moves the
// selection exactly as picking it in the tree would have.
//
// Skipped while the tree HOLDS the keyboard: there it is the one driving, and
// dragging its own selection back would fight the arrow keys.
func (d *ReaderDialog) followDiff() {
	if d.treeFocus || d.doc == nil {
		return
	}
	cur := d.doc.Stream.FileAt(d.streamAt(d.cursor))
	if cur == "" {
		return
	}
	for i, n := range d.treeView {
		if !n.IsDir && n.Path == cur {
			d.treeCursor = i
			return
		}
	}
}

func (d *ReaderDialog) clampScroll() {
	if d.cursor < 0 {
		d.cursor = 0
	}
	if n := len(d.rows); d.cursor >= n {
		d.cursor = max(n-1, 0)
	}
	switch {
	case d.cursor < d.top:
		d.top = d.cursor
	case d.cursor >= d.top+d.body():
		d.top = d.cursor - d.body() + 1
	}
	if d.top < 0 {
		d.top = 0
	}
}

// jumpStream places a stream index near the top of the viewport rather than
// wherever scrolling would leave it — the point of jumping to a hunk is to read
// it, and reading it from the last row of the screen is not that.
func (d *ReaderDialog) jumpStream(streamIdx int) {
	i := d.rowOfStream(streamIdx)
	d.moveTo(i)
	d.top = i - 2
	if d.top < 0 {
		d.top = 0
	}
	if maxTop := len(d.rows) - d.body(); d.top > maxTop && maxTop > 0 {
		d.top = maxTop
	}
	d.followDiff()
}

// PR is the pull request the reader is open on, or 0.
func (d *ReaderDialog) PR() int {
	if !d.visible {
		return 0
	}
	return d.pr
}
