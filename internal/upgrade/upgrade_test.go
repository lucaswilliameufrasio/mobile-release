package upgrade

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/adopt"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

func tags(releases []Release) string {
	var out []string
	for _, release := range releases {
		out = append(out, release.Tag)
	}
	return strings.Join(out, ",")
}

var sample = []Release{
	{Tag: "v2.0.1"}, {Tag: "v2.2.0"}, {Tag: "v2.3.0-rc.1", Pre: true}, {Tag: "v2.1.0"},
	{Tag: "v3.0.0"}, {Tag: "v2.2.1", Draft: true}, {Tag: "nightly"}, {Tag: "v2.0.0"},
}

func TestVersionOrdering(t *testing.T) {
	ordered := []string{"v1.9.9", "v2.0.0-rc.1", "v2.0.0", "v2.0.1", "v2.1.0", "v10.0.0"}
	for i := 0; i+1 < len(ordered); i++ {
		left, _ := Parse(ordered[i])
		right, _ := Parse(ordered[i+1])
		if !left.Less(right) || right.Less(left) {
			t.Errorf("%s should sort before %s", ordered[i], ordered[i+1])
		}
	}
	if _, ok := Parse("2.0.0"); ok {
		t.Fatal("tags need the v prefix")
	}
}

func TestStableTargetAndChangelog(t *testing.T) {
	stable := Stable(sample)
	if tags(stable) != "v3.0.0,v2.2.0,v2.1.0,v2.0.1,v2.0.0" {
		t.Fatalf("stable = %s", tags(stable))
	}
	same, next := Target(stable, 2)
	if same == nil || same.Tag != "v2.2.0" || next == nil || next.Tag != "v3.0.0" {
		t.Fatalf("same=%v next=%v", same, next)
	}
	if got := tags(Between(stable, "v2.0.1", "v2.2.0")); got != "v2.1.0,v2.2.0" {
		t.Fatalf("between = %s", got)
	}
	if got := tags(Between(stable, "", "v2.0.1")); got != "v2.0.0,v2.0.1" {
		t.Fatalf("from nothing = %s", got)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestFetch(t *testing.T) {
	client := &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		if !strings.Contains(request.URL.String(), adopt.Repository+"/releases") || request.Header.Get("User-Agent") == "" {
			t.Fatalf("request = %s", request.URL)
		}
		body := `[{"tag_name":"v2.2.0","body":"notes","draft":false,"prerelease":false}]`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	releases, err := Fetch(context.Background(), client)
	if err != nil || len(releases) != 1 || releases[0].Notes != "notes" {
		t.Fatalf("releases=%v err=%v", releases, err)
	}
	failing := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Status: "403 rate limited", Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if _, err := Fetch(context.Background(), failing); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Parse([]byte("schema_version = 2\n[project]\ntype = \"flutter\"\n[android]\npackage_name = \"a\"\n[ios]\nbundle_id = \"a\"\n[environments.qa]\ndestinations = [\"artifacts\"]\n"))
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

const workflow = `jobs:
  release:
    uses: lucaswilliameufrasio/mobile-release/.github/workflows/reusable-release.yml@aaaa # v2.0.1
    with:
      mobile-release-version: v2.0.1
`

func TestPlanProjectMovesEveryPin(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "build:\n\ttrue\n\n"+adopt.MakefileBlock("v2.0.1"))
	write(t, root, WorkflowPath, workflow)
	pins := ReadPins(root)
	if pins.Makefile != "v2.0.1" || pins.MakefileManual || pins.Workflow != "v2.0.1" || pins.WorkflowRef != "aaaa" || pins.Oldest() != "v2.0.1" {
		t.Fatalf("pins = %+v", pins)
	}
	changes, err := PlanProject(root, testConfig(t), "v2.2.0", "bbbb")
	if err != nil {
		t.Fatal(err)
	}
	if err := adopt.Apply(root, changes); err != nil {
		t.Fatal(err)
	}
	after := ReadPins(root)
	if after.Makefile != "v2.2.0" || after.Workflow != "v2.2.0" || after.WorkflowRef != "bbbb" {
		t.Fatalf("after = %+v", after)
	}
	data, _ := os.ReadFile(filepath.Join(root, WorkflowPath))
	if !strings.Contains(string(data), "reusable-release.yml@bbbb # v2.2.0") {
		t.Fatalf("workflow:\n%s", data)
	}
	again, err := PlanProject(root, testConfig(t), "v2.2.0", "bbbb")
	if err != nil {
		t.Fatal(err)
	}
	if adopt.Pending(again) {
		t.Fatalf("second plan should be a no-op: %+v", again)
	}
}

func TestPlanProjectLeavesHandWrittenPins(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "MOBILE_RELEASE_VERSION ?= v2.0.1\nrelease-install:\n\tgo install x@$(MOBILE_RELEASE_VERSION)\n")
	write(t, root, WorkflowPath, "jobs:\n  test:\n    runs-on: ubuntu-latest\n")
	pins := ReadPins(root)
	if !pins.MakefileManual || pins.Makefile != "v2.0.1" {
		t.Fatalf("pins = %+v", pins)
	}
	changes, err := PlanProject(root, testConfig(t), "v2.2.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if adopt.Pending(changes) {
		t.Fatalf("hand-written files must be left alone: %+v", changes)
	}
	if !strings.Contains(changes[0].Reason, "written by hand") || !strings.Contains(changes[1].Reason, "does not use") {
		t.Fatalf("reasons = %+v", changes)
	}
}
