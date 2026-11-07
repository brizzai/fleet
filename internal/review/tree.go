package review

import (
	"sort"
	"strings"
)

// TreeNode is one row of the file tree.
type TreeNode struct {
	Label string // what renders — a directory segment, or a file's basename
	// Path is the full path: a file's path, or a directory's own prefix. Both
	// are set, so a directory has a stable identity to fold against — check
	// IsDir before treating one as a file to open.
	Path   string
	Depth  int
	IsDir  bool
	Adds   int
	Dels   int
	Status string // added, modified, removed, renamed
}

// BuildTree turns a flat list of paths into a nested, renderable tree.
//
// Directories with a single child are collapsed into one row — `pkg/shared/`
// rather than `pkg/` then `shared/` — the way GitHub's own tree does it. A
// monorepo's paths are mostly shared prefix, and spending a row on each empty
// level pushes the filenames off the edge of a narrow panel.
func BuildTree(files []TreeFile) []TreeNode {
	root := &dirNode{children: map[string]*dirNode{}}
	for _, f := range files {
		parts := strings.Split(f.Path, "/")
		cur := root
		for _, seg := range parts[:len(parts)-1] {
			next, ok := cur.children[seg]
			if !ok {
				next = &dirNode{name: seg, children: map[string]*dirNode{}}
				cur.children[seg] = next
			}
			cur = next
		}
		cur.files = append(cur.files, f)
	}
	var out []TreeNode
	walk(root, 0, "", "", &out)
	return out
}

// TreeFile is the input to BuildTree.
type TreeFile struct {
	Path      string
	Additions int
	Deletions int
	Status    string
}

type dirNode struct {
	name     string
	children map[string]*dirNode
	files    []TreeFile
}

func walk(d *dirNode, depth int, prefix, parent string, out *[]TreeNode) {
	names := make([]string, 0, len(d.children))
	for n := range d.children {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		child := d.children[n]
		label := prefix + n
		// Collapse a directory that holds exactly one directory and no files
		// of its own: it carries no information a joined label does not.
		for len(child.children) == 1 && len(child.files) == 0 {
			for k, v := range child.children {
				label += "/" + k
				child = v
			}
		}
		full := parent + label
		*out = append(*out, TreeNode{Label: label + "/", Path: full, Depth: depth, IsDir: true})
		walk(child, depth+1, "", full+"/", out)
	}

	sort.SliceStable(d.files, func(i, j int) bool { return d.files[i].Path < d.files[j].Path })
	for _, f := range d.files {
		base := f.Path
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		*out = append(*out, TreeNode{
			Label:  base,
			Path:   f.Path,
			Depth:  depth,
			Adds:   f.Additions,
			Dels:   f.Deletions,
			Status: f.Status,
		})
	}
}
