// Package adopt plans and applies the files that make a project use
// mobile-release: a managed Makefile block that pins the CLI version, a
// release workflow and .gitignore entries. Nothing is overwritten: existing
// files are only appended to, and conflicts are reported and skipped.
package adopt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

// Module is the import path used to install the CLI.
const Module = "github.com/lucaswilliameufrasio/mobile-release/v2/cmd/mobile-release"

// Repository hosts the reusable workflow.
const Repository = "lucaswilliameufrasio/mobile-release"

// Markers delimit the Makefile block owned by mobile-release.
const (
	BeginMarker = "# >>> mobile-release (managed: `mobile-release upgrade` updates this block)"
	EndMarker   = "# <<< mobile-release"
)

// Action describes what happens to a file.
type Action string

const (
	Create Action = "create"
	Append Action = "append"
	Update Action = "update"
	Skip   Action = "skip"
)

// Change is one planned file change.
type Change struct {
	Path   string
	Action Action
	// Reason explains skips and updates.
	Reason string
	// Content is the full new file (create/update) or the appended text.
	Content string
}

// Options configures the plan.
type Options struct {
	Root    string
	Config  config.Config
	Version string
	// Workflow adds .github/workflows/release.yml.
	Workflow bool
	// WorkflowRef pins the reusable workflow: a commit SHA, or the tag when
	// the SHA could not be resolved.
	WorkflowRef string
}

var targets = []string{"release-install", "release-setup", "release-doctor", "deploy", "deploy-dry-run", "release-qa"}

// MakefileBlock is the managed block for version.
func MakefileBlock(version string) string {
	return BeginMarker + `
MOBILE_RELEASE_VERSION ?= ` + version + `
MOBILE_RELEASE ?= mobile-release

.PHONY: ` + strings.Join(targets, " ") + `

release-install:
	go install ` + Module + `@$(MOBILE_RELEASE_VERSION)

release-setup:
	$(MOBILE_RELEASE) setup

release-doctor:
	$(MOBILE_RELEASE) doctor

deploy:
	$(MOBILE_RELEASE) deploy

deploy-dry-run:
	$(MOBILE_RELEASE) deploy -dry-run

release-qa:
	$(MOBILE_RELEASE) qa
` + EndMarker + "\n"
}

var versionPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// Plan lists the changes, without touching the disk.
func Plan(options Options) ([]Change, error) {
	if !versionPattern.MatchString(options.Version) {
		return nil, fmt.Errorf("version %q is not a release (vX.Y.Z); pass -version", options.Version)
	}
	var changes []Change
	makefile, err := planMakefile(options)
	if err != nil {
		return nil, err
	}
	changes = append(changes, makefile)
	gitignore, err := planGitignore(options)
	if err != nil {
		return nil, err
	}
	changes = append(changes, gitignore)
	if options.Workflow {
		changes = append(changes, planWorkflow(options))
	}
	return changes, nil
}

func read(root, path string) (string, bool, error) {
	data, err := os.ReadFile(filepath.Join(root, path))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

// definedTargets returns which managed targets a Makefile already defines
// outside the managed block.
func definedTargets(makefile string) []string {
	outside := makefile
	if begin := strings.Index(makefile, BeginMarker); begin >= 0 {
		if end := strings.Index(makefile[begin:], EndMarker); end >= 0 {
			outside = makefile[:begin] + makefile[begin+end+len(EndMarker):]
		}
	}
	var found []string
	for _, target := range targets {
		if regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `\s*:`).MatchString(outside) {
			found = append(found, target)
		}
	}
	return found
}

func planMakefile(options Options) (Change, error) {
	block := MakefileBlock(options.Version)
	current, exists, err := read(options.Root, "Makefile")
	if err != nil {
		return Change{}, err
	}
	if !exists {
		return Change{Path: "Makefile", Action: Create, Content: block}, nil
	}
	if conflicts := definedTargets(current); len(conflicts) > 0 {
		return Change{Path: "Makefile", Action: Skip, Reason: "already defines " + strings.Join(conflicts, ", ") +
			" outside a managed block; remove them and run adopt again to let mobile-release manage them"}, nil
	}
	if begin := strings.Index(current, BeginMarker); begin >= 0 {
		end := strings.Index(current[begin:], EndMarker)
		if end < 0 {
			return Change{Path: "Makefile", Action: Skip, Reason: "has a managed block without its end marker"}, nil
		}
		end += begin + len(EndMarker)
		if end < len(current) && current[end] == '\n' {
			end++
		}
		updated := current[:begin] + block + current[end:]
		if updated == current {
			return Change{Path: "Makefile", Action: Skip, Reason: "already up to date"}, nil
		}
		return Change{Path: "Makefile", Action: Update, Reason: "managed block refreshed", Content: updated}, nil
	}
	separator := "\n"
	if !strings.HasSuffix(current, "\n") {
		separator = "\n\n"
	}
	return Change{Path: "Makefile", Action: Append, Content: separator + block}, nil
}

func planGitignore(options Options) (Change, error) {
	directory := "/" + strings.Trim(options.Config.Artifacts.Directory, "/") + "/"
	current, exists, err := read(options.Root, ".gitignore")
	if err != nil {
		return Change{}, err
	}
	for _, line := range strings.Split(current, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == directory || trimmed == strings.TrimPrefix(directory, "/") || trimmed == strings.TrimSuffix(directory, "/") {
			return Change{Path: ".gitignore", Action: Skip, Reason: directory + " already ignored"}, nil
		}
	}
	entry := "# mobile-release build outputs\n" + directory + "\n"
	if !exists {
		return Change{Path: ".gitignore", Action: Create, Content: entry}, nil
	}
	separator := "\n"
	if current != "" && !strings.HasSuffix(current, "\n") {
		separator = "\n\n"
	}
	return Change{Path: ".gitignore", Action: Append, Content: separator + entry}, nil
}

// WorkflowFile is the generated CI workflow.
// flutterVersionFile is .fvmrc or pubspec.yaml (Flutter projects only).
func WorkflowFile(c config.Config, version, ref, flutterVersionFile string) string {
	var options []string
	options = append(options, c.EnvironmentNames()...)
	platforms := "[all, android, ios]"
	defaultPlatform := "all"
	switch {
	case c.AndroidEnabled() && !c.IOSEnabled():
		platforms, defaultPlatform = "[android]", "android"
	case c.IOSEnabled() && !c.AndroidEnabled():
		platforms, defaultPlatform = "[ios]", "ios"
	}
	var extra []string
	switch c.Project.Type {
	case config.TypeFlutter:
		extra = append(extra, "      flutter-version-file: "+flutterVersionFile)
	case config.TypeExpo:
		extra = append(extra, "      node-version: '26'")
	}
	if c.AndroidEnabled() {
		extra = append(extra, "      java-version: '21'")
	} else {
		extra = append(extra, "      java-version: ''")
	}
	comment := " # " + version
	return `name: Release

# Manual releases through mobile-release (configuration in release.toml).
# Android-only runs work on Linux; iOS needs a macOS runner.
on:
  workflow_dispatch:
    inputs:
      environment:
        description: Environment from release.toml
        type: choice
        options: [` + strings.Join(options, ", ") + `]
        required: true
      platform:
        type: choice
        options: ` + platforms + `
        default: ` + defaultPlatform + `

permissions:
  contents: read

jobs:
  release:
    uses: ` + Repository + `/.github/workflows/reusable-release.yml@` + ref + comment + `
    with:
      environment: ${{ inputs.environment }}
      platform: ${{ inputs.platform }}
      runner: ${{ inputs.platform == 'android' && 'ubuntu-latest' || 'macos-26' }}
      mobile-release-version: ` + version + `
` + strings.Join(extra, "\n") + `
    secrets: inherit
`
}

func planWorkflow(options Options) Change {
	path := ".github/workflows/release.yml"
	if _, exists, _ := read(options.Root, path); exists {
		return Change{Path: path, Action: Skip, Reason: "already exists"}
	}
	ref := options.WorkflowRef
	if ref == "" {
		ref = options.Version
	}
	flutterVersionFile := "pubspec.yaml"
	if _, exists, _ := read(options.Root, ".fvmrc"); exists {
		flutterVersionFile = ".fvmrc"
	}
	return Change{Path: path, Action: Create, Content: WorkflowFile(options.Config, options.Version, ref, flutterVersionFile)}
}

// Apply writes the changes. Skips are ignored.
func Apply(root string, changes []Change) error {
	for _, change := range changes {
		path := filepath.Join(root, change.Path)
		switch change.Action {
		case Create, Update:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(change.Content), 0o644); err != nil {
				return err
			}
		case Append:
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			_, err = file.WriteString(change.Content)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Pending reports whether any change writes to disk.
func Pending(changes []Change) bool {
	for _, change := range changes {
		if change.Action != Skip {
			return true
		}
	}
	return false
}
