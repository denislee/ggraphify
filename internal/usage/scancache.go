package usage

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// racyWindow is how old a directory's mtime must be before a listing of it is
// trusted on the strength of that mtime alone. Kernel timestamps come from a
// coarse clock, so two changes a few milliseconds apart can leave the same
// mtime behind; a listing taken while the directory was that fresh is redone
// on the next tick instead of being cached.
const racyWindow = 2 * time.Second

// dirStamp is what a cached listing was taken against.
type dirStamp struct {
	path   string
	mod    int64
	stable bool
	ok     bool
}

// fresh reports whether a listing taken at d still describes the directory fi
// was read from.
func (d *dirStamp) fresh(fi os.FileInfo) bool {
	return d.ok && d.stable && d.mod == fi.ModTime().UnixNano()
}

func (d *dirStamp) mark(fi os.FileInfo) {
	d.ok = true
	d.mod = fi.ModTime().UnixNano()
	d.stable = time.Since(fi.ModTime()) > racyWindow
}

// acctListing is one account's projects directory, as last listed.
type acctListing struct {
	root     dirStamp
	projects []*projListing
}

// projListing is one project directory's transcripts, as last listed.
type projListing struct {
	dirStamp
	files []string
}

// transcripts appends the transcript files under one account directory to out.
//
// The layout is projects/<slugified-cwd>/<session-id>.jsonl, with sidecar
// directories of the same name beside the files; only the .jsonl files are
// returned, and the slug is not decoded — the cwd inside each record is the
// authoritative one, and the slug is lossy about separators.
//
// A directory is re-listed only when its mtime moved: a transcript being
// appended to does not change its directory, a new or removed one does. The
// order is the one os.ReadDir gives, exactly as when every directory was
// listed every tick.
func (sc *scanCache) transcripts(dir string, out []string) ([]string, error) {
	if sc.accounts == nil {
		sc.accounts = map[string]*acctListing{}
	}
	a := sc.accounts[dir]
	if a == nil {
		a = &acctListing{root: dirStamp{path: filepath.Join(dir, "projects")}}
		sc.accounts[dir] = a
	}
	fi, err := os.Stat(a.root.path)
	if err == nil && !a.root.fresh(fi) {
		var ents []os.DirEntry
		ents, err = os.ReadDir(a.root.path)
		if err == nil {
			old := make(map[string]*projListing, len(a.projects))
			for _, p := range a.projects {
				old[p.path] = p
			}
			projects := make([]*projListing, 0, len(ents))
			for _, e := range ents {
				if !e.IsDir() {
					continue
				}
				path := filepath.Join(a.root.path, e.Name())
				p := old[path]
				if p == nil {
					p = &projListing{dirStamp: dirStamp{path: path}}
				}
				projects = append(projects, p)
			}
			a.projects = projects
			a.root.mark(fi)
		}
	}
	if err != nil {
		a.root.ok, a.projects = false, nil
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	for _, p := range a.projects {
		pfi, err := os.Stat(p.path)
		if err != nil {
			p.ok, p.files = false, nil
			continue
		}
		if !p.fresh(pfi) {
			files, err := os.ReadDir(p.path)
			if err != nil {
				p.ok, p.files = false, nil
				continue
			}
			p.files = p.files[:0]
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
					continue
				}
				p.files = append(p.files, filepath.Join(p.path, f.Name()))
			}
			p.mark(pfi)
		}
		out = append(out, p.files...)
	}
	return out, nil
}

// forgetAccounts drops the listings of accounts no longer configured.
func (sc *scanCache) forgetAccounts(accts []Account) {
	for dir := range sc.accounts {
		found := false
		for _, a := range accts {
			if a.Dir == dir {
				found = true
				break
			}
		}
		if !found {
			delete(sc.accounts, dir)
		}
	}
}

// sessionDirChanged reports whether a repository's graft session directory
// may hold anything the last update did not see, and returns its path.
//
// graft writes each counter file by creating it beside the old one and
// renaming it over, which moves the directory's mtime; an unchanged mtime
// therefore means unchanged files, and the directory is not read at all. A
// directory that does not exist is never "changed": there is nothing in it.
func (sc *scanCache) sessionDirChanged(repo string) (string, bool) {
	if sc.sessDirs == nil {
		sc.sessDirs = map[string]*dirStamp{}
	}
	d := sc.sessDirs[repo]
	if d == nil {
		d = &dirStamp{path: GraftSessionDir(repo)}
		sc.sessDirs[repo] = d
	}
	fi, err := os.Stat(d.path)
	if err != nil {
		d.ok = false
		return d.path, false
	}
	if d.fresh(fi) {
		return d.path, false
	}
	d.mark(fi)
	return d.path, true
}
