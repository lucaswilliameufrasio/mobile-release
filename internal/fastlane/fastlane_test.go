package fastlane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/runner"
)

func TestLanesAreMaterializedAndInstalledOnce(t *testing.T) {
	cache := t.TempDir()
	fake := &runner.Fake{}
	lanes := &Lanes{Runner: fake, ProjectDir: "/project", CacheDir: cache}
	if err := lanes.Run(context.Background(), "android", "play", []string{"MOBILE_RELEASE_ANDROID_TRACK=internal"}); err != nil {
		t.Fatal(err)
	}
	dir, _ := lanes.Dir()
	if filepath.Base(dir) != "lanes-"+Digest() {
		t.Fatalf("dir = %s", dir)
	}
	for _, file := range []string{"Gemfile", "Gemfile.lock", "fastlane/Fastfile", "fastlane/Pluginfile"} {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			t.Fatalf("%s not written: %v", file, err)
		}
	}
	if err := lanes.Run(context.Background(), "ios", "testflight", nil); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(fake.Calls, "\n")
	if strings.Count(calls, "bundle install") != 1 {
		t.Fatalf("gems must be installed once:\n%s", calls)
	}
	if !strings.Contains(calls, "bundle exec fastlane android play") || !strings.Contains(calls, "bundle exec fastlane ios testflight") {
		t.Fatalf("calls:\n%s", calls)
	}
	env := strings.Join(fake.Envs[1], "\n")
	for _, want := range []string{"BUNDLE_FROZEN=true", "MOBILE_RELEASE_PROJECT_DIR=/project", "MOBILE_RELEASE_ANDROID_TRACK=internal"} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %s in lane env", want)
		}
	}
}

func TestEmbeddedFastfileDeclaresEveryLane(t *testing.T) {
	data, err := assets.ReadFile("assets/fastlane/Fastfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range []string{"next_build_number", "play", "firebase", "build", "testflight", "app_store"} {
		if !strings.Contains(string(data), "lane :"+lane+" do") {
			t.Errorf("lane %s missing", lane)
		}
	}
	lock, _ := assets.ReadFile("assets/Gemfile.lock")
	for _, platform := range []string{"x86_64-linux", "arm64-darwin"} {
		if !strings.Contains(string(lock), platform) {
			t.Errorf("Gemfile.lock lacks %s", platform)
		}
	}
}
