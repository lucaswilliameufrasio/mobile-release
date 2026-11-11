package adopt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Parse([]byte(`
schema_version = 2
[project]
type = "flutter"
[android]
package_name = "com.example"
[ios]
bundle_id = "com.example"
[environments.staging]
destinations = ["firebase"]
[environments.staging.firebase]
android_app_id = "a"
ios_app_id = "b"
[environments.production]
destinations = ["play", "testflight"]
`))
	if err != nil {
		t.Fatal(err)
	}
	c.ApplyDefaults()
	return c
}

func write(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func byPath(changes []Change) map[string]Change {
	out := map[string]Change{}
	for _, change := range changes {
		out[change.Path] = change
	}
	return out
}

func TestFreshProjectCreatesEverything(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".fvmrc", `{"flutter":"3.47.9"}`)
	options := Options{Root: root, Config: testConfig(t), Version: "v2.1.0", Workflow: true, WorkflowRef: "abc123"}
	changes, err := Plan(options)
	if err != nil {
		t.Fatal(err)
	}
	plan := byPath(changes)
	for _, path := range []string{"Makefile", ".gitignore", ".github/workflows/release.yml"} {
		if plan[path].Action != Create {
			t.Fatalf("%s = %+v", path, plan[path])
		}
	}
	if err := Apply(root, changes); err != nil {
		t.Fatal(err)
	}
	makefile := readFile(t, root, "Makefile")
	if !strings.Contains(makefile, "MOBILE_RELEASE_VERSION ?= v2.1.0") || !strings.Contains(makefile, "go install "+Module+"@$(MOBILE_RELEASE_VERSION)") {
		t.Fatalf("makefile:\n%s", makefile)
	}
	workflow := readFile(t, root, ".github/workflows/release.yml")
	for _, want := range []string{
		"reusable-release.yml@abc123 # v2.1.0",
		"options: [production, staging]",
		"mobile-release-version: v2.1.0",
		"flutter-version-file: .fvmrc",
		"permissions:\n  contents: read",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("workflow lacks %q:\n%s", want, workflow)
		}
	}
	if readFile(t, root, ".gitignore") != "# mobile-release build outputs\n/dist/\n" {
		t.Fatalf("gitignore = %q", readFile(t, root, ".gitignore"))
	}

	// Running again changes nothing.
	again, err := Plan(options)
	if err != nil {
		t.Fatal(err)
	}
	if Pending(again) {
		t.Fatalf("second run should be a no-op: %+v", again)
	}
}

func TestExistingFilesAreAppendedNotOverwritten(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "test:\n\tgo test ./...")
	write(t, root, ".gitignore", "build/")
	changes, err := Plan(Options{Root: root, Config: testConfig(t), Version: "v2.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, changes); err != nil {
		t.Fatal(err)
	}
	makefile := readFile(t, root, "Makefile")
	if !strings.HasPrefix(makefile, "test:\n\tgo test ./...\n\n"+BeginMarker) {
		t.Fatalf("makefile:\n%s", makefile)
	}
	if readFile(t, root, ".gitignore") != "build/\n\n# mobile-release build outputs\n/dist/\n" {
		t.Fatalf("gitignore = %q", readFile(t, root, ".gitignore"))
	}
	if _, ok := byPath(changes)[".github/workflows/release.yml"]; ok {
		t.Fatal("workflow only when requested")
	}
}

func TestHandWrittenTargetsAreLeftAlone(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "deploy:\n\t./scripts/deploy.sh\n")
	write(t, root, ".gitignore", "dist/\n")
	write(t, root, ".github/workflows/release.yml", "name: mine\n")
	changes, err := Plan(Options{Root: root, Config: testConfig(t), Version: "v2.1.0", Workflow: true})
	if err != nil {
		t.Fatal(err)
	}
	plan := byPath(changes)
	if plan["Makefile"].Action != Skip || !strings.Contains(plan["Makefile"].Reason, "deploy") {
		t.Fatalf("makefile = %+v", plan["Makefile"])
	}
	if plan[".gitignore"].Action != Skip || plan[".github/workflows/release.yml"].Action != Skip {
		t.Fatalf("plan = %+v", plan)
	}
	if Pending(changes) {
		t.Fatal("nothing should be written")
	}
}

func TestManagedBlockIsUpdatedInPlace(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "before:\n\ttrue\n\n"+MakefileBlock("v2.0.1")+"\nafter:\n\ttrue\n")
	changes, err := Plan(Options{Root: root, Config: testConfig(t), Version: "v2.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	makefile := byPath(changes)["Makefile"]
	if makefile.Action != Update {
		t.Fatalf("makefile = %+v", makefile)
	}
	if err := Apply(root, changes); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, root, "Makefile")
	if strings.Contains(content, "v2.0.1") || !strings.Contains(content, "v2.1.0") ||
		!strings.HasPrefix(content, "before:") || !strings.HasSuffix(content, "after:\n\ttrue\n") ||
		strings.Count(content, BeginMarker) != 1 {
		t.Fatalf("makefile:\n%s", content)
	}
}

func TestPlatformsFollowTheConfig(t *testing.T) {
	c := testConfig(t)
	disabled := false
	c.IOS.Enabled = &disabled
	workflow := WorkflowFile(c, "v2.1.0", "abc", "pubspec.yaml")
	if !strings.Contains(workflow, "options: [android]") || !strings.Contains(workflow, "default: android") {
		t.Fatalf("workflow:\n%s", workflow)
	}
}

func TestRejectsDevelopmentVersions(t *testing.T) {
	for _, version := range []string{"dev", "(devel)", "2.1.0", ""} {
		if _, err := Plan(Options{Root: t.TempDir(), Config: testConfig(t), Version: version}); err == nil {
			t.Errorf("%q should be rejected", version)
		}
	}
}
