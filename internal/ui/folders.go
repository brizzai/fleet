package ui

import (
	"time"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

// Folder groups: inside an area, origins whose main checkout sits two or more
// directories below the area's path are gathered under a header named after
// the first directory below it (area ~/P, repo ~/P/Youtube/lastro → "Youtube").
// One level only. The grouping is a post-pass over BuildFlatItems' output, so
// that function and its callers' arguments are untouched.

// folderExpandPrefix namespaces folder keys in the shared expand map and in
// collapsed_groups, beside "origin:" and the checkout paths.
const folderExpandPrefix = "folder:"

// FolderExpandKey is the expand/persist key of a folder group. It includes the
// area, so equal folder names in different areas never merge.
func FolderExpandKey(areaName, folder string) string {
	return folderExpandPrefix + areaName + "/" + folder
}

// FolderOf maps an origin's main checkout path to its folder group, as
// (area, folder); an empty folder means "not grouped".
type FolderOf func(repoPath string) (areaName, folder string)

// areaFolderOf resolves folder groups for this window, memoized: the worktree
// fallback shells out to git, which must not run per frame. Nil when no areas
// are configured, which leaves the sidebar exactly as it was.
func (h *Home) areaFolderOf() FolderOf {
	if !h.areaEnabled() {
		return nil
	}
	return func(path string) (string, string) {
		h.areas.mu.Lock()
		defer h.areas.mu.Unlock()
		if f, ok := h.areas.folders[path]; ok {
			return f[0], f[1]
		}
		a, f := h.areas.matcher.Folder(path, func() string { return git.GetMainWorktreePath(path) })
		if h.areas.folders == nil {
			h.areas.folders = make(map[string][2]string)
		}
		h.areas.folders[path] = [2]string{a, f}
		return a, f
	}
}

// GroupFolders reshapes a BuildFlatItems result into folder groups. Entries —
// a loose origin, or a folder holding several — keep the origins' own order,
// each placed where its first member was. A collapsed folder emits only its
// header; members are indented one level. With a nil folderOf, or when no
// origin has a folder, items come back unchanged.
func GroupFolders(items []SidebarItem, folderOf FolderOf, expanded map[string]bool) []SidebarItem {
	if folderOf == nil {
		return items
	}
	type block struct {
		items  []SidebarItem
		folder string // FolderExpandKey, "" = loose
		label  string
	}
	var blocks []block
	for _, it := range items {
		switch {
		case it.IsSpacer:
			continue // regenerated below
		case it.IsOriginHeader:
			b := block{}
			if a, f := folderOf(it.FolderPath); f != "" {
				b.folder, b.label = FolderExpandKey(a, f), f
			}
			blocks = append(blocks, b)
		}
		if len(blocks) == 0 {
			return items // not shaped like BuildFlatItems output; leave it alone
		}
		blocks[len(blocks)-1].items = append(blocks[len(blocks)-1].items, it)
	}

	type entry struct {
		key, label string
		blocks     []*block
	}
	var entries []*entry
	byKey := map[string]*entry{}
	grouped := false
	for i := range blocks {
		b := &blocks[i]
		if b.folder == "" {
			entries = append(entries, &entry{blocks: []*block{b}})
			continue
		}
		grouped = true
		e := byKey[b.folder]
		if e == nil {
			e = &entry{key: b.folder, label: b.label}
			byKey[b.folder] = e
			entries = append(entries, e)
		}
		e.blocks = append(e.blocks, b)
	}
	if !grouped {
		return items
	}

	out := make([]SidebarItem, 0, len(items)+len(entries))
	for _, e := range entries {
		if len(out) > 0 && SidebarDensity != "compact" {
			out = append(out, SidebarItem{IsSpacer: true})
		}
		if e.key == "" {
			out = append(out, e.blocks[0].items...)
			continue
		}
		hdr := SidebarItem{
			IsRepoHeader:   true,
			IsFolderHeader: true,
			FolderKey:      e.key,
			FolderLabel:    e.label,
			OriginLabel:    e.label, // read by the shared header renderer
			Expanded:       IsExpanded(expanded, e.key),
			StatusCounts:   map[session.Status]int{},
		}
		allSnoozed := true
		var soonest time.Time
		for _, b := range e.blocks {
			oh := b.items[0]
			hdr.SessionCount += oh.SessionTotal
			for st, n := range oh.StatusCounts {
				hdr.StatusCounts[st] += n
			}
			// The folder shows a countdown only when every member origin is
			// snoozed itself; the earliest wake is the one that matters.
			if oh.GroupSnooze.IsZero() {
				allSnoozed = false
			} else if soonest.IsZero() || oh.GroupSnooze.Before(soonest) {
				soonest = oh.GroupSnooze
			}
		}
		if allSnoozed {
			hdr.GroupSnooze = soonest
		}
		out = append(out, hdr)
		if !hdr.Expanded {
			continue
		}
		for _, b := range e.blocks {
			for _, it := range b.items {
				it.Depth = 1
				out = append(out, it)
			}
		}
	}
	return out
}
