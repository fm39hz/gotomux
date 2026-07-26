package project

import (
	"os"
	"path/filepath"
	"sync"

	pd "github.com/richardwooding/projectdetect"
)

// Register gotomux extras once (godot / jj / nix / envrc).
// Built-ins already cover go, node, rust, python, dotnet, docker-compose, ...
var registerOnce sync.Once

func ensureProjectTypes() {
	registerOnce.Do(func() {
		// Godot game projects
		pd.Register(&pd.ProjectType{
			Name:        "godot",
			Description: "Godot project (project.godot)",
			Indicators:  []pd.Indicator{{HasFile: "project.godot"}},
		})
		// Jujutsu VCS root
		pd.Register(&pd.ProjectType{
			Name:        "jj",
			Description: "Jujutsu repository (.jj)",
			Indicators:  []pd.Indicator{{HasFile: ".jj"}},
		})
		// Nix flake / direnv
		pd.Register(&pd.ProjectType{
			Name:        "nix",
			Description: "Nix flake or shell",
			Indicators: []pd.Indicator{
				{HasFile: "flake.nix"},
				{HasFile: "shell.nix"},
			},
		})
		pd.Register(&pd.ProjectType{
			Name:        "direnv",
			Description: "direnv (.envrc)",
			Indicators:  []pd.Indicator{{HasFile: ".envrc"}},
		})
		// bare Makefile (common CLI / C projects without cmake)
		pd.Register(&pd.ProjectType{
			Name:        "make",
			Description: "Make-based project (Makefile)",
			Indicators: []pd.Indicator{
				{HasFile: "Makefile"},
				{HasFile: "makefile"},
			},
		})
		// monorepo workspace markers as soft project roots
		pd.Register(&pd.ProjectType{
			Name:        "js-workspace",
			Description: "JS monorepo workspace",
			Indicators: []pd.Indicator{
				{HasFile: "pnpm-workspace.yaml"},
				{HasFile: "turbo.json"},
				{HasFile: "nx.json"},
				{HasFile: "lerna.json"},
			},
			BuildExcludes: []string{"node_modules", ".turbo", ".next"},
		})
	})
}

// DetectTypes returns projectdetect matches for dir (may be empty).
func DetectTypes(dir string) []pd.Match {
	ensureProjectTypes()
	dir = filepath.Clean(dir)
	if dir == "" {
		return nil
	}
	// Detect lists basenames of dir only; DirFS must root at dir.
	return pd.Detect(os.DirFS(dir), ".")
}

// IsDetectedProject: any registered project type matches (not VCS-only).
// .git alone is NOT a projectdetect type - use IsGitRepo for that.
func IsDetectedProject(dir string) bool {
	return len(DetectTypes(dir)) > 0
}
