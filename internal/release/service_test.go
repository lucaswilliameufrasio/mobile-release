package release

import (
	"context"
	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type fake struct{ calls []string }

func (f *fake) Run(_ context.Context, n string, a ...string) error {
	f.calls = append(f.calls, n+" "+join(a))
	return nil
}
func (f *fake) LookPath(string) error { return nil }
func join(a []string) string {
	r := ""
	for i, v := range a {
		if i > 0 {
			r += " "
		}
		r += v
	}
	return r
}
func TestExpoQAIntegrationPlan(t *testing.T) {
	f := &fake{}
	c := config.Defaults()
	c.ProjectType = "expo"
	c.IOSWorkspace = "ios/App.xcworkspace"
	c.IOSScheme = "App"
	if e := (Service{R: f, C: c}).QA(context.Background()); e != nil {
		t.Fatal(e)
	}
	want := []string{"pnpm install --frozen-lockfile", "npx expo prebuild --clean", "./android/gradlew -p android assembleRelease", "xcodebuild -workspace ios/App.xcworkspace -scheme App -configuration Debug -sdk iphonesimulator -derivedDataPath build/ios-simulator CODE_SIGNING_ALLOWED=NO build"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %#v", f.calls)
	}
}
func TestFlutterInternalAndroidPlan(t *testing.T) {
	f := &fake{}
	c := config.Defaults()
	c.ProjectType = "flutter"
	c.AndroidAABOutput = stubAAB(t)
	if e := (Service{R: f, C: c}).Internal(context.Background(), "android"); e != nil {
		t.Fatal(e)
	}
	want := []string{"flutter pub get", "flutter build appbundle --release", "bundle exec fastlane android internal"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %#v", f.calls)
	}
}
func TestKMPInternalAllPlan(t *testing.T) {
	f := &fake{}
	c := config.Defaults()
	c.ProjectType = "kmp"
	c.AndroidAABTask = ":composeApp:bundleRelease"
	c.AndroidAABOutput = stubAAB(t)
	if e := (Service{R: f, C: c}).Internal(context.Background(), "all"); e != nil {
		t.Fatal(e)
	}
	if len(f.calls) != 4 {
		t.Fatalf("calls %#v", f.calls)
	}
}
func stubAAB(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "app-release.aab")
	if e := os.WriteFile(p, []byte("x"), 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestResolveAABConfigWins(t *testing.T) {
	want := stubAAB(t)
	c := config.Defaults()
	c.ProjectType = "flutter"
	c.AndroidAABOutput = want
	got, e := resolveAAB(c)
	if e != nil || got != want {
		t.Fatalf("%s %v", got, e)
	}
}
func TestResolveAABDefaultPerProvider(t *testing.T) {
	for _, tc := range []struct{ typ, rel string }{
		{"expo", "android/app/build/outputs/bundle/release/app-release.aab"},
		{"flutter", "build/app/outputs/bundle/release/app-release.aab"},
	} {
		dir := t.TempDir()
		p := filepath.Join(dir, tc.rel)
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte("x"), 0600); e != nil {
			t.Fatal(e)
		}
		c := config.Defaults()
		c.ProjectType = tc.typ
		old, _ := os.Getwd()
		if e := os.Chdir(dir); e != nil {
			t.Fatal(e)
		}
		got, e := resolveAAB(c)
		if ch := os.Chdir(old); ch != nil {
			t.Fatal(ch)
		}
		if e != nil || got != tc.rel {
			t.Fatalf("%s: %s %v", tc.typ, got, e)
		}
	}
}
func TestResolveAABMissing(t *testing.T) {
	c := config.Defaults()
	c.ProjectType = "kmp"
	if _, e := resolveAAB(c); e == nil {
		t.Fatal("expected error")
	}
}
