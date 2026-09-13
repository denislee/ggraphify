package board

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Group is one folder the board can be narrowed to: a scan root, or one
// directory immediately below a root that holds checkouts of its own.
//
// A group is a directory path rather than a name, which is what makes the
// membership rule a single line and makes two roots that happen to end in the
// same name — ~/git/api and ~/tmp/api — two groups rather than one.
type Group struct {
	Path  string `json:"path"`  // the absolute directory this group is
	Label string `json:"label"` // how it is shown: ~/git, or the subdirectory's name
	Root  string `json:"root"`  // the scan root it belongs to
	Depth int    `json:"depth"` // 0 for a root, 1 for a directory under one
	Count int    `json:"count"` // how many rows are in it
}

// MinSubgroup is how many checkouts a directory below a root must hold before
// it is offered as a folder of its own. Roots are always offered, however
// empty: they are what the user configured.
const MinSubgroup = 2

// InGroup reports whether a row belongs to a group. The empty group is every
// row, because "all folders" is the absence of a filter rather than a group of
// its own.
func InGroup(r Row, group string) bool {
	if strings.TrimSpace(group) == "" {
		return true
	}
	g := filepath.Clean(group)
	return r.Path == g || strings.HasPrefix(r.Path, g+string(filepath.Separator))
}

// Groups derives the folder list from the rows themselves rather than from the
// configured roots, so a root that currently holds nothing does not appear as
// a filter that can only ever show zero repositories, and a subdirectory that
// appeared this morning does.
//
// Each root is followed by the directories directly below it that hold
// checkouts — one level, not the whole tree: ~/git and ~/git/nova are places
// somebody keeps repositories, ~/git/nova/services/api is one repository's
// parent and filtering to it would be the same as selecting the row.
func Groups(rows []Row) []Group {
	type acc struct {
		root  string
		depth int
		count int
	}
	seen := map[string]*acc{}
	note := func(path, root string, depth int) {
		if path == "" {
			return
		}
		a := seen[path]
		if a == nil {
			a = &acc{root: root, depth: depth}
			seen[path] = a
		}
		a.count++
	}
	for _, r := range rows {
		root := r.Root
		if root == "" {
			root = filepath.Dir(r.Path)
		}
		note(root, root, 0)
		if seg := firstSegment(r.Group); seg != "" {
			note(filepath.Join(root, seg), root, 1)
		}
	}

	out := make([]Group, 0, len(seen))
	for path, a := range seen {
		if a.depth > 0 && a.count < MinSubgroup {
			// A directory holding a single checkout is not a folder worth
			// filtering to: narrowing to it shows the one row that selecting
			// it would have shown, and a dropdown full of those is noise.
			continue
		}
		g := Group{Path: path, Root: a.root, Depth: a.depth, Count: a.count}
		if a.depth == 0 {
			g.Label = Tilde(path)
		} else {
			g.Label = filepath.Base(path)
		}
		out = append(out, g)
	}
	// Roots in path order, each immediately followed by its own subdirectories
	// — the order the dropdown renders top to bottom.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Root != out[j].Root {
			return out[i].Root < out[j].Root
		}
		if out[i].Depth != out[j].Depth {
			return out[i].Depth < out[j].Depth
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// GroupOf is the most specific group a row belongs to, which is what "show me
// the rest of this one's folder" has to resolve to.
func GroupOf(r Row) string {
	root := r.Root
	if root == "" {
		root = filepath.Dir(r.Path)
	}
	if seg := firstSegment(r.Group); seg != "" {
		return filepath.Join(root, seg)
	}
	return root
}

// Tilde shortens a path under the home directory the way a shell prompt does.
// A folder filter is read at a glance and "~/git" is that; the absolute path
// is three times as wide and says the same thing.
func Tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// firstSegment is the first path element of a repository's group — the
// directory under the root, not the whole relative path.
func firstSegment(group string) string {
	group = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(group)), "/")
	if group == "" || group == "." {
		return ""
	}
	if i := strings.IndexByte(group, '/'); i >= 0 {
		return group[:i]
	}
	return group
}
