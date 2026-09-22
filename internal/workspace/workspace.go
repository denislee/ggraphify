// Package workspace reads graft's cross-repository federation — the
// workspace.json graft writes at a directory that holds checkouts rather than
// code — without running graft at all.
//
// It is the graft-side twin of internal/globalgraph, and it exists for the
// same reason: the board already knows which checkouts are under a root, and
// the only other fact needed to say whether the federation is current is a
// few kilobytes of JSON listing the children graft last federated.
//
// The distinction that makes this a separate package from graftstate: a
// graftstate.Index is one checkout's own graft/ directory, built from that
// checkout's code. A workspace is the directory ABOVE those checkouts, whose
// graft/ holds no cards at all — only the list of children `graft ask` fans a
// query out across. A root and a checkout both have a graft/ directory and
// they are not the same thing, so asking graftstate about a root would get
// back "broken: no readable wiring.json", which is the ordinary and correct
// state of every workspace root there has ever been.
//
// Nothing here writes. The federation is graft's file, and `graft build
// <root>` is the only thing that rewrites it — which also makes the repair
// self-pruning: graft lists the children it finds, so a child that has been
// deleted leaves the file by the same command that adds a new one.
package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The names are graft's, not this board's.
const (
	DirName  = "graft"
	FileName = "workspace.json"
)

// Path is where the federation for a root lives.
func Path(root string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	return filepath.Join(root, DirName, FileName)
}

// File is the federation as graft wrote it.
//
// The zero value, and a nil *File, are both usable and mean "this root is not
// a graft workspace", which is the ordinary state of most directories — every
// method is nil-safe.
type File struct {
	// Path is the file this was read from, for the UI to name.
	Path string
	// Root is the directory the federation belongs to.
	Root string
	// Children is every child graft federates, sorted. They are directory
	// names relative to the root, which is how graft stores them.
	Children []string
	// Exists is false when there is no workspace.json at all. It is distinct
	// from a federation that exists and is empty.
	Exists bool
	// Err is why a file that exists could not be read. An unreadable
	// federation must not read as "no children", because that answer invites
	// a rebuild of the whole root on every tick — so the error is carried
	// rather than swallowed.
	Err error

	set map[string]bool
}

type rawFile struct {
	Version  int      `json:"version"`
	Children []string `json:"children"`
}

// Load reads the federation at root. A missing file is not an error: it is
// the answer "graft has never federated this directory".
func Load(root string) *File {
	f := &File{Path: Path(root), Root: root, set: map[string]bool{}}
	if f.Path == "" {
		return f
	}
	b, err := os.ReadFile(f.Path)
	if err != nil {
		if !os.IsNotExist(err) {
			f.Exists, f.Err = true, err
		}
		return f
	}
	f.Exists = true

	var raw rawFile
	if err := json.Unmarshal(b, &raw); err != nil {
		f.Err = err
		return f
	}
	for _, c := range raw.Children {
		c = strings.Trim(strings.TrimSpace(c), "/")
		if c == "" || f.set[c] {
			continue
		}
		f.set[c] = true
		f.Children = append(f.Children, c)
	}
	sort.Strings(f.Children)
	return f
}

// Has reports whether a child is federated.
func (f *File) Has(child string) bool {
	if f == nil {
		return false
	}
	return f.set[child]
}

// Len is how many children the federation carries.
func (f *File) Len() int {
	if f == nil {
		return 0
	}
	return len(f.Children)
}

// IssueDrift is the one defect a federation can have that anything can act
// on: the children graft recorded are not the checkouts that are there now.
//
// It is a string for the same reason graphstate's, graftstate's and
// globalgraph's codes are — it goes into the auto-fix loop's signature, where
// "what is wrong here" has to compare as a value rather than as a struct.
const IssueDrift = "workspace-drift"

// State is one root's verdict: what graft federates versus what is on disk.
type State struct {
	// Root is the directory this is about.
	Root string
	// File is the federation as read, never nil after Inspect.
	File *File
	// Missing is every checkout on disk that graft does not federate, sorted.
	// A query at the root answers without them.
	Missing []string
	// Orphans is every federated child with no checkout on disk, sorted.
	// graft resolves each one to a directory that is not there.
	Orphans []string
}

// Inspect compares a root's federation against the checkouts the board found
// under it. Children are directory names relative to the root.
//
// It is pure apart from the one file Load reads: the caller supplies what is
// on disk, because the board has already walked it and a second walk of a
// directory holding a hundred checkouts on every tick is not free.
//
// A root with no federation at all is not drift and produces no state: graft
// has never been asked to federate it, and a loop that federated every
// directory that happens to hold two checkouts would be answering a question
// nobody asked. Joining is Inspect's caller's decision — see autofix.Fleet.
func Inspect(root string, onDisk []string) State {
	s := State{Root: root, File: Load(root)}
	if !s.File.Exists || s.File.Err != nil {
		return s
	}
	have := make(map[string]bool, len(onDisk))
	for _, c := range onDisk {
		c = strings.Trim(strings.TrimSpace(c), "/")
		if c == "" {
			continue
		}
		have[c] = true
		if !s.File.Has(c) {
			s.Missing = append(s.Missing, c)
		}
	}
	for _, c := range s.File.Children {
		if !have[c] {
			s.Orphans = append(s.Orphans, c)
		}
	}
	sort.Strings(s.Missing)
	sort.Strings(s.Orphans)
	return s
}

// Issue is this federation's defect code, or "" when there is nothing to do.
//
// A root that is not a workspace has no defect — it is a directory, and most
// directories are. An unreadable one has no defect either, for a sharper
// reason: the remedy for a file this board cannot parse is a person reading
// it, not a rebuild of a hundred checkouts launched every cooldown forever.
func (s State) Issue() string {
	if s.File == nil || !s.File.Exists || s.File.Err != nil {
		return ""
	}
	if len(s.Missing) == 0 && len(s.Orphans) == 0 {
		return ""
	}
	return IssueDrift
}

// Summary is the drift in one line, for a log or a tooltip.
func (s State) Summary() string {
	switch {
	case len(s.Missing) > 0 && len(s.Orphans) > 0:
		return itoa(len(s.Missing)) + " unfederated, " + itoa(len(s.Orphans)) + " gone"
	case len(s.Missing) > 0:
		return itoa(len(s.Missing)) + " unfederated"
	case len(s.Orphans) > 0:
		return itoa(len(s.Orphans)) + " gone"
	}
	return "federated"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
