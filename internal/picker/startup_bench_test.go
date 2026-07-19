package picker

import (
	"os"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/template"
	"github.com/fm39hz/gotomux/internal/tmux"
)

func TestStartupBreakdown(t *testing.T) {
	if os.Getenv("STARTUP_BENCH") != "1" {
		t.Skip("STARTUP_BENCH=1")
	}
	t0 := time.Now()
	ctl, err := tmux.New()
	t.Logf("tmux.New: %v err=%v", time.Since(t0), err)

	t1 := time.Now()
	st, err := store.Open()
	t.Logf("store.Open: %v err=%v", time.Since(t1), err)
	if st != nil {
		defer st.Close()
	}

	t2 := time.Now()
	cwd, _ := os.Getwd()
	root := project.FindProjectRoot(cwd)
	name := project.SessionName(root)
	t.Logf("FindProjectRoot: %v root=%s", time.Since(t2), root)

	t3 := time.Now()
	lab := template.StickyLabel(st)
	t.Logf("StickyLabel#1 (sync+reconcile): %v label=%s", time.Since(t3), lab)

	t4 := time.Now()
	_ = template.StickyLabel(st)
	t.Logf("StickyLabel#2: %v", time.Since(t4))

	t5 := time.Now()
	if ctl != nil {
		live, _ := ctl.ListLive()
		t.Logf("ListLive: %v n=%d", time.Since(t5), len(live))
	}

	t6 := time.Now()
	if st != nil {
		meta, _ := st.ListMeta()
		us, _ := st.AllUsage()
		t.Logf("ListMeta+AllUsage: %v meta=%d usage=%d", time.Since(t6), len(meta), len(us))
	}

	t7 := time.Now()
	BindStore(st)
	_ = NewModel(ctl, st, name, root)
	t.Logf("NewModel: %v", time.Since(t7))

	t8 := time.Now()
	_ = project.DetectTypes(root)
	t.Logf("DetectTypes: %v", time.Since(t8))

	t9 := time.Now()
	for i := 0; i < 5; i++ {
		_ = project.IsGitRepo(root)
	}
	t.Logf("IsGitRepo x5: %v", time.Since(t9))

	t10 := time.Now()
	_ = project.Children(root)
	t.Logf("Children: %v", time.Since(t10))

	t.Logf("wall from start: %v", time.Since(t0))
}
