// Package gfy owns every assumption ggraphify makes about the graphify CLI.
//
// The GUI never re-implements graphify and never imports it: graphify is a
// Python console script and this is a Go process, and the coupling between
// them is a pinned command-line contract. One package holding all of it means
// that when graphify moves — and at 0.9.58 it is clearly still moving — there
// is exactly one place to look.
//
// Two rules are load-bearing:
//
//   - Only `--json` outputs are parsed. Everything else is opaque log text
//     for display. Scraping human-readable output would make this GUI break
//     on a release that reworded a line.
//   - Every argv is shown to the user before it runs. That is both the trust
//     story ("this is the money-spending command") and the debugging story
//     (copy it, paste it in a terminal, get the identical result).
package gfy

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BuiltAgainst is the graphify version this package's argv builders were
// written against. A materially different version found at run time raises a
// banner rather than failing mysteriously later.
const BuiltAgainst = "0.9.58"

// Backends is what `graphify extract --backend` accepts, verbatim from
// `graphify --help` at 0.9.58. The GUI offers the list and graphify validates
// the choice; an empty backend means "auto-detect", which is graphify's own
// default and the board's too — see EffectiveBackend for what that resolves
// to, because auto-detection is key-based and so cannot find claude-cli.
//
// "claude" is the Anthropic API with an ANTHROPIC_API_KEY; "claude-cli" is the
// Claude Code CLI already installed and logged in on this machine, which needs
// no key of its own.
var Backends = []string{"", "gemini", "kimi", "claude", ClaudeCLIBackend, "openai", "deepseek", "ollama"}

// Platforms is what `graphify install --platform P` accepts. The Integrations
// view offers install/uninstall per platform.
var Platforms = []string{
	"claude", "codex", "cursor", "opencode", "gemini", "copilot", "vscode",
	"aider", "amp", "agents", "claw", "droid", "trae", "trae-cn",
	"antigravity", "hermes", "kiro", "pi", "devin", "codebuddy", "kilo", "windows",
}

// Bin resolves the graphify executable: $GRAPHIFY_BIN, then PATH, then the
// uv tool install location. Returning the path rather than relying on PATH at
// exec time matters because the board may be launched from a .desktop entry,
// whose PATH is the session's and not the shell's.
func Bin() string {
	if v := strings.TrimSpace(os.Getenv("GRAPHIFY_BIN")); v != "" {
		return v
	}
	if p, err := exec.LookPath("graphify"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, c := range []string{
			filepath.Join(home, ".local", "bin", "graphify"),
			filepath.Join(home, ".local", "share", "uv", "tools", "graphifyy", "bin", "graphify"),
		} {
			if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
				return c
			}
		}
	}
	return "graphify" // let exec fail with a clear message
}

// Version is what a version probe found.
type Version struct {
	Bin    string
	Raw    string // the whole line, e.g. "graphify 0.9.58"
	Number string // "0.9.58"
	Found  bool
	Err    string
	// SkillSkew is the platform-skill warning graphify prints on every
	// invocation when an installed agent skill is older than the package. It
	// is a real condition on a machine that has upgraded graphify without
	// re-running `graphify install`, and the board surfaces it as a one-click
	// fix rather than as noise in every job log.
	SkillSkew string
}

// Supported reports whether the found version is the one the argv builders
// were written against, at major.minor granularity. A patch bump is assumed
// compatible; anything else earns the banner.
func (v Version) Supported() bool {
	if !v.Found {
		return false
	}
	return majorMinor(v.Number) == majorMinor(BuiltAgainst)
}

func majorMinor(s string) string {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return s
	}
	return parts[0] + "." + parts[1]
}

// Probe runs `graphify version` once and caches the answer for the process's
// lifetime. It is called at startup and from the settings dialog's re-probe
// button; nothing else should shell out to discover the version.
//
// The cache is mutex-guarded rather than a sync.Once because Reprobe has to be
// able to invalidate it, and resetting a Once that another goroutine may be
// inside is a data race.
func Probe(ctx context.Context) Version {
	probeMu.Lock()
	defer probeMu.Unlock()
	if probeDone {
		return probed
	}
	probed, probeDone = probe(ctx), true
	return probed
}

// Reprobe discards the cached answer and probes again.
func Reprobe(ctx context.Context) Version {
	probeMu.Lock()
	probeDone = false
	probeMu.Unlock()
	return Probe(ctx)
}

var (
	probeMu   sync.Mutex
	probeDone bool
	probed    Version
)

func probe(ctx context.Context) Version {
	bin := Bin()
	v := Version{Bin: bin}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.Env = append(os.Environ(), "GRAPHIFY_NO_TIPS=1")
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		v.Err = err.Error()
		return v
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if skew := SkillSkew(line); skew != "" {
			v.SkillSkew = skew
			continue
		}
		if n := versionNumber(line); n != "" {
			v.Raw, v.Number, v.Found = line, n, true
		}
	}
	if !v.Found && v.Err == "" {
		v.Err = "could not parse a version out of `graphify version`"
	}
	return v
}

// versionNumber pulls a dotted number out of a line. It is deliberately loose:
// the only contract being relied on is that `graphify version` prints one.
func versionNumber(line string) string {
	for _, f := range strings.Fields(line) {
		f = strings.TrimPrefix(f, "v")
		if strings.Count(f, ".") < 1 {
			continue
		}
		ok := true
		for _, part := range strings.Split(f, ".") {
			if part == "" {
				ok = false
				break
			}
			if _, err := strconv.Atoi(strings.TrimRight(part, "abrc0123456789-")); err != nil {
				if _, err := strconv.Atoi(part); err != nil {
					ok = false
					break
				}
			}
		}
		if ok {
			return f
		}
	}
	return ""
}

// SkillSkew returns the human-readable skew message when line is graphify's
// platform-skill warning, and "" otherwise.
//
// The warning goes to stderr ahead of real output on every single invocation,
// so it also has to be stripped before parsing a --json response — a naive
// json.Unmarshal of the combined stream would fail on it.
func SkillSkew(line string) string {
	l := strings.TrimSpace(line)
	if !strings.HasPrefix(strings.ToLower(l), "warning:") {
		return ""
	}
	if !strings.Contains(l, "skill at") || !strings.Contains(l, "is from graphify") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(l, "warning:"))
}

// TrimNoise strips graphify's leading warnings from a stream that is about to
// be parsed as JSON. It removes lines before the first '{' or '[' only, so a
// brace inside the payload is never touched.
func TrimNoise(s string) string {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{', '[':
			return s[i:]
		case ' ', '\t', '\r', '\n':
		default:
			// Skip to the end of this line and keep looking.
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return ""
			}
			i += j
		}
	}
	return ""
}

// FirstErrorLine picks the line worth putting on a failed row's badge. It
// prefers a Python exception's final line, which is where the actual message
// is, and skips the skew warning and traceback scaffolding.
func FirstErrorLine(log string) string {
	sc := bufio.NewScanner(strings.NewReader(log))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	best := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || SkillSkew(line) != "" {
			continue
		}
		if strings.HasPrefix(line, "Traceback") || strings.HasPrefix(line, "File \"") || strings.HasPrefix(line, "  ") {
			continue
		}
		low := strings.ToLower(line)
		if strings.Contains(low, "error") || strings.Contains(low, "failed") ||
			strings.Contains(low, "exception") || strings.Contains(low, "traceback") {
			best = line
		} else if best == "" {
			best = line
		}
	}
	return best
}
