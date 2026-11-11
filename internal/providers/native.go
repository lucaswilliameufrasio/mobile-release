package providers

import (
	"context"
	"os"
	"path/filepath"

	"github.com/lucaswilliameufrasio/mobile-release/internal/build"
	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/internal/runner"
)

// Native builds a Gradle Android project and an Xcode iOS project directly.
// With preset "jirau", the iOS Rust core is built and the Xcode project is
// generated with XcodeGen first (Gradle builds the Android core in preBuild).
type Native struct {
	Root string
}

func (Native) Prepare(context.Context, runner.Runner, build.Context) error { return nil }

func (Native) Android(ctx context.Context, r runner.Runner, b build.Context, kind build.Kind) error {
	task := gradleTask(b.Config, kind, "assembleRelease", "bundleRelease")
	return r.Run(ctx, runner.Command{Name: "./gradlew", Args: []string{task}, Dir: b.Config.Android.ProjectDir, Env: buildEnv(b, true)})
}

func (Native) AndroidOutputs(c config.Config, kind build.Kind) []string {
	return gradleOutputs(c, kind, c.Android.ProjectDir)
}

func (n Native) IOSConfigure(ctx context.Context, r runner.Runner, b build.Context, kind build.Kind) error {
	if b.Config.Project.Preset == config.PresetJirau {
		profile := "release"
		if kind == build.IOSSimulator {
			profile = "debug"
		}
		if n.exists("scripts/build-ios-ffi.sh") {
			if err := r.Run(ctx, runner.Command{Name: "./scripts/build-ios-ffi.sh", Args: []string{profile}}); err != nil {
				return err
			}
		}
	}
	if spec := b.Config.IOS.XcodeGenSpec; spec != "" {
		if err := r.Run(ctx, runner.Command{Name: "xcodegen", Args: []string{"generate", "--spec", spec}}); err != nil {
			return err
		}
	}
	if kind != build.IOSSimulator {
		return nil
	}
	container, err := ResolveIOSContainer(n.Root, b.Config)
	if err != nil {
		return err
	}
	return r.Run(ctx, SimulatorBuild(container, buildEnv(b, false)))
}

func (Native) IOSArchiveArgs(b build.Context) []string { return currentProjectVersion(b) }

func (n Native) exists(path string) bool {
	_, err := os.Stat(filepath.Join(n.Root, path))
	return err == nil
}

// KMP builds Kotlin Multiplatform apps: Gradle tasks for Android (required
// in release.toml) and the iosApp Xcode project, whose build phase compiles
// the shared framework.
type KMP struct {
	Root string
}

func (KMP) Prepare(context.Context, runner.Runner, build.Context) error { return nil }

func (KMP) Android(ctx context.Context, r runner.Runner, b build.Context, kind build.Kind) error {
	task := gradleTask(b.Config, kind, ":composeApp:assembleRelease", ":composeApp:bundleRelease")
	return r.Run(ctx, runner.Command{Name: "./gradlew", Args: []string{task}, Dir: b.Config.Android.ProjectDir, Env: buildEnv(b, true)})
}

func (KMP) AndroidOutputs(c config.Config, kind build.Kind) []string {
	return gradleOutputs(c, kind, ".")
}

func (k KMP) IOSConfigure(ctx context.Context, r runner.Runner, b build.Context, kind build.Kind) error {
	if kind != build.IOSSimulator {
		return nil
	}
	container, err := ResolveIOSContainer(k.Root, b.Config)
	if err != nil {
		return err
	}
	return r.Run(ctx, SimulatorBuild(container, buildEnv(b, false)))
}

func (KMP) IOSArchiveArgs(b build.Context) []string { return currentProjectVersion(b) }
