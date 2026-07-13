package release

import (
	"context"
	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
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
	if e := (Service{R: f, C: c}).Internal(context.Background(), "all"); e != nil {
		t.Fatal(e)
	}
	if len(f.calls) != 4 {
		t.Fatalf("calls %#v", f.calls)
	}
}
