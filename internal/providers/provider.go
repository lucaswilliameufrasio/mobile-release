// Package providers turns a build context into the project-specific commands
// for Flutter, Expo, Kotlin Multiplatform and native (Gradle/Xcode) apps.
//
// Every provider only prepares the Xcode project; archiving and exporting
// the IPA is shared (fastlane gym), so all project types get the same
// signing options.
package providers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/build"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/runner"
)

// Provider builds one project type.
type Provider interface {
	// Prepare installs dependencies and generates native projects.
	Prepare(context.Context, runner.Runner, build.Context) error
	// Android builds an APK or AAB.
	Android(context.Context, runner.Runner, build.Context, build.Kind) error
	// AndroidOutputs lists glob patterns, in order, where the artifact lands.
	AndroidOutputs(config.Config, build.Kind) []string
	// IOSConfigure readies the Xcode project for the shared archive (IPA) or,
	// for IOSSimulator, produces the unsigned simulator build.
	IOSConfigure(context.Context, runner.Runner, build.Context, build.Kind) error
	// IOSArchiveArgs are extra xcodebuild settings for the archive.
	IOSArchiveArgs(build.Context) []string
}

// For returns the provider for the project type rooted at root.
func For(projectType, root string) (Provider, error) {
	switch projectType {
	case config.TypeFlutter:
		return Flutter{}, nil
	case config.TypeExpo:
		return Expo{Root: root}, nil
	case config.TypeKMP:
		return KMP{Root: root}, nil
	case config.TypeNative:
		return Native{Root: root}, nil
	}
	return nil, fmt.Errorf("unsupported project type %q", projectType)
}

// IOSContainer is the resolved Xcode workspace or project and scheme.
type IOSContainer struct {
	Workspace string
	Project   string
	Scheme    string
}

// Args renders the container as xcodebuild arguments.
func (c IOSContainer) Args() []string {
	if c.Workspace != "" {
		return []string{"-workspace", c.Workspace, "-scheme", c.Scheme}
	}
	return []string{"-project", c.Project, "-scheme", c.Scheme}
}

// ResolveIOSContainer uses the configured workspace/project, or finds one
// under ios/ (generated projects only exist after Prepare/IOSConfigure).
func ResolveIOSContainer(root string, c config.Config) (IOSContainer, error) {
	container := IOSContainer{Workspace: c.IOS.Workspace, Project: c.IOS.Project, Scheme: c.IOS.Scheme}
	if container.Workspace == "" && container.Project == "" {
		directory := "ios"
		if c.Project.Type == config.TypeKMP {
			directory = "iosApp"
		}
		if found := glob(root, directory+"/*.xcworkspace"); found != "" {
			container.Workspace = found
		} else if found := glob(root, directory+"/*.xcodeproj"); found != "" {
			container.Project = found
		} else {
			return container, fmt.Errorf("no Xcode workspace or project in %s/; set ios.workspace or ios.project", directory)
		}
	}
	if container.Scheme == "" {
		name := container.Workspace
		if name == "" {
			name = container.Project
		}
		container.Scheme = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(name), ".xcworkspace"), ".xcodeproj")
	}
	return container, nil
}

// SimulatorBuild is the unsigned Debug simulator build shared by providers.
func SimulatorBuild(container IOSContainer, env []string) runner.Command {
	args := append(container.Args(),
		"-configuration", "Debug",
		"-sdk", "iphonesimulator",
		"-derivedDataPath", "build/ios-simulator",
		"CODE_SIGNING_ALLOWED=NO",
		"build",
	)
	return runner.Command{Name: "xcodebuild", Args: args, Env: env}
}

// ResolveOutput returns the newest file matching the first pattern with
// matches, relative to root.
func ResolveOutput(root string, patterns []string) (string, error) {
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil || len(matches) == 0 {
			continue
		}
		sort.Slice(matches, func(i, j int) bool {
			left, _ := os.Stat(matches[i])
			right, _ := os.Stat(matches[j])
			if left == nil || right == nil {
				return matches[i] < matches[j]
			}
			return left.ModTime().After(right.ModTime())
		})
		relative, err := filepath.Rel(root, matches[0])
		if err != nil {
			return matches[0], nil
		}
		return filepath.ToSlash(relative), nil
	}
	return "", fmt.Errorf("artifact not found (searched %s)", strings.Join(patterns, ", "))
}

func glob(root, pattern string) string {
	matches, _ := filepath.Glob(filepath.Join(root, pattern))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	relative, err := filepath.Rel(root, matches[0])
	if err != nil {
		return ""
	}
	return filepath.ToSlash(relative)
}

// buildEnv is the process environment for non-Flutter builds: env_file
// values and defines (e.g. EXPO_PUBLIC_*), plus Gradle signing/version.
func buildEnv(b build.Context, gradle bool) []string {
	env := append([]string{}, b.EnvFileVars...)
	env = append(env, build.SortedPairs(b.Defines)...)
	if gradle {
		env = append(env, b.GradleEnv...)
	}
	return env
}

// gradleTask picks the configured task or the default release task.
func gradleTask(c config.Config, kind build.Kind, apkDefault, aabDefault string) string {
	if kind == build.AAB {
		if c.Android.AABTask != "" {
			return c.Android.AABTask
		}
		return aabDefault
	}
	if c.Android.APKTask != "" {
		return c.Android.APKTask
	}
	return apkDefault
}

func gradleOutputs(c config.Config, kind build.Kind, directory string) []string {
	if kind == build.AAB {
		return []string{c.Android.AABOutput, directory + "/app/build/outputs/bundle/release/*.aab", directory + "/*/build/outputs/bundle/release/*.aab"}
	}
	return []string{c.Android.APKOutput, directory + "/app/build/outputs/apk/release/*.apk", directory + "/*/build/outputs/apk/release/*.apk"}
}

func currentProjectVersion(b build.Context) []string {
	if b.BuildNumber == "" {
		return nil
	}
	return []string{"CURRENT_PROJECT_VERSION=" + b.BuildNumber}
}
