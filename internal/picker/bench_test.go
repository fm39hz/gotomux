package picker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func BenchmarkEnrichAllCold(b *testing.B) {
	dir := b.TempDir()
	for i := range 20 {
		repo := filepath.Join(dir, fmt.Sprintf("repo-%d", i))
		if err := os.MkdirAll(repo, 0755); err != nil {
			b.Fatal(err)
		}
		cmds := [][]string{
			{"git", "-C", repo, "init", "-b", "main"},
			{"git", "-C", repo, "commit", "--allow-empty", "-m", "root"},
			{"git", "-C", repo, "tag", fmt.Sprintf("v0.%d.0", i)},
			{"git", "-C", repo, "checkout", "-b", "develop"},
			{"git", "-C", repo, "commit", "--allow-empty", "-m", "wip"},
		}
		for _, args := range cmds {
			if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
				b.Fatalf("%s: %s: %s", args, err, out)
			}
		}
	}
	bySrc := map[string][]Item{SrcZoxide: {}}
	for i := range 20 {
		repo := filepath.Join(dir, fmt.Sprintf("repo-%d", i))
		bySrc[SrcZoxide] = append(bySrc[SrcZoxide], Item{Path: repo})
	}

	b.ResetTimer()
	b.Run("parallel", func(b *testing.B) {
		gitBranchCache = sync.Map{}
		for range b.N {
			enrichAllSync(bySrc)
		}
	})
	b.Run("sequential", func(b *testing.B) {
		gitBranchCache = sync.Map{}
		for range b.N {
			for _, it := range bySrc[SrcZoxide] {
				_ = readGitBranch(it.Path)
			}
		}
	})
}

func BenchmarkEnrichAllWarm(b *testing.B) {
	dir := b.TempDir()
	for i := range 20 {
		repo := filepath.Join(dir, fmt.Sprintf("repo-%d", i))
		if err := os.MkdirAll(repo, 0755); err != nil {
			b.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", repo, "init", "-b", "main").CombinedOutput(); err != nil {
			b.Fatalf("git init: %s: %s", err, out)
		}
	}
	bySrc := map[string][]Item{SrcZoxide: {}}
	for i := range 20 {
		repo := filepath.Join(dir, fmt.Sprintf("repo-%d", i))
		bySrc[SrcZoxide] = append(bySrc[SrcZoxide], Item{Path: repo})
	}
	enrichAllSync(bySrc)

	b.ResetTimer()
	b.Run("setGitBranch", func(b *testing.B) {
		for range b.N {
			view := make([]Item, len(bySrc[SrcZoxide]))
			copy(view, bySrc[SrcZoxide])
			for i := range view {
				setGitBranch(&view[i])
			}
		}
	})
}
