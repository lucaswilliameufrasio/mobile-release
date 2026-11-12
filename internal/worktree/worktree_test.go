package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeIsolatesPrepareChanges(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "init", "-q")
	project := filepath.Join(repo, "apps", "mobile")
	write(t, filepath.Join(project, "release.toml"), "schema_version = 2\n")
	write(t, filepath.Join(project, "icon.txt"), "production\n")
	write(t, filepath.Join(repo, ".gitignore"), ".env\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "init")
	write(t, filepath.Join(project, ".env"), "API=staging\n")
	write(t, filepath.Join(project, "uncommitted.txt"), "local only\n")

	ctx := context.Background()
	w, err := Create(ctx, project, []string{".env", "missing.env"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(w.Root, filepath.Join("apps", "mobile")) {
		t.Fatalf("root = %s", w.Root)
	}
	if data, _ := os.ReadFile(filepath.Join(w.Root, ".env")); string(data) != "API=staging\n" {
		t.Fatalf(".env not copied: %q", data)
	}
	if _, err := os.Stat(filepath.Join(w.Root, "uncommitted.txt")); err == nil {
		t.Fatal("uncommitted files must not leak into the worktree")
	}

	// A prepare command swaps the icon and the build writes artifacts.
	write(t, filepath.Join(w.Root, "icon.txt"), "staging\n")
	write(t, filepath.Join(w.Root, "dist", "staging", "android", "app.apk"), "apk")
	if err := w.CopyBack(project, filepath.Join("dist", "staging")); err != nil {
		t.Fatal(err)
	}
	if err := w.Remove(ctx); err != nil {
		t.Fatal(err)
	}

	if data, _ := os.ReadFile(filepath.Join(project, "icon.txt")); string(data) != "production\n" {
		t.Fatalf("the checkout was modified: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(project, "dist", "staging", "android", "app.apk")); string(data) != "apk" {
		t.Fatal("artifacts were not copied back")
	}
	if _, err := os.Stat(w.Dir); err == nil {
		t.Fatal("worktree directory should be removed")
	}
	command := exec.Command("git", "worktree", "list")
	command.Dir = repo
	output, _ := command.Output()
	if strings.Count(string(output), "\n") != 1 {
		t.Fatalf("worktree still registered:\n%s", output)
	}
}

func TestCreateOutsideGit(t *testing.T) {
	if _, err := Create(context.Background(), t.TempDir(), nil); err == nil {
		t.Fatal("expected an error outside a git repository")
	}
}
