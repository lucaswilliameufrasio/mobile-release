package providers

import (
	"context"
	"os"
	"path/filepath"

	"github.com/lucaswilliameufrasio/mobile-release/internal/build"
	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/internal/runner"
)

// Expo installs JavaScript dependencies, runs `expo prebuild` and then
// builds the generated android/ and ios/ projects. The env_file and defines
// are passed as process environment, so EXPO_PUBLIC_* values are inlined.
type Expo struct {
	// Root is used to pick the package manager from the lockfile.
	Root string
}

func (e Expo) Prepare(ctx context.Context, r runner.Runner, b build.Context) error {
	env := buildEnv(b, false)
	if err := r.Run(ctx, packageManagerInstall(e.Root, env)); err != nil {
		return err
	}
	return r.Run(ctx, runner.Command{Name: "npx", Args: []string{"expo", "prebuild", "--clean"}, Env: env})
}

func (Expo) Android(ctx context.Context, r runner.Runner, b build.Context, kind build.Kind) error {
	task := gradleTask(b.Config, kind, "assembleRelease", "bundleRelease")
	directory := b.Config.Android.ProjectDir
	return r.Run(ctx, runner.Command{Name: "./gradlew", Args: []string{task}, Dir: directory, Env: buildEnv(b, true)})
}

func (Expo) AndroidOutputs(c config.Config, kind build.Kind) []string {
	return gradleOutputs(c, kind, c.Android.ProjectDir)
}

func (e Expo) IOSConfigure(ctx context.Context, r runner.Runner, b build.Context, kind build.Kind) error {
	if kind != build.IOSSimulator {
		return nil
	}
	container, err := ResolveIOSContainer(e.Root, b.Config)
	if err != nil {
		return err
	}
	return r.Run(ctx, SimulatorBuild(container, buildEnv(b, false)))
}

func (Expo) IOSArchiveArgs(b build.Context) []string { return currentProjectVersion(b) }

// packageManagerInstall picks the install command from the lockfile.
func packageManagerInstall(root string, env []string) runner.Command {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
	switch {
	case has("pnpm-lock.yaml"):
		return runner.Command{Name: "pnpm", Args: []string{"install", "--frozen-lockfile"}, Env: env}
	case has("bun.lock") || has("bun.lockb"):
		return runner.Command{Name: "bun", Args: []string{"install", "--frozen-lockfile"}, Env: env}
	case has("yarn.lock") && has(".yarnrc.yml"):
		return runner.Command{Name: "yarn", Args: []string{"install", "--immutable"}, Env: env}
	case has("yarn.lock"):
		return runner.Command{Name: "yarn", Args: []string{"install", "--frozen-lockfile"}, Env: env}
	default:
		return runner.Command{Name: "npm", Args: []string{"ci"}, Env: env}
	}
}
