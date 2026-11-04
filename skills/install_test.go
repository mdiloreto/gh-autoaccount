package skills

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestInstallAndExisting(t *testing.T) {
	if checkInstallPlatform() != nil {
		t.Skip("installation unsupported")
	}
	dir := filepath.Join(t.TempDir(), "missing", "skills")
	path, installed, err := install(dir, "content\n")
	if err != nil || !installed || path != filepath.Join(dir, "gh-autoaccount", "SKILL.md") {
		t.Fatalf("install: %q %v %v", path, installed, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, installed, err = install(dir, "content\n")
	if err != nil || installed {
		t.Fatalf("repeat: %v %v", installed, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("no-op changed file metadata")
	}
	if err := os.WriteFile(path, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = install(dir, "content\n")
	if err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("modified: %v", err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "modified" {
		t.Fatal("modified file overwritten")
	}
}

func TestInstallRejectsSpecialTargets(t *testing.T) {
	if checkInstallPlatform() != nil {
		t.Skip("installation unsupported")
	}
	for _, kind := range []string{"symlink", "dangling", "directory", "managed-symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			managed := filepath.Join(dir, "gh-autoaccount")
			if kind == "managed-symlink" {
				if err := os.Symlink(t.TempDir(), managed); err != nil {
					t.Skip(err)
				}
			} else {
				if err := os.Mkdir(managed, 0755); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(managed, "SKILL.md")
				switch kind {
				case "directory":
					if err := os.Mkdir(path, 0755); err != nil {
						t.Fatal(err)
					}
				default:
					target := filepath.Join(dir, "target")
					if kind == "symlink" {
						if err := os.WriteFile(target, []byte("content"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Symlink(target, path); err != nil {
						t.Skip(err)
					}
				}
			}
			if _, _, err := install(dir, "content"); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestInstallParentSymlinkAndConcurrent(t *testing.T) {
	if checkInstallPlatform() != nil {
		t.Skip("installation unsupported")
	}
	dir := t.TempDir()
	actual := filepath.Join(dir, "actual")
	if err := os.Mkdir(actual, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(actual, link); err != nil {
		t.Skip(err)
	}
	const count = 16
	var wg sync.WaitGroup
	errs := make(chan error, count)
	installed := make(chan bool, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, ok, err := install(link, "same content"); errs <- err; installed <- ok }()
	}
	wg.Wait()
	close(errs)
	close(installed)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	n := 0
	for ok := range installed {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Errorf("installed count %d", n)
	}
	entries, err := os.ReadDir(filepath.Join(actual, "gh-autoaccount"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestUnsupportedInstallDoesNotCreateParent(t *testing.T) {
	if checkInstallPlatform() == nil {
		t.Skip("installation supported")
	}
	dir := filepath.Join(t.TempDir(), "missing", "skills")
	_, installed, err := install(dir, "content")
	if err == nil || !strings.Contains(err.Error(), "supported only on Linux and macOS") || installed {
		t.Fatalf("unsupported: %v %v", installed, err)
	}
	if _, err := os.Stat(filepath.Dir(dir)); !os.IsNotExist(err) {
		t.Fatalf("created parent: %v", err)
	}
}

func TestInstallInvalidParent(t *testing.T) {
	if checkInstallPlatform() != nil {
		t.Skip("installation unsupported")
	}
	if _, _, err := install(" \t", "content"); err == nil {
		t.Fatal("blank parent accepted")
	}
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := install(filepath.Join(parent, "skills"), "content"); err == nil {
		t.Fatal("non-directory parent accepted")
	}
	b, err := os.ReadFile(parent)
	if err != nil || string(b) != "keep" {
		t.Fatalf("parent changed: %q %v", b, err)
	}
}

func TestInstallConcurrentDifferentContent(t *testing.T) {
	if checkInstallPlatform() != nil {
		t.Skip("installation unsupported")
	}
	dir := t.TempDir()
	type result struct {
		content   string
		installed bool
		err       error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, content := range []string{"first", "second"} {
		go func(content string) {
			<-start
			_, installed, err := install(dir, content)
			results <- result{content, installed, err}
		}(content)
	}
	close(start)
	a, b := <-results, <-results
	if !a.installed {
		a, b = b, a
	}
	if !a.installed || a.err != nil || b.installed || b.err == nil || !strings.Contains(b.err.Error(), "differs") {
		t.Fatalf("results: %+v %+v", a, b)
	}
	managed := filepath.Join(dir, "gh-autoaccount")
	data, err := os.ReadFile(filepath.Join(managed, "SKILL.md"))
	if err != nil || string(data) != a.content {
		t.Fatalf("winner: %q %v", data, err)
	}
	entries, err := os.ReadDir(managed)
	if err != nil || len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		t.Fatalf("leftovers: %v %v", entries, err)
	}
}
