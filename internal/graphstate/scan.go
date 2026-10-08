package graphstate

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// This file is graph.json's byte scanner: the counters the board shows, read
// without json.Decoder.Token.
//
// Token boxes every scalar and allocates every string, so counting a 46 MB
// graph that way cost about two million allocations to produce three ints —
// and the board did it for every repository about once a minute. The scanner
// below tracks only what counting needs (nesting depth, whether it is inside a
// string, whether the previous byte was an escape) over one reused 64 KB
// buffer, and allocates nothing per element.
//
// It validates structure, not grammar: a truncated file — what a killed build
// leaves — is an error exactly as it was, but a malformed scalar inside an
// element (`[1 2]`, `tru`) is not diagnosed. The counts on any well-formed
// graph.json are identical to the decoder's; scan_test.go holds that against
// the previous implementation.

// errNotObject is a graph.json whose root is not an object; the message is the
// one the decoder-based reader gave.
var errNotObject = errors.New("not a JSON object")

// scanBufSize is the read buffer. Large enough that a graph.json is a few
// hundred reads, small enough to keep one per worker without noticing.
const scanBufSize = 64 << 10

// tailBytes is how much of the end of graph.json the stamp lookup reads.
// graphify writes built_at_commit as the last top-level member, so it is in
// the last few dozen bytes; 4 KB leaves room for trailing whitespace.
const tailBytes = 4 << 10

var scannerPool = sync.Pool{New: func() any { return &scanner{buf: make([]byte, scanBufSize)} }}

type scanner struct {
	rd   io.Reader
	buf  []byte
	i, n int
	base int64 // file offset of buf[0]
	err  error

	// capture, when on, copies every consumed byte into cap — used only for
	// the small "graph" metadata object, never for the arrays.
	capturing bool
	capStart  int
	cap       []byte

	key    []byte // the current top-level key, reused
	keyEnd int64  // file offset just past the current key's closing quote
	str    []byte // the built_at_commit value, reused
}

func getScanner(r io.Reader) *scanner {
	s := scannerPool.Get().(*scanner)
	s.rd, s.i, s.n, s.base, s.err = r, 0, 0, 0, nil
	s.capturing = false
	return s
}

func putScanner(s *scanner) {
	s.rd = nil
	if cap(s.cap) > 1<<20 {
		s.cap = nil // one freak "graph" object must not be pinned forever
	}
	scannerPool.Put(s)
}

// fill refills the buffer. It reports false at end of input or on a read
// error; s.err then says which.
func (s *scanner) fill() bool {
	if s.capturing {
		s.cap = append(s.cap, s.buf[s.capStart:s.n]...)
		s.capStart = 0
	}
	s.base += int64(s.n)
	s.i, s.n = 0, 0
	for s.n == 0 {
		if s.err != nil {
			return false
		}
		s.n, s.err = s.rd.Read(s.buf)
	}
	return true
}

// offset is the file offset of the next unread byte.
func (s *scanner) offset() int64 { return s.base + int64(s.i) }

// fail is the error for input that ended (or failed) mid-value.
func (s *scanner) fail() error {
	if s.err != nil && s.err != io.EOF {
		return s.err
	}
	return io.ErrUnexpectedEOF
}

// peekNonSpace skips whitespace and returns the next byte without consuming it.
func (s *scanner) peekNonSpace() (byte, bool) {
	for {
		for s.i < s.n {
			switch c := s.buf[s.i]; c {
			case ' ', '\t', '\n', '\r':
				s.i++
			default:
				return c, true
			}
		}
		if !s.fill() {
			return 0, false
		}
	}
}

// skipString consumes the rest of a string whose opening quote has been read.
func (s *scanner) skipString() error {
	esc := false
	for {
		for s.i < s.n {
			c := s.buf[s.i]
			s.i++
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				return nil
			}
		}
		if !s.fill() {
			return s.fail()
		}
	}
}

// readString consumes the rest of a string whose opening quote has been read,
// appending its raw (still escaped) bytes to dst, up to limit. escaped reports whether it held
// any escape at all, in which case dst is not the decoded value.
func (s *scanner) readString(dst []byte, limit int) (out []byte, escaped bool, err error) {
	esc := false
	for {
		for s.i < s.n {
			c := s.buf[s.i]
			s.i++
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc, escaped = true, true
			case c == '"':
				return dst, escaped, nil
			}
			if len(dst) < limit {
				dst = append(dst, c)
			}
		}
		if !s.fill() {
			return dst, escaped, s.fail()
		}
	}
}

// skipNested consumes the rest of an object or array whose opening delimiter
// has been read, however deep it goes.
func (s *scanner) skipNested() error {
	depth, inStr, esc := 1, false, false
	for {
		for s.i < s.n {
			c := s.buf[s.i]
			s.i++
			if inStr {
				switch {
				case esc:
					esc = false
				case c == '\\':
					esc = true
				case c == '"':
					inStr = false
				}
				continue
			}
			switch c {
			case '"':
				inStr = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return nil
				}
			}
		}
		if !s.fill() {
			return s.fail()
		}
	}
}

// skipScalar consumes a number or literal: everything up to the next
// delimiter or whitespace.
func (s *scanner) skipScalar() error {
	for {
		for s.i < s.n {
			switch s.buf[s.i] {
			case ',', ']', '}', ' ', '\t', '\n', '\r':
				return nil
			}
			s.i++
		}
		if !s.fill() {
			if s.err == io.EOF {
				return nil // a bare scalar at the very end; the caller decides
			}
			return s.fail()
		}
	}
}

// skipValue consumes exactly one JSON value.
func (s *scanner) skipValue() error {
	c, ok := s.peekNonSpace()
	if !ok {
		return s.fail()
	}
	switch c {
	case '"':
		s.i++
		return s.skipString()
	case '{', '[':
		s.i++
		return s.skipNested()
	case ',', ']', '}', ':':
		return errors.New("graphstate: unexpected '" + string(c) + "' where a value belongs")
	}
	return s.skipScalar()
}

// countArray consumes one value and, when it is an array, counts its
// elements. A non-array value counts as zero, as it did under the decoder.
func (s *scanner) countArray() (int, error) {
	c, ok := s.peekNonSpace()
	if !ok {
		return 0, s.fail()
	}
	if c != '[' {
		return 0, s.skipValue()
	}
	s.i++
	n := 0
	for {
		c, ok := s.peekNonSpace()
		if !ok {
			return n, s.fail()
		}
		if c == ']' {
			s.i++
			return n, nil
		}
		if n > 0 {
			if c != ',' {
				return n, errors.New("graphstate: expected ',' or ']' in array")
			}
			s.i++
		}
		if err := s.skipValue(); err != nil {
			return n, err
		}
		n++
	}
}

// expect consumes c, after whitespace, or fails.
func (s *scanner) expect(c byte) error {
	got, ok := s.peekNonSpace()
	if !ok {
		return s.fail()
	}
	if got != c {
		return errors.New("graphstate: expected '" + string(c) + "', got '" + string(got) + "'")
	}
	s.i++
	return nil
}

// member walks one top-level object, calling fn with each key (raw bytes, valid
// only for the call) after its colon has been consumed. fn must consume the
// value. escaped reports a key that held an escape sequence, which this scanner
// does not decode — no graphify key does.
func (s *scanner) members(fn func(key []byte, escaped bool) error) error {
	c, ok := s.peekNonSpace()
	if !ok {
		if s.err != nil && s.err != io.EOF {
			return s.err
		}
		return io.EOF // an empty file, as the decoder reported it
	}
	if c != '{' {
		return errNotObject
	}
	s.i++
	first := true
	for {
		c, ok := s.peekNonSpace()
		if !ok {
			return s.fail()
		}
		if c == '}' {
			s.i++
			return nil
		}
		if !first {
			if c != ',' {
				return errors.New("graphstate: expected ',' or '}' in object")
			}
			s.i++
		}
		first = false
		if err := s.expect('"'); err != nil {
			return err
		}
		var (
			escaped bool
			err     error
		)
		s.key, escaped, err = s.readString(s.key[:0], 64)
		if err != nil {
			return err
		}
		s.keyEnd = s.offset()
		if err := s.expect(':'); err != nil {
			return err
		}
		if err := fn(s.key, escaped); err != nil {
			return err
		}
	}
}

// commitValue consumes built_at_commit's value. A string is the commit; null
// is the empty commit (a decode of null into a string leaves it empty); any
// other type is an error, as the typed decode made it.
func (s *scanner) commitValue() (string, error) {
	c, ok := s.peekNonSpace()
	if !ok {
		return "", s.fail()
	}
	switch c {
	case '"':
		s.i++
		start := s.offset()
		v, escaped, err := s.readString(s.str[:0], 64<<10)
		s.str = v
		if err != nil {
			return "", err
		}
		if !escaped && s.offset()-start-1 == int64(len(v)) {
			return string(v), nil
		}
		// Escaped, or longer than the copy kept: no commit id looks like
		// either. Decode what was kept rather than invent a third rule.
		var out string
		q := append(append([]byte{'"'}, v...), '"')
		if err := json.Unmarshal(q, &out); err != nil {
			return "", err
		}
		return out, nil
	case 'n':
		return "", s.skipScalar()
	}
	if err := s.skipValue(); err != nil {
		return "", err
	}
	return "", errors.New("json: cannot unmarshal built_at_commit into Go value of type string")
}

// captureValue consumes one value and returns its bytes, valid until the next
// capture.
func (s *scanner) captureValue() ([]byte, error) {
	if _, ok := s.peekNonSpace(); !ok {
		return nil, s.fail()
	}
	s.cap = s.cap[:0]
	s.capturing, s.capStart = true, s.i
	err := s.skipValue()
	s.cap = append(s.cap, s.buf[s.capStart:s.i]...)
	s.capturing = false
	return s.cap, err
}

// readGraph pulls the counters out of graph.json with the byte scanner: the
// element counts of nodes, links/edges and hyperedges, and built_at_commit.
// Neither the arrays nor any string inside them is ever materialised.
func readGraph(path string, g *Graph) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return scanGraph(f, g)
}

func scanGraph(r io.Reader, g *Graph) error {
	s := getScanner(r)
	defer putScanner(s)
	return s.members(func(key []byte, escaped bool) error {
		if escaped {
			return s.skipValue()
		}
		switch string(key) { // no allocation: a switch on a converted []byte
		case "nodes":
			n, err := s.countArray()
			if err != nil {
				return err
			}
			g.Nodes = n
		case "links", "edges":
			n, err := s.countArray()
			if err != nil {
				return err
			}
			g.Links += n
		case "hyperedges":
			n, err := s.countArray()
			if err != nil {
				return err
			}
			g.Hyperedges = n
		case "built_at_commit":
			v, err := s.commitValue()
			if err != nil {
				return err
			}
			g.BuiltCommit = v
		case "graph":
			// Some versions carry built_at_commit inside a "graph" object;
			// 0.9.58 writes a bare 0 here and puts the commit at top level.
			// Either shape is survivable — a struct decode against `0` would
			// fail the whole read and show the repository as broken.
			raw, err := s.captureValue()
			if err != nil {
				return err
			}
			var meta struct {
				BuiltAtCommit string `json:"built_at_commit"`
			}
			if json.Unmarshal(raw, &meta) == nil && g.BuiltCommit == "" {
				g.BuiltCommit = meta.BuiltAtCommit
			}
		default:
			return s.skipValue()
		}
		return nil
	})
}

// stampAt is where graph.json's top-level built_at_commit sits: start is the
// offset just past the key, end just past the value — the range Restamp
// splices — and cur its contents.
type stampAt struct {
	start, end int64
	cur        string
}

// tailStamp finds the top-level built_at_commit in the last tailBytes of a
// graph.json of the given size, without reading anything else.
//
// It accepts exactly one shape, graphify's own: the stamp is the last member
// of the root object, a plain string, followed by nothing but the root's
// closing brace. Anything else — a nested "graph" object last, an escape, a
// null, a file that does not end in `}` — reports false and the caller falls
// back to the full scan, so a tail answer is never a guess.
func tailStamp(f io.ReaderAt, size int64) (stampAt, bool) {
	if size <= 0 {
		return stampAt{}, false
	}
	n := int64(tailBytes)
	if size < n {
		n = size
	}
	var buf [tailBytes]byte
	b := buf[:n]
	if _, err := f.ReadAt(b, size-n); err != nil && err != io.EOF {
		return stampAt{}, false
	}
	base := size - n
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

	i := len(b) - 1
	for i >= 0 && isSpace(b[i]) {
		i--
	}
	if i < 0 || b[i] != '}' {
		return stampAt{}, false
	}
	i--
	for i >= 0 && isSpace(b[i]) {
		i--
	}
	if i < 0 || b[i] != '"' {
		return stampAt{}, false
	}
	valEnd := i + 1 // just past the closing quote
	i--
	for i >= 0 && b[i] != '"' {
		if b[i] == '\\' || b[i] < 0x20 {
			return stampAt{}, false
		}
		i--
	}
	if i < 0 || (i > 0 && b[i-1] == '\\') {
		return stampAt{}, false
	}
	cur := string(b[i+1 : valEnd-1])
	i--
	for i >= 0 && isSpace(b[i]) {
		i--
	}
	if i < 0 || b[i] != ':' {
		return stampAt{}, false
	}
	i--
	for i >= 0 && isSpace(b[i]) {
		i--
	}
	const key = `"built_at_commit"`
	if i+1 < len(key) || string(b[i+1-len(key):i+1]) != key {
		return stampAt{}, false
	}
	keyEnd := i + 1
	i -= len(key)
	for i >= 0 && isSpace(b[i]) {
		i--
	}
	if i < 0 || (b[i] != ',' && b[i] != '{') {
		return stampAt{}, false
	}
	return stampAt{start: base + int64(keyEnd), end: base + int64(valEnd), cur: cur}, true
}

// scanStamp is tailStamp's fallback: a full scan for the first top-level
// built_at_commit. It reads from r's current position, which must be the
// start of the file.
func scanStamp(r io.Reader) (stampAt, error) {
	s := getScanner(r)
	defer putScanner(s)
	var (
		at    stampAt
		found bool
	)
	errStop := errors.New("stop")
	err := s.members(func(key []byte, escaped bool) error {
		if escaped || string(key) != "built_at_commit" {
			return s.skipValue()
		}
		// The range starts just past the key's closing quote, which is where
		// the decoder's InputOffset stood: the splice rewrites the colon too.
		at.start = s.keyEnd
		v, err := s.commitValue()
		if err != nil {
			return err
		}
		at.end, at.cur, found = s.offset(), v, true
		return errStop
	})
	if found {
		return at, nil
	}
	if err != nil && err != errStop {
		return stampAt{}, err
	}
	return stampAt{}, ErrNoStamp
}

// ReadBuiltCommit returns graph.json's built_at_commit without counting
// anything: the tail read, and a full scan only when the tail does not hold
// graphify's usual layout and the file is within MaxGraphBytes. It is the
// cheap read for callers that want the commit and nothing else.
func ReadBuiltCommit(out string) (string, error) {
	f, err := os.Open(filepath.Join(out, "graph.json"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if at, ok := tailStamp(f, fi.Size()); ok {
		return at.cur, nil
	}
	if fi.Size() > MaxGraphBytes {
		return "", errors.New("graphstate: graph.json is larger than the parse ceiling and its stamp is not at the end")
	}
	var g Graph
	if err := scanGraph(f, &g); err != nil {
		return "", err
	}
	return g.BuiltCommit, nil
}
