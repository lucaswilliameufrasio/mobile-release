// Package worktree builds a release from a temporary git worktree of the
// current commit, so [prepare] commands (for example per-environment app
// icons) can change files without touching the developer's checkout.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is a detached checkout of HEAD.
type Worktree struct {
	// Source is the repository the worktree was created from.
	Source string
	// Dir is the worktree root (same relative layout as Source).
	Dir string
	// Root is where release.toml lives inside Dir (projects may sit in a
	// subdirectory of the repository).
	Root string
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// Create adds a detached worktree of HEAD for the project at projectRoot
// and copies the given untracked files (paths relative to projectRoot) into
// it. Call Remove when done.
func Create(ctx context.Context, projectRoot string, files []string) (*Worktree, error) {
	top, err := git(ctx, projectRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, errors.New("worktree builds need a git repository")
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil {
		return nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(top, resolvedRoot)
	if err != nil {
		return nil, err
	}
	parent, err := os.MkdirTemp("", "mobile-release-worktree-")
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(parent, "checkout")
	if _, err := git(ctx, top, "worktree", "add", "--detach", dir, "HEAD"); err != nil {
		os.RemoveAll(parent)
		return nil, err
	}
	w := &Worktree{Source: top, Dir: dir, Root: filepath.Join(dir, relative)}
	for _, file := range files {
		if file == "" {
			continue
		}
		source := filepath.Join(resolvedRoot, file)
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := copyFile(source, filepath.Join(w.Root, file)); err != nil {
			w.Remove(context.Background())
			return nil, fmt.Errorf("copy %s into the worktree: %w", file, err)
		}
	}
	return w, nil
}

// Remove deletes the worktree and its temporary directory.
func (w *Worktree) Remove(ctx context.Context) error {
	if w == nil {
		return nil
	}
	_, err := git(ctx, w.Source, "worktree", "remove", "--force", w.Dir)
	if removeErr := os.RemoveAll(filepath.Dir(w.Dir)); err == nil {
		err = removeErr
	}
	return err
}

// CopyBack copies a directory (relative to the project root) from the
// worktree to the original project, replacing files with the same name.
func (w *Worktree) CopyBack(projectRoot, relative string) error {
	source := filepath.Join(w.Root, relative)
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		inside, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		return copyFile(path, filepath.Join(projectRoot, relative, inside))
	})
}

func copyFile(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
