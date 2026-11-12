// Package release orchestrates builds and publication for one environment.
package release

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/build"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/credentials"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/providers"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/runner"
)

// LaneRunner runs the shared fastlane lanes.
type LaneRunner interface {
	Run(ctx context.Context, platform, lane string, env []string) error
	Output(ctx context.Context, platform, lane string, env []string) (string, error)
}

// Service runs releases for a project.
type Service struct {
	Root        string
	Config      config.Config
	Runner      runner.Runner
	Lanes       LaneRunner
	Credentials *credentials.Set
	Out         io.Writer
	Now         func() time.Time
	// DryRun prints commands; artifacts and store numbers are placeholders.
	DryRun bool
	// Git runs read-only git queries (defaults to Runner); dry runs still
	// need the real commit.
	Git runner.Runner
}

// Request selects what to release.
type Request struct {
	Environment  string
	Android      bool
	IOS          bool
	ReleaseNotes string
}

// Artifact is one produced file.
type Artifact struct {
	Platform    string
	Kind        build.Kind
	Path        string
	BuildNumber string
}

// Result summarizes a run.
type Result struct {
	Artifacts    []Artifact
	Destinations []string
}

func (s Service) printf(format string, args ...any) {
	if s.Out != nil {
		fmt.Fprintf(s.Out, format, args...)
	}
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Service) credentials() *credentials.Set {
	if s.Credentials != nil {
		return s.Credentials
	}
	return credentials.NewSet()
}

// Deploy builds and publishes req.Environment.
func (s Service) Deploy(ctx context.Context, req Request) (Result, error) {
	var result Result
	env, err := s.Config.Environment(req.Environment)
	if err != nil {
		return result, err
	}
	android := req.Android && s.Config.AndroidEnabled()
	ios := req.IOS && s.Config.IOSEnabled()
	provider, err := providers.For(s.Config.Project.Type, s.Root)
	if err != nil {
		return result, err
	}
	base, err := s.baseContext(ctx, env)
	if err != nil {
		return result, err
	}
	base.Release = true
	base = withBuildNumber(base, "")
	notes := req.ReleaseNotes
	if notes == "" {
		notes = fmt.Sprintf("%s build from %s", env.Name, firstNonEmpty(base.Commit, "unknown commit"))
	}

	s.printf("==> Preparing %s\n", s.Config.Project.Type)
	if err := s.prepare(ctx, base, stepAll); err != nil {
		return result, err
	}
	if err := provider.Prepare(ctx, s.Runner, base); err != nil {
		return result, err
	}

	if android {
		artifacts, err := s.deployAndroid(ctx, provider, base, env, notes)
		result.Artifacts = append(result.Artifacts, artifacts...)
		if err != nil {
			return result, err
		}
	}
	if ios {
		artifacts, err := s.deployIOS(ctx, provider, base, env, notes)
		result.Artifacts = append(result.Artifacts, artifacts...)
		if err != nil {
			return result, err
		}
	}
	if env.Has(config.DestinationOTA) {
		if err := s.publishOTA(ctx, base, env, notes); err != nil {
			return result, err
		}
	}
	result.Destinations = env.Destinations
	return result, nil
}

// QA builds an APK and an iOS simulator app without release signing.
func (s Service) QA(ctx context.Context, envName string, android, ios bool) (Result, error) {
	var result Result
	var env *config.Environment
	if envName != "" {
		found, err := s.Config.Environment(envName)
		if err != nil {
			return result, err
		}
		env = found
	}
	provider, err := providers.For(s.Config.Project.Type, s.Root)
	if err != nil {
		return result, err
	}
	base, err := s.baseContext(ctx, env)
	if err != nil {
		return result, err
	}
	base = withBuildNumber(base, "")
	if err := s.prepare(ctx, base, stepAll); err != nil {
		return result, err
	}
	if err := provider.Prepare(ctx, s.Runner, base); err != nil {
		return result, err
	}
	if android && s.Config.AndroidEnabled() {
		if err := s.prepare(ctx, base, stepAndroid); err != nil {
			return result, err
		}
		artifact, err := s.buildAndroid(ctx, provider, base, build.APK)
		if err != nil {
			return result, err
		}
		result.Artifacts = append(result.Artifacts, artifact)
	}
	if ios && s.Config.IOSEnabled() {
		if err := s.prepare(ctx, base, stepIOS); err != nil {
			return result, err
		}
		s.printf("==> iOS simulator build\n")
		if err := provider.IOSConfigure(ctx, s.Runner, base, build.IOSSimulator); err != nil {
			return result, err
		}
		result.Artifacts = append(result.Artifacts, Artifact{Platform: "ios", Kind: build.IOSSimulator, Path: firstNonEmpty(s.Config.IOS.SimulatorOutput, "build/ios-simulator")})
	}
	return result, nil
}

func (s Service) baseContext(ctx context.Context, env *config.Environment) (build.Context, error) {
	b := build.Context{Config: s.Config, Environment: env}
	git := s.Git
	if git == nil {
		git = s.Runner
	}
	if commit, err := git.Output(ctx, runner.Command{Name: "git", Args: []string{"rev-parse", "--short", "HEAD"}}); err == nil {
		b.Commit = commit
	}
	if env == nil {
		return b, nil
	}
	if env.EnvFile != "" {
		path := filepath.Join(s.Root, env.EnvFile)
		if _, err := os.Stat(path); err != nil {
			return b, fmt.Errorf("environment %q: env_file %s not found", env.Name, env.EnvFile)
		}
		if s.Config.Project.Type != config.TypeFlutter {
			pairs, err := build.ReadEnvFile(path)
			if err != nil {
				return b, err
			}
			b.EnvFileVars = pairs
		}
	}
	return b, nil
}

// withBuildNumber returns a copy of b for one platform's build number.
func withBuildNumber(b build.Context, number string) build.Context {
	b.BuildNumber = number
	b.Defines = build.ExpandDefines(environmentDefines(b), b.Commit, number, b.EnvName())
	return b
}

func environmentDefines(b build.Context) map[string]string {
	if b.Environment == nil {
		return nil
	}
	return b.Environment.Defines
}

// buildNumber resolves the environment strategy for one platform.
func (s Service) buildNumber(ctx context.Context, env *config.Environment, platform string) (string, error) {
	switch env.BuildNumber {
	case config.BuildNumberProject:
		return "", nil
	case config.BuildNumberTimestamp, "":
		return build.TimestampBuildNumber(s.now()), nil
	case config.BuildNumberStore:
		s.printf("==> %s: asking the store for the latest build number\n", platform)
		laneEnv := append(s.credentials().LaneEnv(), s.identityEnv()...)
		number, err := s.Lanes.Output(ctx, platform, "next_build_number", laneEnv)
		if err != nil {
			return "", err
		}
		if s.DryRun {
			return "STORE_NEXT", nil
		}
		if _, err := strconv.Atoi(number); err != nil {
			return "", fmt.Errorf("%s store returned an invalid build number %q", platform, number)
		}
		return number, nil
	default:
		return env.BuildNumber, nil
	}
}

func (s Service) identityEnv() []string {
	return []string{
		"MOBILE_RELEASE_ANDROID_PACKAGE_NAME=" + s.Config.Android.PackageName,
		"MOBILE_RELEASE_IOS_BUNDLE_ID=" + s.Config.IOS.BundleID,
	}
}

func (s Service) deployAndroid(ctx context.Context, provider providers.Provider, base build.Context, env *config.Environment, notes string) ([]Artifact, error) {
	var artifacts []Artifact
	number, err := s.buildNumber(ctx, env, "android")
	if err != nil {
		return nil, err
	}
	b := withBuildNumber(base, number)
	if s.Config.Android.Signing == config.AndroidSigningInjected {
		b.GradleEnv = s.credentials().GradleEnv()
	}
	if number != "" && s.Config.Project.Type != config.TypeFlutter {
		b.GradleEnv = append(b.GradleEnv, "ORG_GRADLE_PROJECT_android.injected.version.code="+number)
	}
	if err := s.prepare(ctx, b, stepAndroid); err != nil {
		return nil, err
	}
	var kinds []build.Kind
	if env.Has(config.DestinationFirebase) || env.Has(config.DestinationArtifacts) {
		kinds = append(kinds, build.APK)
	}
	if env.Has(config.DestinationPlay) || env.Has(config.DestinationArtifacts) {
		kinds = append(kinds, build.AAB)
	}
	for _, kind := range kinds {
		artifact, err := s.buildAndroid(ctx, provider, b, kind)
		if err != nil {
			return artifacts, err
		}
		artifacts = append(artifacts, artifact)
		laneEnv := append(s.credentials().LaneEnv(), s.identityEnv()...)
		laneEnv = append(laneEnv, "MOBILE_RELEASE_RELEASE_NOTES="+notes)
		switch {
		case kind == build.APK && env.Has(config.DestinationFirebase):
			s.printf("==> android: Firebase App Distribution\n")
			laneEnv = append(laneEnv,
				"MOBILE_RELEASE_ANDROID_APK_PATH="+filepath.Join(s.Root, artifact.Path),
				"MOBILE_RELEASE_FIREBASE_APP_ID="+env.Firebase.AndroidAppID,
				"MOBILE_RELEASE_FIREBASE_GROUPS="+strings.Join(env.Firebase.Groups, ","),
				"MOBILE_RELEASE_FIREBASE_TESTERS="+strings.Join(env.Firebase.Testers, ","),
			)
			if err := s.Lanes.Run(ctx, "android", "firebase", laneEnv); err != nil {
				return artifacts, err
			}
		case kind == build.AAB && env.Has(config.DestinationPlay):
			s.printf("==> android: Google Play (%s)\n", env.Play.Track)
			laneEnv = append(laneEnv,
				"MOBILE_RELEASE_ANDROID_AAB_PATH="+filepath.Join(s.Root, artifact.Path),
				"MOBILE_RELEASE_ANDROID_TRACK="+env.Play.Track,
				"MOBILE_RELEASE_ANDROID_RELEASE_STATUS="+env.Play.ReleaseStatus,
			)
			if err := s.Lanes.Run(ctx, "android", "play", laneEnv); err != nil {
				return artifacts, err
			}
		}
	}
	return artifacts, nil
}

func (s Service) buildAndroid(ctx context.Context, provider providers.Provider, b build.Context, kind build.Kind) (Artifact, error) {
	s.printf("==> android: %s\n", kind)
	if err := provider.Android(ctx, s.Runner, b, kind); err != nil {
		return Artifact{}, err
	}
	name := fmt.Sprintf("%s-%s", firstNonEmpty(s.Config.App.ID, "app"), b.EnvName())
	if b.BuildNumber != "" {
		name += "-" + b.BuildNumber
	}
	target := filepath.Join(s.Config.Artifacts.Directory, b.EnvName(), "android", name+"."+string(kind))
	if !s.DryRun {
		output, err := providers.ResolveOutput(s.Root, provider.AndroidOutputs(s.Config, kind))
		if err != nil {
			return Artifact{}, err
		}
		if err := copyFile(filepath.Join(s.Root, output), filepath.Join(s.Root, target)); err != nil {
			return Artifact{}, err
		}
	}
	return Artifact{Platform: "android", Kind: kind, Path: filepath.ToSlash(target), BuildNumber: b.BuildNumber}, nil
}

// exportMethods maps destinations to the IPAs they need.
func exportMethods(env *config.Environment) []string {
	var methods []string
	if env.Has(config.DestinationFirebase) {
		methods = append(methods, "ad-hoc")
	}
	if env.Has(config.DestinationTestFlight) || env.Has(config.DestinationAppStore) || (env.Has(config.DestinationArtifacts) && len(methods) == 0) {
		methods = append(methods, "app-store")
	}
	return methods
}

func (s Service) deployIOS(ctx context.Context, provider providers.Provider, base build.Context, env *config.Environment, notes string) ([]Artifact, error) {
	var artifacts []Artifact
	methods := exportMethods(env)
	if len(methods) == 0 {
		return nil, nil
	}
	number, err := s.buildNumber(ctx, env, "ios")
	if err != nil {
		return nil, err
	}
	b := withBuildNumber(base, number)
	if err := s.prepare(ctx, b, stepIOS); err != nil {
		return nil, err
	}
	s.printf("==> ios: configuring the Xcode project\n")
	if err := provider.IOSConfigure(ctx, s.Runner, b, build.IPA); err != nil {
		return nil, err
	}
	container, err := providers.ResolveIOSContainer(s.Root, s.Config)
	if err != nil && s.DryRun {
		// Generated projects (Expo prebuild, XcodeGen) do not exist in a dry run.
		container, err = providers.IOSContainer{Workspace: "ios/<generated>.xcworkspace", Scheme: firstNonEmpty(s.Config.IOS.Scheme, "<scheme>")}, nil
	}
	if err != nil {
		return nil, err
	}
	for _, method := range methods {
		outputDir := filepath.Join(s.Root, s.Config.Artifacts.Directory, env.Name, "ios", method)
		laneEnv := append(s.credentials().LaneEnv(), s.identityEnv()...)
		laneEnv = append(laneEnv,
			"MOBILE_RELEASE_IOS_EXPORT_METHOD="+method,
			"MOBILE_RELEASE_IOS_SIGNING="+s.Config.IOS.Signing,
			"MOBILE_RELEASE_IOS_TEAM_ID="+s.Config.IOS.TeamID,
			"MOBILE_RELEASE_IOS_SCHEME="+container.Scheme,
			"MOBILE_RELEASE_IOS_CONFIGURATION="+s.Config.IOS.Configuration,
			"MOBILE_RELEASE_IOS_OUTPUT_DIR="+outputDir,
			"MOBILE_RELEASE_IOS_XCARGS="+strings.Join(provider.IOSArchiveArgs(b), " "),
			"MOBILE_RELEASE_RELEASE_NOTES="+notes,
		)
		if container.Workspace != "" {
			laneEnv = append(laneEnv, "MOBILE_RELEASE_IOS_WORKSPACE="+filepath.Join(s.Root, container.Workspace))
		} else {
			laneEnv = append(laneEnv, "MOBILE_RELEASE_IOS_PROJECT="+filepath.Join(s.Root, container.Project))
		}
		s.printf("==> ios: archive (%s)\n", method)
		if err := s.Lanes.Run(ctx, "ios", "build", laneEnv); err != nil {
			return artifacts, err
		}
		ipa := filepath.Join(outputDir, "app.ipa")
		relative, _ := filepath.Rel(s.Root, ipa)
		artifacts = append(artifacts, Artifact{Platform: "ios", Kind: build.IPA, Path: filepath.ToSlash(relative), BuildNumber: number})
		laneEnv = append(laneEnv, "MOBILE_RELEASE_IOS_IPA_PATH="+ipa)
		if method == "ad-hoc" {
			s.printf("==> ios: Firebase App Distribution\n")
			firebaseEnv := append(laneEnv,
				"MOBILE_RELEASE_FIREBASE_APP_ID="+env.Firebase.IOSAppID,
				"MOBILE_RELEASE_FIREBASE_GROUPS="+strings.Join(env.Firebase.Groups, ","),
				"MOBILE_RELEASE_FIREBASE_TESTERS="+strings.Join(env.Firebase.Testers, ","),
			)
			if err := s.Lanes.Run(ctx, "ios", "firebase", firebaseEnv); err != nil {
				return artifacts, err
			}
			continue
		}
		if env.Has(config.DestinationTestFlight) {
			s.printf("==> ios: TestFlight\n")
			if err := s.Lanes.Run(ctx, "ios", "testflight", laneEnv); err != nil {
				return artifacts, err
			}
		}
		if env.Has(config.DestinationAppStore) {
			s.printf("==> ios: App Store review submission\n")
			if err := s.Lanes.Run(ctx, "ios", "app_store", laneEnv); err != nil {
				return artifacts, err
			}
		}
	}
	return artifacts, nil
}

func (s Service) publishOTA(ctx context.Context, b build.Context, env *config.Environment, notes string) error {
	b = withBuildNumber(b, "")
	processEnv := append(append([]string{}, b.EnvFileVars...), build.SortedPairs(b.Defines)...)
	if env.OTA.Mode == "eas" {
		// Only the Expo token: OTA commands never see signing or store keys.
		for _, pair := range s.credentials().LaneEnv() {
			if strings.HasPrefix(pair, "EXPO_TOKEN=") {
				processEnv = append(processEnv, pair)
			}
		}
		s.printf("==> OTA: eas update (%s)\n", env.OTA.Channel)
		return s.Runner.Run(ctx, runner.Command{
			Name: "npx",
			Args: []string{"--yes", "eas-cli", "update", "--channel", env.OTA.Channel, "--message", notes, "--non-interactive"},
			Env:  processEnv,
		})
	}
	directory := filepath.Join(s.Config.Artifacts.Directory, env.Name, "ota")
	s.printf("==> OTA: export to %s\n", directory)
	if err := s.Runner.Run(ctx, runner.Command{Name: "npx", Args: []string{"expo", "export", "--output-dir", directory}, Env: processEnv}); err != nil {
		return err
	}
	processEnv = append(processEnv,
		"MOBILE_RELEASE_OTA_DIR="+filepath.Join(s.Root, directory),
		"MOBILE_RELEASE_OTA_CHANNEL="+env.OTA.Channel,
		"MOBILE_RELEASE_RELEASE_NOTES="+notes,
	)
	s.printf("==> OTA: publish command\n")
	return s.Runner.Run(ctx, runner.Command{Name: "sh", Args: []string{"-c", env.OTA.Command}, Env: processEnv})
}

// Prepare steps.
const (
	stepAll = iota
	stepAndroid
	stepIOS
)

// prepare runs the global [prepare] commands of a step, then the
// environment's own.
func (s Service) prepare(ctx context.Context, b build.Context, step int) error {
	pick := func(p config.Prepare) []string {
		switch step {
		case stepAndroid:
			return p.Android
		case stepIOS:
			return p.IOS
		}
		return p.All
	}
	commands := append([]string{}, pick(s.Config.Prepare)...)
	if b.Environment != nil {
		commands = append(commands, pick(b.Environment.Prepare)...)
	}
	return s.runPrepare(ctx, commands, b)
}

func (s Service) runPrepare(ctx context.Context, commands []string, b build.Context) error {
	for _, command := range commands {
		s.printf("==> prepare: %s\n", command)
		env := append(append([]string{}, b.EnvFileVars...), build.SortedPairs(b.Defines)...)
		if err := s.Runner.Run(ctx, runner.Command{Name: "sh", Args: []string{"-c", command}, Env: env}); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, target string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
