package tmux

import (
	"os/exec"
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

// known tools we prefer when walking the pane process tree
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

// procIndex: one ps snapshot for a whole Freeze (portable: Linux/macOS/BSD).
type procIndex struct {
	children map[int][]int
	comm     map[int]string
}

// loadProcIndex runs a single `ps` — no /proc dependency.
func loadProcIndex() *procIndex {
	idx := &procIndex{
		children: map[int][]int{},
		comm:     map[int]string{},
	}
	// -axo: BSD/macOS + Linux procps; -eo: POSIX-ish fallback
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,comm=").Output()
	if err != nil {
		out, err = exec.Command("ps", "-eo", "pid=", "ppid=", "comm=").Output()
		if err != nil {
			return idx
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		// comm may contain spaces on some ps — join rest
		name := strings.ToLower(filepath.Base(strings.Join(fields[2:], " ")))
		// strip macOS "-zsh" style
		name = strings.TrimPrefix(name, "-")
		idx.comm[pid] = name
		idx.children[ppid] = append(idx.children[ppid], pid)
	}
	return idx
}

// detectPaneCmd: non-shell current → non-shell start → tool in process tree.
// Returns binary base name only (e.g. "nvim").
func detectPaneCmd(currentCmd, startCmd string, pid int32, procs *procIndex) string {
	if base := binBase(currentCmd); base != "" && !shellNames[base] {
		return base
	}
	if base := binBase(startCmd); base != "" && !shellNames[base] {
		return base
	}
	if pid <= 0 || procs == nil {
		return ""
	}
	return procs.findTool(int(pid), 4)
}

func binBase(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	cmd = strings.TrimPrefix(cmd, "-")
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(filepath.Base(fields[0]))
}

// findTool: BFS children; prefer restoreTools, else first non-shell.
func (p *procIndex) findTool(rootPID, maxDepth int) string {
	type node struct{ pid, depth int }
	q := []node{{rootPID, 0}}
	seen := map[int]bool{rootPID: true}
	var fallback string

	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		if n.depth > 0 {
			name := p.comm[n.pid]
			if name == "" {
				// continue walk
			} else if restoreTools[name] {
				return name
			} else if fallback == "" && !shellNames[name] {
				fallback = name
			}
		}
		if n.depth >= maxDepth {
			continue
		}
		for _, child := range p.children[n.pid] {
			if seen[child] {
				continue
			}
			seen[child] = true
			q = append(q, node{child, n.depth + 1})
		}
	}
	return fallback
}
