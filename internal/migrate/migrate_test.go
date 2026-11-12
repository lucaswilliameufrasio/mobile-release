package migrate

import (
	"strings"
	"testing"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

const redundant = `# Releases for the shop app.
schema_version = 2

[project]
type = "flutter"

[app]
name = "Shop"
id = "com.example.shop"  # same as the package

[android]
package_name = "com.example.shop"
signing = "injected"

[ios]
bundle_id = "com.example.shop"
# Automatic signing until match is set up.
signing = "automatic"
workspace = "ios/Runner.xcworkspace"

[environments.staging]
destinations = ["firebase"]
build_number = "timestamp"

[environments.staging.firebase]
android_app_id = "1:1:android:a"
ios_app_id = "1:1:ios:b"
groups = ["internal-testers"]

[environments.production]
destinations = ["play"]
build_number = "store"

[environments.production.play]
track = "internal"
release_status = "completed"
`

func TestV2DropsDefaultsAndKeepsComments(t *testing.T) {
	result, err := Run([]byte(redundant))
	if err != nil {
		t.Fatal(err)
	}
	text := string(result.Content)
	for _, gone := range []string{`signing = "injected"`, "workspace =", `build_number = "timestamp"`, "groups =", "[environments.production.play]", "track =", "release_status ="} {
		if strings.Contains(text, gone) {
			t.Errorf("%q should be removed:\n%s", gone, text)
		}
	}
	// Commented defaults document a choice and are kept.
	for _, kept := range []string{"# Releases for the shop app.", "# Automatic signing until match is set up.", `signing = "automatic"`, `id = "com.example.shop"  # same as the package`, `build_number = "store"`, `android_app_id = "1:1:android:a"`} {
		if !strings.Contains(text, kept) {
			t.Errorf("%q should be kept:\n%s", kept, text)
		}
	}
	if strings.Contains(text, "\n\n\n") {
		t.Fatalf("blank lines should be collapsed:\n%s", text)
	}
	if result.Rewritten || !result.Changed([]byte(redundant)) {
		t.Fatalf("result = %+v", result)
	}
	fixed := 0
	for _, finding := range result.Findings {
		if finding.Fixed && finding.Line > 0 {
			fixed++
		}
	}
	if fixed != 6 {
		t.Fatalf("fixed findings = %d: %+v", fixed, result.Findings)
	}

	again, err := Run(result.Content)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed(result.Content) {
		t.Fatalf("second run must be a no-op:\n%s", again.Content)
	}
}

func TestNonDefaultsAndMultilineValuesAreKept(t *testing.T) {
	data := `schema_version = 2
[project]
type = "expo"
[android]
package_name = "a"
project_dir = "mobile/android"
[ios]
enabled = false
[environments.beta]
destinations = [
  "firebase",
]
[environments.beta.firebase]
android_app_id = "x"
groups = ["qa", "internal-testers"]
[environments.beta.play]
track = "beta"
`
	result, err := Run([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed([]byte(data)) {
		t.Fatalf("nothing should change:\n%s", result.Content)
	}
}

func TestV1IsRewrittenAsV2(t *testing.T) {
	data := `schema_version = 1
[project]
type = "kmp"
[app]
name = "Example"
[android]
package_name = "com.example"
track = "beta"
apk_task = ":app:assembleRelease"
aab_task = ":app:bundleRelease"
[ios]
workspace = "iosApp/iosApp.xcworkspace"
scheme = "iosApp"
`
	result, err := Run([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	text := string(result.Content)
	if !result.Rewritten || !strings.HasPrefix(text, "# Migrated from schema v1") || !strings.Contains(text, "schema_version = 2") {
		t.Fatalf("content:\n%s", text)
	}
	c, err := config.Parse(result.Content)
	if err != nil {
		t.Fatal(err)
	}
	c.ApplyDefaults()
	internal, err := c.Environment("internal")
	if err != nil || internal.Play.Track != "beta" {
		t.Fatalf("internal = %+v err=%v", internal, err)
	}
	advised, todo := false, false
	for _, finding := range result.Findings {
		if finding.Key == "android.track" && !finding.Fixed {
			advised = true
		}
		// Schema 1 had no bundle id, which TestFlight needs in schema 2.
		if strings.Contains(finding.Message, "to do:") && strings.Contains(finding.Message, "ios.bundle_id") {
			todo = true
		}
	}
	if !advised || !todo {
		t.Fatalf("expected advice for android.track and ios.bundle_id: %+v", result.Findings)
	}
}

func TestInvalidInput(t *testing.T) {
	if _, err := Run([]byte("schema_version = [")); err == nil {
		t.Fatal("syntax errors must fail")
	}
}

func TestCommentedNonDefaultIsNotReported(t *testing.T) {
	data := "schema_version = 2\n[project]\ntype = \"native\"\n[android]\npackage_name = \"a\"\n# The Gradle project signs by itself.\nsigning = \"project\"\n[ios]\nenabled = false\n[environments.beta]\ndestinations = [\"artifacts\"]\n"
	result, err := Run([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range result.Findings {
		if finding.Key == "android.signing" {
			t.Fatalf("non-default value reported: %+v", finding)
		}
	}
	if result.Changed([]byte(data)) {
		t.Fatal("nothing should change")
	}
}
