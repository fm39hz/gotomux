package tmux

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fm39hz/gotomux/internal/toolclass"
	"github.com/shirou/gopsutil/v4/process"
)

// procIndex: process snapshot for a whole Freeze (gopsutil - portable).
type procIndex struct {
	children map[int32][]int32
	comm     map[int32]string
	exe      map[int32]string // absolute executable, "" when unreadable
}

var (
	procIdxCache    *procIndex
	procIdxCachedAt time.Time
	procIdxMu       sync.Mutex
)

// loadProcIndex: one process table snapshot via gopsutil (no ps/fork).
// Cached for 2 seconds — pane commands rarely change mid-freeze.
func loadProcIndex() *procIndex {
	procIdxMu.Lock()
	defer procIdxMu.Unlock()

	if procIdxCache != nil && time.Since(procIdxCachedAt) < 2*time.Second {
		return procIdxCache
	}
	idx := &procIndex{
		children: map[int32][]int32{},
		comm:     map[int32]string{},
		exe:      map[int32]string{},
	}
	procs, err := process.Processes()
	if err != nil {
		procIdxCache = idx
		procIdxCachedAt = time.Now()
		return idx
	}
	for _, p := range procs {
		pid := p.Pid
		ppid, err := p.Ppid()
		if err != nil {
			continue
		}
		name, err := p.Name()
		if err != nil || name == "" {
			if exe, e2 := p.Exe(); e2 == nil && exe != "" {
				name = filepath.Base(exe)
			}
		}
		name = strings.ToLower(filepath.Base(name))
		idx.comm[pid] = name
		if exe, e2 := p.Exe(); e2 == nil && exe != "" {
			idx.exe[pid] = exe
		}
		idx.children[ppid] = append(idx.children[ppid], pid)
	}
	procIdxCache = idx
	procIdxCachedAt = time.Now()
	return idx
}

// detectPaneCmd: non-shell current -> non-shell start -> tool in process tree.
// Returns binary base name only (e.g. "nvim").
func detectPaneCmd(currentCmd, startCmd string, pid int32, procs *procIndex) string {
	if base := toolclass.Intent(currentCmd); base != "" {
		return base
	}
	if base := toolclass.Intent(startCmd); base != "" {
		return base
	}
	// raw base even if not in preferred list (non-shell)
	if base := toolclass.Base(currentCmd); base != "" && !toolclass.IsShell(base) {
		return base
	}
	if base := toolclass.Base(startCmd); base != "" && !toolclass.IsShell(base) {
		return base
	}
	if pid <= 0 || procs == nil {
		return ""
	}
	return procs.findTool(pid, 4)
}

// resolvePaneCmdPath: absolute executable behind the detected tool name, for
// replay in environments where the bare name may not resolve. Searches the
// pane leader first, then the process tree — the same order findTool uses.
// Returns "" when nothing matches or no snapshot is available.
func resolvePaneCmdPath(name string, panePid int32, procs *procIndex) string {
	if name == "" || panePid <= 0 || procs == nil {
		return ""
	}
	want := strings.ToLower(name)
	type node struct {
		pid   int32
		depth int
	}
	q := []node{{panePid, 0}}
	seen := map[int32]bool{panePid: true}
	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		if procs.comm[n.pid] == want {
			if exe := procs.exe[n.pid]; exe != "" {
				return exe
			}
		}
		if n.depth >= 4 {
			continue
		}
		for _, child := range procs.children[n.pid] {
			if seen[child] {
				continue
			}
			seen[child] = true
			q = append(q, node{child, n.depth + 1})
		}
	}
	return ""
}

func binBase(cmd string) string {
	return toolclass.Base(cmd)
}

// findTool: BFS children; prefer toolclass preferred set, else first non-shell.
func (p *procIndex) findTool(rootPID int32, maxDepth int) string {
	type node struct {
		pid   int32
		depth int
	}
	q := []node{{rootPID, 0}}
	seen := map[int32]bool{rootPID: true}
	var fallback string

	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		if n.depth > 0 {
			name := p.comm[n.pid]
			if name != "" {
				if toolclass.IsPreferred(name) {
					return name
				}
				if fallback == "" && !toolclass.IsShell(name) {
					fallback = name
				}
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
