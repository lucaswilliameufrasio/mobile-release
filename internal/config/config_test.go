package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const flutterV2 = `
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
build_number = "timestamp"
destinations = ["firebase"]

[environments.staging.firebase]
android_app_id = "1:1:android:a"
ios_app_id = "1:1:ios:b"

[environments.production]
env_file = ".env.prd"
defines = { APP_VARIANT = "prd" }
build_number = "store"
destinations = ["play", "testflight"]
`

func mustParse(t *testing.T, data string) Config {
	t.Helper()
	c, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	c.ApplyDefaults()
	return c
}

func TestParseV2(t *testing.T) {
	c := mustParse(t, flutterV2)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	production, err := c.Environment("production")
	if err != nil {
		t.Fatal(err)
	}
	if production.Name != "production" || production.Defines["APP_VARIANT"] != "prd" {
		t.Fatalf("production = %+v", production)
	}
	if !production.NeedsConfirmation() {
		t.Fatal("store environments must ask for confirmation")
	}
	staging, _ := c.Environment("staging")
	if staging.NeedsConfirmation() {
		t.Fatal("firebase-only environment should not need confirmation by default")
	}
	if got := c.EnvironmentNames(); strings.Join(got, ",") != "production,staging" {
		t.Fatalf("names = %v", got)
	}
}

func TestDefaultsByProjectType(t *testing.T) {
	c := mustParse(t, flutterV2)
	if c.IOS.Workspace != "ios/Runner.xcworkspace" || c.IOS.Scheme != "Runner" {
		t.Fatalf("flutter iOS defaults = %+v", c.IOS)
	}
	if c.App.ID != "com.example.app" {
		t.Fatalf("app id = %q", c.App.ID)
	}
	staging, _ := c.Environment("staging")
	if strings.Join(staging.Firebase.Groups, ",") != "internal-testers" {
		t.Fatalf("groups = %v", staging.Firebase.Groups)
	}
	if !c.FlutterObfuscate() {
		t.Fatal("obfuscation should default to true")
	}

	jirau := mustParse(t, `
schema_version = 2
[project]
type = "native"
preset = "jirau"
[app]
name = "My App!"
[environments.qa]
destinations = ["artifacts"]
`)
	if jirau.IOS.XcodeGenSpec != "ios/project.yml" || jirau.Android.ProjectDir != "android" {
		t.Fatalf("jirau defaults = %+v %+v", jirau.IOS, jirau.Android)
	}
	if jirau.App.ID != "my-app" {
		t.Fatalf("slug id = %q", jirau.App.ID)
	}
	if err := jirau.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownKeysAreRejected(t *testing.T) {
	_, err := Parse([]byte(flutterV2 + "\n[ios.extra]\nfoo = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Fatalf("expected unknown keys error, got %v", err)
	}
}

func TestValidateListsEveryProblem(t *testing.T) {
	c := mustParse(t, `
schema_version = 2
[project]
type = "flutter"
[ios]
signing = "match"
team_id = "short"
[environments.Prod]
build_number = "soon"
defines = { "bad-name" = "x" }
destinations = ["play", "testflight", "firebase", "ota", "carrier-pigeon"]
`)
	err := c.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{
		"team_id",
		`environment "Prod": use lowercase`,
		"build_number must be",
		`invalid define name "bad-name"`,
		"set android.package_name",
		"set ios.bundle_id",
		"set firebase.android_app_id",
		"OTA updates are only supported for expo",
		"unknown destination",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%s", want, err)
		}
	}
}

func TestV1IsUpgraded(t *testing.T) {
	c := mustParse(t, `
schema_version = 1
[project]
type = "expo"
[app]
name = "Example"
[android]
package_name = "com.example"
track = "beta"
[ios]
workspace = "ios/App.xcworkspace"
scheme = "App"
[artifacts]
directory = "dist"
`)
	if c.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("schema = %d", c.SchemaVersion)
	}
	internal, err := c.Environment("internal")
	if err != nil {
		t.Fatal(err)
	}
	if internal.Play.Track != "beta" || !internal.Has(DestinationPlay) || !internal.Has(DestinationTestFlight) {
		t.Fatalf("internal = %+v", internal)
	}
	if c.Android.Signing != AndroidSigningProject {
		t.Fatalf("v1 projects keep their own Gradle signing, got %q", c.Android.Signing)
	}
}

func TestEnvironmentOverrides(t *testing.T) {
	c := mustParse(t, flutterV2)
	values := map[string]string{
		"MOBILE_RELEASE_ANDROID_TRACK": "beta",
		"MOBILE_RELEASE_IOS_SCHEME":    "Custom",
	}
	c.ApplyEnv(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	production, _ := c.Environment("production")
	if production.Play.Track != "beta" || c.IOS.Scheme != "Custom" {
		t.Fatalf("overrides not applied: track=%q scheme=%q", production.Play.Track, c.IOS.Scheme)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	original := mustParse(t, flutterV2)
	data, err := Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	again := mustParse(t, string(data))
	if err := again.Validate(); err != nil {
		t.Fatalf("round trip invalid: %v\n%s", err, data)
	}
	production, _ := again.Environment("production")
	if production.BuildNumber != BuildNumberStore || production.EnvFile != ".env.prd" {
		t.Fatalf("round trip lost data:\n%s", data)
	}
}

func TestLoadMissingFile(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "release.toml"))
	if err != nil || c.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("c=%+v err=%v", c, err)
	}
	path := filepath.Join(t.TempDir(), "release.toml")
	if err := os.WriteFile(path, []byte("schema_version = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected syntax error")
	}
}

func TestExamplesAreValid(t *testing.T) {
	paths, err := filepath.Glob("../../examples/*/release.toml")
	if err != nil || len(paths) < 5 {
		t.Fatalf("examples = %v err=%v", paths, err)
	}
	for _, path := range paths {
		c, err := Load(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		c.ApplyDefaults()
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

const tixnowProvisioning = `
schema_version = 2

[project]
type = "flutter"

[app]
name = "TixNow"

[android]
package_name = "br.com.tixnow.app"

[ios]
bundle_id = "br.com.tixnow.app"
team_id = "ABCDE12345"
signing = "manual"

[ios.capabilities]
associated_domains = ["applinks:go.tixnow.com.br"]
push_notifications = true
sign_in_with_apple = true
app_groups = ["group.br.com.tixnow"]
other = ["NFC_TAG_READING"]

[[ios.extensions]]
bundle_id = "br.com.tixnow.app.notifications"
name = "TixNow Notifications"
capabilities = { app_groups = ["group.br.com.tixnow"] }

[[ios.extensions]]
bundle_id = "br.com.tixnow.app.clip"

[ios.provisioning]
directory = "ios/profiles"

[environments.staging]
destinations = ["firebase"]
ios_profiles = ["development", "ad-hoc"]

[environments.staging.firebase]
android_app_id = "1:1:android:a"
ios_app_id = "1:1:ios:b"

[environments.production]
destinations = ["play", "testflight"]
`

func TestParseProvisioning(t *testing.T) {
	c := mustParse(t, tixnowProvisioning)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := c.IOS.Capabilities.AssociatedDomains; len(got) != 1 || got[0] != "applinks:go.tixnow.com.br" {
		t.Fatalf("associated domains = %v", got)
	}
	if len(c.IOS.Extensions) != 2 || c.IOS.Extensions[0].Capabilities.AppGroups[0] != "group.br.com.tixnow" {
		t.Fatalf("extensions = %+v", c.IOS.Extensions)
	}
	if c.ProvisioningStrategy() != ProvisioningAPI || c.ProfilesDirectory() != "ios/profiles" || !c.InstallProfiles() {
		t.Fatalf("provisioning = %s %s %v", c.ProvisioningStrategy(), c.ProfilesDirectory(), c.InstallProfiles())
	}
	if got := strings.Join(c.ProfileTypes(""), ","); got != "development,ad-hoc,app-store" {
		t.Fatalf("profile types = %s", got)
	}
	if got := strings.Join(c.ProfileTypes("production"), ","); got != "app-store" {
		t.Fatalf("production profile types (derived) = %s", got)
	}
}

func TestProvisioningIsBackwardCompatible(t *testing.T) {
	// A file written before ios.capabilities existed keeps parsing, declares
	// nothing to enable and derives profiles from its destinations.
	c := mustParse(t, flutterV2)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if !c.IOS.Capabilities.Empty() || len(c.IOS.Extensions) != 0 {
		t.Fatalf("unexpected provisioning state: %+v", c.IOS)
	}
	if c.ProvisioningStrategy() != ProvisioningMatch || c.ProfilesDirectory() != filepath.Join("dist", "profiles") {
		t.Fatalf("defaults = %s %s", c.ProvisioningStrategy(), c.ProfilesDirectory())
	}
	if got := strings.Join(c.ProfileTypes(""), ","); got != "ad-hoc,app-store" {
		t.Fatalf("derived profile types = %s", got)
	}
	automatic := mustParse(t, strings.Replace(flutterV2, `signing = "match"`, `signing = "automatic"`, 1))
	if automatic.ProvisioningStrategy() != ProvisioningNone {
		t.Fatalf("automatic signing strategy = %s", automatic.ProvisioningStrategy())
	}
	// Marshalling an old config adds no new keys.
	data, err := Marshal(mustParse(t, flutterV2))
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"capabilities", "extensions", "provisioning", "ios_profiles"} {
		if strings.Contains(string(data), unwanted) {
			t.Errorf("marshal added %q:\n%s", unwanted, data)
		}
	}
}

func TestProvisioningValidation(t *testing.T) {
	bad := strings.NewReplacer(
		`"applinks:go.tixnow.com.br"`, `"go.tixnow.com.br"`,
		`app_groups = ["group.br.com.tixnow"]`+"\n", `app_groups = ["br.com.tixnow"]`+"\n",
		`"NFC_TAG_READING"`, `"NFC"`,
		`bundle_id = "br.com.tixnow.app.clip"`, `bundle_id = "br.com.tixnow.app.notifications"`,
		`ios_profiles = ["development", "ad-hoc"]`, `ios_profiles = ["enterprise"]`,
		`directory = "ios/profiles"`, `strategy = "sigh"`,
	).Replace(tixnowProvisioning)
	c := mustParse(t, bad)
	err := c.Validate()
	if err == nil {
		t.Fatal("expected problems")
	}
	for _, want := range []string{
		"associated_domains", `"br.com.tixnow" must start with "group."`, `unknown App Store Connect capability "NFC"`,
		"declared twice", "ios_profiles values", "ios.provisioning.strategy",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if _, err := Parse([]byte(strings.Replace(tixnowProvisioning, "push_notifications", "push", 1))); err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Fatalf("typos in capabilities must be rejected, got %v", err)
	}
}
