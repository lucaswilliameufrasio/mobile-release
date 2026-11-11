// Package fastlane runs the shared lanes embedded in the binary, so
// projects need no Fastfile, Gemfile or fastlane setup of their own.
package fastlane

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lucaswilliameufrasio/mobile-release/internal/runner"
)

//go:embed assets/Gemfile assets/Gemfile.lock assets/fastlane/Fastfile assets/fastlane/Pluginfile
var assets embed.FS

// Lanes runs embedded lanes for a project.
type Lanes struct {
	Runner runner.Runner
	// ProjectDir is the absolute project root passed to the lanes.
	ProjectDir string
	// CacheDir holds the materialized lanes and installed gems.
	CacheDir string
	dir      string
}

// DefaultCacheDir is the per-user cache for lanes and gems.
func DefaultCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mobile-release"), nil
}

// Digest identifies the embedded lanes.
func Digest() string {
	hash := sha256.New()
	_ = fs.WalkDir(assets, "assets", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := assets.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		hash.Write([]byte(path))
		hash.Write(data)
		return nil
	})
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

// Dir writes the lanes once per version and returns their directory.
func (l *Lanes) Dir() (string, error) {
	if l.dir != "" {
		return l.dir, nil
	}
	dir := filepath.Join(l.CacheDir, "lanes-"+Digest())
	err := fs.WalkDir(assets, "assets", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, strings.TrimPrefix(path, "assets"))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := assets.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return "", fmt.Errorf("write lanes: %w", err)
	}
	l.dir = dir
	return dir, nil
}

func (l *Lanes) bundleEnv(dir string) []string {
	return []string{
		"BUNDLE_GEMFILE=" + filepath.Join(dir, "Gemfile"),
		"BUNDLE_PATH=" + filepath.Join(l.CacheDir, "bundle"),
		"BUNDLE_FROZEN=true",
		"FASTLANE_SKIP_UPDATE_CHECK=1",
		"FASTLANE_HIDE_CHANGELOG=1",
		"FASTLANE_OPT_OUT_USAGE=1",
		"LC_ALL=en_US.UTF-8",
		"LANG=en_US.UTF-8",
		"MOBILE_RELEASE_PROJECT_DIR=" + l.ProjectDir,
	}
}

// Install installs the pinned gems (fastlane and plugins) into the cache.
func (l *Lanes) Install(ctx context.Context) error {
	dir, err := l.Dir()
	if err != nil {
		return err
	}
	marker := filepath.Join(dir, ".installed")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := l.Runner.Run(ctx, runner.Command{Name: "bundle", Args: []string{"install", "--quiet"}, Dir: dir, Env: l.bundleEnv(dir)}); err != nil {
		return fmt.Errorf("install fastlane: %w", err)
	}
	return os.WriteFile(marker, nil, 0o644)
}

// Run executes `fastlane <platform> <lane>` with env.
func (l *Lanes) Run(ctx context.Context, platform, lane string, env []string) error {
	if err := l.Install(ctx); err != nil {
		return err
	}
	dir, _ := l.Dir()
	return l.Runner.Run(ctx, runner.Command{
		Name: "bundle",
		Args: []string{"exec", "fastlane", platform, lane},
		Dir:  dir,
		Env:  append(l.bundleEnv(dir), env...),
	})
}

// Output runs a lane that writes its result to MOBILE_RELEASE_OUTPUT_FILE.
func (l *Lanes) Output(ctx context.Context, platform, lane string, env []string) (string, error) {
	file, err := os.CreateTemp("", "mobile-release-output-")
	if err != nil {
		return "", err
	}
	path := file.Name()
	file.Close()
	defer os.Remove(path)
	if err := l.Run(ctx, platform, lane, append(env, "MOBILE_RELEASE_OUTPUT_FILE="+path)); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
