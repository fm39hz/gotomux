package main

import (
	"os"
	"os/exec"
	"testing"
)

func BenchmarkReadyNoZoxide(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ctl, err := newTmuxCtl()
		if err != nil {
			b.Fatal(err)
		}
		store, err := openStore()
		if err != nil {
			b.Fatal(err)
		}
		cwd, _ := os.Getwd()
		root := findProjectRoot(cwd)
		name := sessionName(root)
		create := item{kind: kindCreate, title: name, name: name, path: root, desc: root}
		_ = collectBase(ctl, store, create)
		store.Close()
	}
}

func BenchmarkReadyWithZoxide(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ctl, err := newTmuxCtl()
		if err != nil {
			b.Fatal(err)
		}
		store, err := openStore()
		if err != nil {
			b.Fatal(err)
		}
		cwd, _ := os.Getwd()
		root := findProjectRoot(cwd)
		name := sessionName(root)
		create := item{kind: kindCreate, title: name, name: name, path: root, desc: root}
		base := collectBase(ctl, store, create)
		n, p := occupancy(base)
		_ = zoxideItems(zoxideList(), n, p)
		store.Close()
	}
}

func BenchmarkOpenStore(b *testing.B) {
	for i := 0; i < b.N; i++ {
		store, err := openStore()
		if err != nil {
			b.Fatal(err)
		}
		store.Close()
	}
}

func BenchmarkListLive(b *testing.B) {
	ctl, err := newTmuxCtl()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ctl.ListLive(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHasSession(b *testing.B) {
	ctl, err := newTmuxCtl()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ctl.Has("tmuxproject")
	}
}

func BenchmarkConnectExisting(b *testing.B) {
	if os.Getenv("TMUX") == "" {
		b.Skip("need TMUX for switch")
	}
	ctl, err := newTmuxCtl()
	if err != nil {
		b.Fatal(err)
	}
	_ = ctl.Connect("tmuxproject", "")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := ctl.Connect("tmuxproject", ""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoadPresetDetached(b *testing.B) {
	ctl, err := newTmuxCtl()
	if err != nil {
		b.Fatal(err)
	}
	store, err := openStore()
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	names, err := store.ListNames()
	if err != nil || len(names) == 0 {
		b.Skip("no presets")
	}
	src, err := store.Get(names[0])
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		p := *src
		p.Name = "tp-bench-load"
		_ = ctl.Kill(p.Name)
		b.StartTimer()
		if err := ctl.Load(&p); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		_ = ctl.Kill(p.Name)
	}
}

func BenchmarkSeshConnectExisting(b *testing.B) {
	if os.Getenv("TMUX") == "" {
		b.Skip("need TMUX")
	}
	// warm
	_ = exec.Command("sesh", "connect", "tmuxproject").Run()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := exec.Command("sesh", "connect", "tmuxproject").Run(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBashDataCollect(b *testing.B) {
	script := `
get_data() {
  echo "[Create: x]"
  tmux ls -F "[Active] #{session_name}: #{session_windows} windows" 2>/dev/null
  [ -d "$HOME/.config/tmuxp" ] && fd -e json . "$HOME/.config/tmuxp" --exec-batch basename -s .json 2>/dev/null | sed 's/^/[Preset] /'
  zoxide query -l | sed 's/^/[Zoxide] /'
}
get_data | awk 'BEGIN{} {print}' >/dev/null
`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cmd := exec.Command("bash", "-c", script)
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("%v %s", err, out)
		}
	}
}

func BenchmarkTmuxpLoadDetached(b *testing.B) {
	// pick first tmuxp json if any
	dir := os.Getenv("HOME") + "/.config/tmuxp"
	ents, err := os.ReadDir(dir)
	if err != nil {
		b.Skip(err.Error())
	}
	var file string
	for _, e := range ents {
		if !e.IsDir() && len(e.Name()) > 5 && e.Name()[len(e.Name())-5:] == ".json" {
			file = dir + "/" + e.Name()
			break
		}
	}
	if file == "" {
		b.Skip("no tmuxp json")
	}
	name := "tp-bench-tmuxp"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
		b.StartTimer()
		// tmuxp load uses config name; use file path
		cmd := exec.Command("tmuxp", "load", "-d", "-y", file)
		// force session name? tmuxp uses name inside json — kill whatever
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("%v %s", err, out)
		}
		b.StopTimer()
		// kill all sessions that might have been created - aggressive cleanup via list
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
		// also try basename
		base := file[len(dir)+1:]
		base = base[:len(base)-5]
		_ = exec.Command("tmux", "kill-session", "-t", base).Run()
	}
}
