package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// shells — not restored as pane cmd
var shellNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true,
	"nu": true, "nushell": true, "dash": true, "ash": true,
	"elvish": true, "xonsh": true, "pwsh": true, "powershell": true,
	"tmux": true, "login": true,
}

// known tools we prefer to restore (binary base name)
// empty map = restore any non-shell; keep set for docs / future filter
var restoreTools = map[string]bool{
	"nvim": true, "vim": true, "vi": true, "hx": true, "helix": true,
	"emacs": true, "nano": true, "micro": true,
	"lazygit": true, "gitui": true, "tig": true,
	"yazi": true, "lf": true, "ranger": true, "nnn": true, "broot": true,
	"btop": true, "htop": true, "top": true, "bottom": true, "nvtop": true,
	"claude": true, "opencode": true, "codex": true, "aider": true,
	"python": true, "python3": true, "node": true, "bun": true, "deno": true,
	"go": true, "cargo": true, "godot": true, "dotnet": true,
	"ssh": true, "mosh": true,
}

// detectPaneCmd: prefer pane_current_command if non-shell; else walk /proc children.
// Returns binary name only (e.g. "nvim") — load runs it in pane cwd.
func detectPaneCmd(currentCmd string, pid int32) string {
	if base := binBase(currentCmd); base != "" && !shellNames[base] {
		return base
	}
	if pid <= 0 {
		return ""
	}
	return walkProc(int(pid), 4)
}

func binBase(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	// "nvim" or "/usr/bin/nvim" or "-nu"
	cmd = strings.TrimPrefix(cmd, "-")
	return strings.ToLower(filepath.Base(strings.Fields(cmd)[0]))
}

func walkProc(rootPID, maxDepth int) string {
	type node struct{ pid, depth int }
	q := []node{{rootPID, 0}}
	seen := map[int]bool{rootPID: true}

	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		if n.depth > 0 { // skip the shell itself at depth 0 if it was shell
			if name := procComm(n.pid); name != "" && !shellNames[name] {
				// prefer known tools; still accept unknown non-shells
				if restoreTools[name] || !shellNames[name] {
					return name
				}
			}
		}
		if n.depth >= maxDepth {
			continue
		}
		for _, child := range procChildren(n.pid) {
			if seen[child] {
				continue
			}
			seen[child] = true
			q = append(q, node{child, n.depth + 1})
		}
	}
	// depth-0 non-shell already handled by caller; check children-only miss
	// second pass: any non-shell in tree including if current was weird
	q = []node{{rootPID, 0}}
	seen = map[int]bool{rootPID: true}
	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		name := procComm(n.pid)
		if name != "" && !shellNames[name] {
			return name
		}
		if n.depth >= maxDepth {
			continue
		}
		for _, child := range procChildren(n.pid) {
			if seen[child] {
				continue
			}
			seen[child] = true
			q = append(q, node{child, n.depth + 1})
		}
	}
	return ""
}

func procComm(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return ""
	}
	return strings.ToLower(string(bytes.TrimSpace(b)))
}

func procChildren(pid int) []int {
	// /proc/<pid>/task/<pid>/children is linux-specific; fallback: scan /proc
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/task/" + strconv.Itoa(pid) + "/children")
	if err == nil {
		fields := strings.Fields(string(b))
		out := make([]int, 0, len(fields))
		for _, f := range fields {
			if id, err := strconv.Atoi(f); err == nil {
				out = append(out, id)
			}
		}
		return out
	}
	// fallback: read /proc/*/stat ppid
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	want := strconv.Itoa(pid)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		id, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// stat: pid (comm) state ppid ...
		ppid := parseStatPPID(stat)
		if ppid == want {
			out = append(out, id)
		}
	}
	return out
}

func parseStatPPID(stat []byte) string {
	// find ") " then fields: state ppid
	i := bytes.LastIndex(stat, []byte(") "))
	if i < 0 {
		return ""
	}
	fields := bytes.Fields(stat[i+2:])
	if len(fields) < 2 {
		return ""
	}
	return string(fields[1])
}
