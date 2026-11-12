package release

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/credentials"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/runner"
)

type laneCall struct {
	platform, lane string
	env            map[string]string
}

type fakeLanes struct {
	calls   []laneCall
	outputs map[string]string
}

func toMap(env []string) map[string]string {
	values := map[string]string{}
	for _, pair := range env {
		key, value, _ := strings.Cut(pair, "=")
		values[key] = value
	}
	return values
}

func (f *fakeLanes) Run(_ context.Context, platform, lane string, env []string) error {
	f.calls = append(f.calls, laneCall{platform, lane, toMap(env)})
	return nil
}

func (f *fakeLanes) Output(ctx context.Context, platform, lane string, env []string) (string, error) {
	_ = f.Run(ctx, platform, lane, env)
	return f.outputs[platform+" "+lane], nil
}

func (f *fakeLanes) names() []string {
	var names []string
	for _, call := range f.calls {
		names = append(names, call.platform+" "+call.lane)
	}
	return names
}

func (f *fakeLanes) find(t *testing.T, name string) laneCall {
	t.Helper()
	for _, call := range f.calls {
		if call.platform+" "+call.lane == name {
			return call
		}
	}
	t.Fatalf("lane %q not called; calls: %v", name, f.names())
	return laneCall{}
}

const (
	secretPassword = "s3cr3t-keystore-pass"
	secretMatch    = "m4tch-pass"
)

func secrets(t *testing.T) *credentials.Set {
	t.Helper()
	set := credentials.NewSet()
	put := func(id string, data string) {
		spec, err := credentials.Lookup(id)
		if err != nil {
			t.Fatal(err)
		}
		set.Put(credentials.Value{Spec: spec, Source: "test", Data: []byte(data)})
	}
	put(credentials.AndroidKeystore, "KEYSTORE")
	put(credentials.AndroidKeystorePassword, secretPassword)
	put(credentials.AndroidKeyAlias, "upload")
	put(credentials.AndroidKeyPassword, secretPassword)
	put(credentials.FirebaseServiceAccount, `{"type":"service_account"}`)
	put(credentials.PlayServiceAccount, `{"type":"service_account"}`)
	put(credentials.AppStoreKeyID, "KEYID")
	put(credentials.AppStoreIssuerID, "ISSUER")
	put(credentials.AppStorePrivateKey, "P8")
	put(credentials.MatchGitURL, "git@example.com:certs.git")
	put(credentials.MatchPassword, secretMatch)
	if err := set.Materialize(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return set
}

func project(t *testing.T, toml string, files ...string) (string, config.Config) {
	t.Helper()
	root := t.TempDir()
	for _, file := range files {
		path := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := config.Parse([]byte(toml))
	if err != nil {
		t.Fatal(err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return root, c
}

var fixedNow = func() time.Time { return time.Date(2026, 11, 10, 15, 30, 0, 0, time.UTC) }

const flutterToml = `
schema_version = 2
[project]
type = "flutter"
[app]
name = "Example"
[android]
package_name = "com.example.app"
[ios]
bundle_id = "com.example.app"
team_id = "ABCDE12345"
signing = "match"

[environments.staging]
env_file = ".env"
defines = { APP_COMMIT = "{commit}" }
destinations = ["firebase"]
[environments.staging.firebase]
android_app_id = "1:1:android:a"
ios_app_id = "1:1:ios:b"
groups = ["qa"]

[environments.production]
env_file = ".env.prd"
defines = { APP_VARIANT = "prd", APP_COMMIT = "{commit}" }
build_number = "store"
destinations = ["play", "testflight"]
`

var flutterFiles = []string{
	".env", ".env.prd",
	"build/app/outputs/flutter-apk/app-release.apk",
	"build/app/outputs/bundle/release/app-release.aab",
}

func gitFake() *runner.Fake {
	return &runner.Fake{Outputs: map[string]string{"git rev-parse --short HEAD": "abc1234"}}
}

func TestFlutterStagingFirebaseBothPlatforms(t *testing.T) {
	root, c := project(t, flutterToml, flutterFiles...)
	fake := gitFake()
	lanes := &fakeLanes{}
	service := Service{Root: root, Config: c, Runner: fake, Lanes: lanes, Credentials: secrets(t), Now: fixedNow}
	result, err := service.Deploy(context.Background(), Request{Environment: "staging", Android: true, IOS: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"git rev-parse --short HEAD",
		"flutter pub get",
		"[ORG_GRADLE_PROJECT_android.injected.signing.key.alias ORG_GRADLE_PROJECT_android.injected.signing.key.password ORG_GRADLE_PROJECT_android.injected.signing.store.file ORG_GRADLE_PROJECT_android.injected.signing.store.password] flutter build apk --release --build-number=263141530 --dart-define-from-file=.env --dart-define=APP_COMMIT=abc1234 --obfuscate --split-debug-info=build/symbols",
		"flutter build ios --config-only --release --build-number=263141530 --dart-define-from-file=.env --dart-define=APP_COMMIT=abc1234 --obfuscate --split-debug-info=build/symbols",
	}
	if strings.Join(fake.Calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(fake.Calls, "\n"))
	}
	if got := strings.Join(lanes.names(), ","); got != "android firebase,ios build,ios firebase" {
		t.Fatalf("lanes = %s", got)
	}
	android := lanes.find(t, "android firebase")
	if android.env["MOBILE_RELEASE_FIREBASE_APP_ID"] != "1:1:android:a" || android.env["MOBILE_RELEASE_FIREBASE_GROUPS"] != "qa" {
		t.Fatalf("android firebase env = %v", android.env)
	}
	if !strings.HasSuffix(android.env["MOBILE_RELEASE_ANDROID_APK_PATH"], "dist/staging/android/com.example.app-staging-263141530.apk") {
		t.Fatalf("apk path = %q", android.env["MOBILE_RELEASE_ANDROID_APK_PATH"])
	}
	if _, err := os.Stat(android.env["MOBILE_RELEASE_ANDROID_APK_PATH"]); err != nil {
		t.Fatalf("artifact not copied: %v", err)
	}
	ios := lanes.find(t, "ios build")
	for key, want := range map[string]string{
		"MOBILE_RELEASE_IOS_EXPORT_METHOD": "ad-hoc",
		"MOBILE_RELEASE_IOS_SIGNING":       "match",
		"MOBILE_RELEASE_IOS_SCHEME":        "Runner",
		"MOBILE_RELEASE_IOS_TEAM_ID":       "ABCDE12345",
		"MATCH_PASSWORD":                   secretMatch,
	} {
		if ios.env[key] != want {
			t.Errorf("ios build %s = %q, want %q", key, ios.env[key], want)
		}
	}
	if !strings.HasSuffix(ios.env["MOBILE_RELEASE_IOS_WORKSPACE"], "ios/Runner.xcworkspace") {
		t.Fatalf("workspace = %q", ios.env["MOBILE_RELEASE_IOS_WORKSPACE"])
	}
	if lanes.find(t, "ios firebase").env["MOBILE_RELEASE_FIREBASE_APP_ID"] != "1:1:ios:b" {
		t.Fatal("ios firebase app id not passed")
	}
	if len(result.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v", result.Artifacts)
	}
}

func TestFlutterProductionStoreBuildNumbers(t *testing.T) {
	root, c := project(t, flutterToml, flutterFiles...)
	fake := gitFake()
	lanes := &fakeLanes{outputs: map[string]string{"android next_build_number": "42", "ios next_build_number": "7"}}
	service := Service{Root: root, Config: c, Runner: fake, Lanes: lanes, Credentials: secrets(t), Now: fixedNow}
	if _, err := service.Deploy(context.Background(), Request{Environment: "production", Android: true, IOS: true}); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(fake.Calls, "\n")
	for _, want := range []string{
		"flutter build appbundle --release --build-number=42 --dart-define-from-file=.env.prd --dart-define=APP_COMMIT=abc1234 --dart-define=APP_VARIANT=prd",
		"flutter build ios --config-only --release --build-number=7 --dart-define-from-file=.env.prd",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q in:\n%s", want, calls)
		}
	}
	if got := strings.Join(lanes.names(), ","); got != "android next_build_number,android play,ios next_build_number,ios build,ios testflight" {
		t.Fatalf("lanes = %s", got)
	}
	play := lanes.find(t, "android play")
	if play.env["MOBILE_RELEASE_ANDROID_TRACK"] != "internal" || play.env["MOBILE_RELEASE_ANDROID_PACKAGE_NAME"] != "com.example.app" {
		t.Fatalf("play env = %v", play.env)
	}
	if lanes.find(t, "ios build").env["MOBILE_RELEASE_IOS_EXPORT_METHOD"] != "app-store" {
		t.Fatal("testflight needs an app-store export")
	}
}

func TestSecretsNeverAppearInCommands(t *testing.T) {
	root, c := project(t, flutterToml, flutterFiles...)
	fake := gitFake()
	service := Service{Root: root, Config: c, Runner: fake, Lanes: &fakeLanes{}, Credentials: secrets(t), Now: fixedNow}
	if _, err := service.Deploy(context.Background(), Request{Environment: "staging", Android: true, IOS: true}); err != nil {
		t.Fatal(err)
	}
	for _, call := range fake.Calls {
		if strings.Contains(call, secretPassword) || strings.Contains(call, secretMatch) {
			t.Fatalf("secret leaked into command line: %s", call)
		}
	}
}

func TestExpoOTACommandAndGradleVersion(t *testing.T) {
	root, c := project(t, `
schema_version = 2
[project]
type = "expo"
[app]
name = "Expo App"
[android]
package_name = "com.example.expo"
signing = "project"
[ios]
enabled = false
[environments.preview]
env_file = ".env.preview"
build_number = "12"
destinations = ["firebase", "ota"]
[environments.preview.firebase]
android_app_id = "1:1:android:e"
[environments.preview.ota]
mode = "command"
command = "./scripts/publish-update.sh"
`, ".env.preview", "pnpm-lock.yaml", "android/app/build/outputs/apk/release/app-release.apk")
	if err := os.WriteFile(filepath.Join(root, ".env.preview"), []byte("EXPO_PUBLIC_API=https://api.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := gitFake()
	lanes := &fakeLanes{}
	service := Service{Root: root, Config: c, Runner: fake, Lanes: lanes, Credentials: secrets(t), Now: fixedNow}
	if _, err := service.Deploy(context.Background(), Request{Environment: "preview", Android: true, IOS: true}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"git rev-parse --short HEAD",
		"[EXPO_PUBLIC_API] pnpm install --frozen-lockfile",
		"[EXPO_PUBLIC_API] npx expo prebuild --clean",
		"[EXPO_PUBLIC_API ORG_GRADLE_PROJECT_android.injected.version.code] (cd android) ./gradlew assembleRelease",
		"[EXPO_PUBLIC_API] npx expo export --output-dir dist/preview/ota",
	}
	got := fake.Calls[:len(want)]
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(fake.Calls, "\n"))
	}
	last := fake.Calls[len(fake.Calls)-1]
	if last != "[EXPO_PUBLIC_API MOBILE_RELEASE_OTA_CHANNEL MOBILE_RELEASE_OTA_DIR MOBILE_RELEASE_RELEASE_NOTES] sh -c ./scripts/publish-update.sh" {
		t.Fatalf("ota publish must only get OTA values, got: %s", last)
	}
	// Signing is left to the project: no injected keystore.
	if strings.Contains(strings.Join(fake.Calls, "\n"), "injected.signing") {
		t.Fatal("android.signing = project must not inject signing")
	}
	if strings.Join(lanes.names(), ",") != "android firebase" {
		t.Fatalf("lanes = %v (ios disabled)", lanes.names())
	}
}

func TestJirauIOSArchive(t *testing.T) {
	root, c := project(t, `
schema_version = 2
[project]
type = "native"
preset = "jirau"
[app]
name = "Vault"
[android]
enabled = false
[ios]
bundle_id = "com.example.vault"
project = "ios/Vault.xcodeproj"
[environments.beta]
destinations = ["testflight"]
`, "scripts/build-ios-ffi.sh")
	fake := gitFake()
	lanes := &fakeLanes{}
	service := Service{Root: root, Config: c, Runner: fake, Lanes: lanes, Credentials: secrets(t), Now: fixedNow}
	if _, err := service.Deploy(context.Background(), Request{Environment: "beta", Android: true, IOS: true}); err != nil {
		t.Fatal(err)
	}
	want := []string{"git rev-parse --short HEAD", "./scripts/build-ios-ffi.sh release", "xcodegen generate --spec ios/project.yml"}
	if strings.Join(fake.Calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(fake.Calls, "\n"))
	}
	archive := lanes.find(t, "ios build")
	if !strings.HasSuffix(archive.env["MOBILE_RELEASE_IOS_PROJECT"], "ios/Vault.xcodeproj") || archive.env["MOBILE_RELEASE_IOS_SCHEME"] != "Vault" {
		t.Fatalf("archive env = %v", archive.env)
	}
	if archive.env["MOBILE_RELEASE_IOS_XCARGS"] != "CURRENT_PROJECT_VERSION=263141530" || archive.env["MOBILE_RELEASE_IOS_SIGNING"] != "automatic" {
		t.Fatalf("xcargs/signing = %v", archive.env)
	}
	lanes.find(t, "ios testflight")
}

func TestKMPArtifactsWithPrepare(t *testing.T) {
	root, c := project(t, `
schema_version = 2
[project]
type = "kmp"
[app]
name = "KMP"
[android]
package_name = "com.example.kmp"
apk_task = ":composeApp:assembleRelease"
aab_task = ":composeApp:bundleRelease"
[ios]
enabled = false
[prepare]
android = ["./gradlew :shared:generateResources"]
[environments.nightly]
build_number = "project"
destinations = ["artifacts"]
`, "composeApp/build/outputs/apk/release/composeApp-release.apk", "composeApp/build/outputs/bundle/release/composeApp-release.aab")
	fake := gitFake()
	service := Service{Root: root, Config: c, Runner: fake, Lanes: &fakeLanes{}, Credentials: secrets(t), Now: fixedNow}
	result, err := service.Deploy(context.Background(), Request{Environment: "nightly", Android: true})
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(fake.Calls, "\n")
	for _, want := range []string{"sh -c ./gradlew :shared:generateResources", "./gradlew :composeApp:assembleRelease", "./gradlew :composeApp:bundleRelease"} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q in:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "injected.version.code") {
		t.Fatal("build_number = project must keep the project's versionCode")
	}
	if len(result.Artifacts) != 2 || result.Artifacts[1].Path != "dist/nightly/android/com.example.kmp-nightly.aab" {
		t.Fatalf("artifacts = %+v", result.Artifacts)
	}
}

func TestFlutterQA(t *testing.T) {
	root, c := project(t, flutterToml, flutterFiles...)
	fake := gitFake()
	service := Service{Root: root, Config: c, Runner: fake, Lanes: &fakeLanes{}, Now: fixedNow}
	if _, err := service.QA(context.Background(), "staging", true, true); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"git rev-parse --short HEAD",
		"flutter pub get",
		"flutter build apk --release --dart-define-from-file=.env --dart-define=APP_COMMIT=abc1234 --obfuscate --split-debug-info=build/symbols",
		"flutter build ios --simulator --debug --dart-define-from-file=.env --dart-define=APP_COMMIT=abc1234",
	}
	if strings.Join(fake.Calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(fake.Calls, "\n"))
	}
}

func TestDryRunNeedsNoArtifactsOrStore(t *testing.T) {
	root, c := project(t, flutterToml, ".env", ".env.prd")
	lanes := &fakeLanes{}
	service := Service{Root: root, Config: c, Runner: gitFake(), Lanes: lanes, Credentials: secrets(t), Now: fixedNow, DryRun: true}
	if _, err := service.Deploy(context.Background(), Request{Environment: "production", Android: true, IOS: true}); err != nil {
		t.Fatal(err)
	}
	if lanes.find(t, "android play").env["MOBILE_RELEASE_ANDROID_AAB_PATH"] == "" {
		t.Fatal("dry run should still describe the upload")
	}
}

func TestMissingEnvFile(t *testing.T) {
	root, c := project(t, flutterToml)
	service := Service{Root: root, Config: c, Runner: gitFake(), Lanes: &fakeLanes{}}
	_, err := service.Deploy(context.Background(), Request{Environment: "staging", Android: true})
	if err == nil || !strings.Contains(err.Error(), "env_file .env not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestExportMethods(t *testing.T) {
	cases := map[string][]string{
		"firebase":            {"ad-hoc"},
		"testflight":          {"app-store"},
		"firebase,testflight": {"ad-hoc", "app-store"},
		"artifacts":           {"app-store"},
		"firebase,artifacts":  {"ad-hoc"},
		"play":                nil,
	}
	for destinations, want := range cases {
		env := &config.Environment{Destinations: strings.Split(destinations, ",")}
		if got := exportMethods(env); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: got %v want %v", destinations, got, want)
		}
	}
}

func TestEnvironmentPrepareRunsAfterGlobalAndOnlyForItsEnvironment(t *testing.T) {
	toml := strings.Replace(flutterToml, "[environments.staging]\n", "[prepare]\nall = [\"./global.sh\"]\n\n[environments.staging]\n", 1) + `
[environments.staging.prepare]
android = ["./icons.sh staging"]
`
	root, c := project(t, toml, flutterFiles...)
	fake := gitFake()
	service := Service{Root: root, Config: c, Runner: fake, Lanes: &fakeLanes{}, Credentials: secrets(t), Now: fixedNow}
	if _, err := service.Deploy(context.Background(), Request{Environment: "staging", Android: true}); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(fake.Calls, "\n")
	global := strings.Index(calls, "sh -c ./global.sh")
	icons := strings.Index(calls, "sh -c ./icons.sh staging")
	apk := strings.Index(calls, "flutter build apk")
	if global < 0 || icons < 0 || !(global < icons && icons < apk) {
		t.Fatalf("order wrong:\n%s", calls)
	}

	fake = gitFake()
	lanes := &fakeLanes{outputs: map[string]string{"android next_build_number": "5"}}
	service = Service{Root: root, Config: c, Runner: fake, Lanes: lanes, Credentials: secrets(t), Now: fixedNow}
	if _, err := service.Deploy(context.Background(), Request{Environment: "production", Android: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(fake.Calls, "\n"), "icons.sh") {
		t.Fatal("staging's prepare must not run for production")
	}
}
