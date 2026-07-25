// Package toolclass is the single vocabulary for pane tools, chrome roles, and TUI icons.
// Hardcoded on purpose: this is product policy, not an external protocol.
package toolclass

import (
	"path/filepath"
	"strings"
)

// Kind is a portable role class for a tool binary.
type Kind int

const (
	KindUnknown Kind = iota
	KindShell
	KindEditor
	KindFiles
	KindGit
	KindAgent
	KindOther
)

// shells: not restored as pane cmd / not tool intent
var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true,
	"nu": true, "nushell": true, "dash": true, "ash": true,
	"elvish": true, "xonsh": true, "pwsh": true, "powershell": true,
	"tmux": true, "login": true,
	"gotomux": true, // self — never persist the picker as pane cmd
}

// preferred: binaries we keep when walking process trees
var preferred = map[string]bool{
	"nvim": true, "vim": true, "vi": true, "hx": true, "helix": true,
	"emacs": true, "nano": true, "micro": true,
	"lazygit": true, "gitui": true, "tig": true,
	"yazi": true, "lf": true, "ranger": true, "nnn": true, "broot": true,
	"btop": true, "htop": true, "top": true, "bottom": true, "nvtop": true,
	"claude": true, "opencode": true, "codex": true, "aider": true, "pi": true,
	"python": true, "python3": true, "node": true, "bun": true, "deno": true,
	"go": true, "cargo": true, "godot": true, "dotnet": true,
	"ssh": true, "mosh": true,
}

// Classify maps a binary base name to Kind.
func Classify(bin string) Kind {
	bin = Base(bin)
	if bin == "" {
		return KindUnknown
	}
	if shells[bin] {
		return KindShell
	}
	switch bin {
	case "nvim", "vim", "vi", "hx", "helix", "emacs", "nano", "micro", "editor":
		return KindEditor
	case "yazi", "lf", "ranger", "nnn", "broot", "files", "file":
		return KindFiles
	case "lazygit", "gitui", "tig", "git":
		return KindGit
	case "opencode", "claude", "codex", "aider", "pi", "agent":
		return KindAgent
	default:
		if preferred[bin] {
			return KindOther
		}
		return KindOther
	}
}

// IsShell reports shell/login wrappers.
func IsShell(bin string) bool {
	return Classify(bin) == KindShell || shells[Base(bin)]
}

// IsPreferred is used when walking process trees (prefer known tools).
func IsPreferred(bin string) bool {
	return preferred[Base(bin)]
}

// Intent: empty if shell/unknown empty; else basename tool.
func Intent(cmd string) string {
	base := Base(cmd)
	if base == "" || IsShell(base) {
		return ""
	}
	return base
}

// ChromeRole: tab title role from tool (portable).
// Agents keep their binary name for multi-agent glance.
func ChromeRole(tool string) string {
	tool = Base(tool)
	if tool == "" {
		return "shell"
	}
	switch Classify(tool) {
	case KindEditor:
		return "editor"
	case KindFiles:
		return "files"
	case KindGit:
		return "git"
	case KindAgent:
		return tool
	case KindShell:
		return "shell"
	default:
		if len(tool) <= 16 {
			return tool
		}
		return "shell"
	}
}

// NerdIcon returns a nerd-font glyph for a tool or role token; empty if unknown.
// Callers decide ASCII fallback.
func NerdIcon(tok string) string {
	tok = Base(tok)
	if tok == "" {
		return ""
	}
	// role aliases
	switch tok {
	case "editor":
		return "" // nf-dev-vim
	case "files", "file":
		return "" // folder
	case "shell", "sh", "term", "terminal":
		return "" // terminal
	case "agent":
		return "" // robot
	case "git":
		return "" // git branch
	}
	switch Classify(tok) {
	case KindEditor:
		return ""
	case KindFiles:
		return ""
	case KindGit:
		return ""
	case KindAgent:
		return ""
	case KindShell:
		return ""
	default:
		return ""
	}
}

// Base: first field basename, lower, strip leading '-'.
func Base(cmd string) string {
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
