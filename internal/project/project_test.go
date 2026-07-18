package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidSessionName(t *testing.T) {
	if ValidSessionName("") || ValidSessionName("a:b") || !ValidSessionName("foo-bar") {
		t.Fatal("validSessionName")
	}
}

func TestFindProjectRootNearest(t *testing.T) {
	dir := t.TempDir()
	// repo/
	//   go.mod
	//   pkg/x/
	repo := filepath.Join(dir, "repo")
	sub := filepath.Join(repo, "pkg", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := FindProjectRoot(sub)
	if got != repo {
		t.Fatalf("got %q want %q", got, repo)
	}
}

func TestFindProjectRootDotnet(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "MyApp")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "MyApp.csproj"), []byte("<Project/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := FindProjectRoot(app)
	if got != app {
		t.Fatalf("got %q want %q", got, app)
	}
}

func TestSessionNameGenericPrefix(t *testing.T) {
	// .../Foo/web → foo-web
	name := SessionName("/work/Foo/web")
	if name != "foo-web" {
		t.Fatalf("got %q", name)
	}
	// underscore → hyphen
	if n := SessionName("/work/my_app"); n != "my-app" {
		t.Fatalf("got %q", n)
	}
}

func TestProjectSessionSubdir(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "grimoire")
	sub := filepath.Join(repo, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	name, root := Session(sub)
	if root != repo {
		t.Fatalf("root %q want %q", root, repo)
	}
	if name != "grimoire" {
		t.Fatalf("name %q", name)
	}
}
