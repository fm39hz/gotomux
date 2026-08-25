package project

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type rootStatus struct {
	root  string
	found bool
}

var projectRootMemo sync.Map // cleaned start -> rootStatus

// FindProjectRoot walks up from start to nearest project-looking directory.
func FindProjectRoot(start string) string {
	return findRoot(start).root
}

// FindRootStatus is FindProjectRoot plus whether a marker-bearing directory was
// actually found. When the walk exhausts the hierarchy, root echoes start and
// found is false — a distinction the negative root cache needs: only exhaustion
// proves "no root anywhere", while start carrying its own marker (also root ==
// start) is an immediate hit whose answer must not be recorded as a miss.
func FindRootStatus(start string) (root string, found bool) {
	st := findRoot(start)
	return st.root, st.found
}

func findRoot(start string) rootStatus {
	start = filepath.Clean(start)
	if start == "" {
		if cwd, err := os.Getwd(); err == nil {
			start = cwd
		} else {
			return rootStatus{root: start}
		}
	}
	if v, ok := projectRootMemo.Load(start); ok {
		return v.(rootStatus)
	}
	path := start
	var chain []string
	st := rootStatus{root: start}
	for path != "/" && path != "." && path != "" {
		chain = append(chain, path)
		if isProjectRoot(path) {
			st.root = path
			st.found = true
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	for _, p := range chain {
		projectRootMemo.Store(p, st)
	}
	return st
}

func isProjectRoot(dir string) bool {
	// mature detector (go/node/rust/python/dotnet/...) + our extras
	if IsDetectedProject(dir) {
		return true
	}
	// VCS roots without language marker
	if IsGitRepo(dir) {
		return true
	}
	if DirExists(filepath.Join(dir, ".jj")) || DirExists(filepath.Join(dir, ".hg")) {
		return true
	}
	// thin fallback list (markers.go)
	for _, m := range projectMarkers {
		p := filepath.Join(dir, m)
		if FileExists(p) || DirExists(p) {
			return true
		}
	}
	if hasExtInDir(dir, ".sln") || hasExtInDir(dir, ".csproj") {
		return true
	}
	return false
}

func hasExtInDir(dir, ext string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	ext = strings.ToLower(ext)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ext) {
			return true
		}
	}
	return false
}

// Session returns (session name, project root) for path.
func Session(path string) (name, root string) {
	root = FindProjectRoot(path)
	name = SessionName(root)
	return name, root
}

// SessionName: tmux-safe name from project root basename.
func SessionName(root string) string {
	root = filepath.Clean(root)
	base := sanitizeSessionPart(filepath.Base(root))
	if base == "" {
		base = "session"
	}
	if genericBases[base] {
		parent := sanitizeSessionPart(filepath.Base(filepath.Dir(root)))
		if parent != "" && parent != base {
			return parent + "-" + base
		}
	}
	return base
}

func sanitizeSessionPart(s string) string {
	s = strings.TrimPrefix(s, ".")
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r == ' ' || r == '.' || r == '_':
			return '-'
		default:
			return -1
		}
	}, s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// ValidSessionName: tmux targets use "sess:win" - colon/control break them.
func ValidSessionName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch r {
		case ':', '\n', '\r', '\t':
			return false
		}
	}
	return true
}

func FileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func DirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
