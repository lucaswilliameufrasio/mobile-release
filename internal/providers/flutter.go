package providers

import (
	"context"

	"github.com/lucaswilliameufrasio/mobile-release/internal/build"
	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/internal/runner"
)

// Flutter builds with the flutter tool. Defines and the env_file become
// --dart-define / --dart-define-from-file; Gradle signing goes through env.
type Flutter struct{}

func (Flutter) Prepare(ctx context.Context, r runner.Runner, _ build.Context) error {
	return r.Run(ctx, runner.Command{Name: "flutter", Args: []string{"pub", "get"}})
}

func (Flutter) Android(ctx context.Context, r runner.Runner, b build.Context, kind build.Kind) error {
	target := "apk"
	if kind == build.AAB {
		target = "appbundle"
	}
	args := append([]string{"build", target, "--release"}, flutterArgs(b, true)...)
	return r.Run(ctx, runner.Command{Name: "flutter", Args: args, Env: b.GradleEnv})
}

func (Flutter) AndroidOutputs(c config.Config, kind build.Kind) []string {
	if kind == build.AAB {
		return []string{c.Android.AABOutput, "build/app/outputs/bundle/release/app-release.aab", "build/app/outputs/bundle/*Release/*.aab"}
	}
	return []string{c.Android.APKOutput, "build/app/outputs/flutter-apk/app-release.apk", "build/app/outputs/flutter-apk/*-release.apk"}
}

// IOSConfigure writes Generated.xcconfig (defines, obfuscation and build
// number) so the shared Xcode archive builds the Dart code correctly.
func (Flutter) IOSConfigure(ctx context.Context, r runner.Runner, b build.Context, kind build.Kind) error {
	if kind == build.IOSSimulator {
		args := append([]string{"build", "ios", "--simulator", "--debug"}, flutterArgs(b, false)...)
		return r.Run(ctx, runner.Command{Name: "flutter", Args: args})
	}
	args := append([]string{"build", "ios", "--config-only", "--release"}, flutterArgs(b, true)...)
	return r.Run(ctx, runner.Command{Name: "flutter", Args: args})
}

// The Flutter build number reaches Xcode through Generated.xcconfig.
func (Flutter) IOSArchiveArgs(build.Context) []string { return nil }

func flutterArgs(b build.Context, release bool) []string {
	var args []string
	if b.BuildNumber != "" {
		args = append(args, "--build-number="+b.BuildNumber)
	}
	if b.Environment != nil && b.Environment.EnvFile != "" {
		args = append(args, "--dart-define-from-file="+b.Environment.EnvFile)
	}
	for _, pair := range build.SortedPairs(b.Defines) {
		args = append(args, "--dart-define="+pair)
	}
	if release && b.Config.FlutterObfuscate() {
		args = append(args, "--obfuscate", "--split-debug-info="+b.Config.Flutter.SplitDebugInfo)
	}
	return args
}
