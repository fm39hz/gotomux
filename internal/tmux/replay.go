package tmux

import (
	"github.com/fm39hz/gotomux/internal/toolclass"
	"strings"
)

// inferredReplayTool admits interactive workspace tools only. A foreground
// process is an observation, not a launch recipe; batch tools and language
// runtimes need an explicit pane start command to be replayed.
func inferredReplayTool(command string) bool {
	switch toolclass.Classify(command) {
	case toolclass.KindEditor, toolclass.KindFiles, toolclass.KindAgent:
		return true
	case toolclass.KindGit:
		return toolclass.IsPreferred(command)
	default:
		return false
	}
}

// Linux appends this marker to /proc executable links after an upgrade. It is
// observation metadata and must never become part of a replay executable path.
func replayExecutablePath(path string) string {
	return strings.TrimSuffix(path, " (deleted)")
}
