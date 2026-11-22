package provisioning

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/appstore"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/appstore/appstoretest"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

const (
	app    = "br.com.tixnow.app"
	clip   = "br.com.tixnow.app.clip"
	notify = "br.com.tixnow.app.notifications"
)

// tixnow is the TixNow case: Associated Domains was added to the app, the
// App Clip was never registered and the ad-hoc profile became invalid.
type tixnow struct {
	server  *appstoretest.Server
	desired Desired
	adHoc   *appstoretest.Profile
	store   *appstoretest.Profile
	devices []string
}

func newTixNow(t *testing.T, strategy string) tixnow {
	t.Helper()
	server := appstoretest.New(t)
	development := server.AddCertificate("DEVELOPMENT", 200)
	distribution := server.AddCertificate("DISTRIBUTION", 300)
	server.AddCertificate("IOS_DISTRIBUTION", -5) // expired: never used
	first, second := server.AddDevice("QA iPhone"), server.AddDevice("PM iPad")
	main := server.AddBundle(app, "PUSH_NOTIFICATIONS")
	server.AddBundle(notify, "APP_GROUPS")
	fixture := tixnow{server: server, devices: []string{first.ID, second.ID}}
	fixture.adHoc = server.AddProfile(main, "TixNow AdHoc", appstore.ProfileIOSAdHoc, appstore.ProfileStateInvalid, 200, []string{distribution.ID}, []string{first.ID})
	fixture.store = server.AddProfile(main, "TixNow AppStore", appstore.ProfileIOSAppStore, appstore.ProfileStateActive, 10, []string{distribution.ID}, nil)
	server.AddProfile(main, "TixNow Development", appstore.ProfileIOSDevelopment, appstore.ProfileStateActive, 300, []string{development.ID}, []string{first.ID, second.ID})
	notifyBundle := server.Bundle(notify)
	server.AddProfile(notifyBundle, notify+" AdHoc", appstore.ProfileIOSAdHoc, appstore.ProfileStateActive, 300, []string{distribution.ID}, []string{first.ID, second.ID})
	server.AddProfile(notifyBundle, notify+" AppStore", appstore.ProfileIOSAppStore, appstore.ProfileStateActive, 300, []string{distribution.ID}, nil)
	server.AddProfile(notifyBundle, notify+" Development", appstore.ProfileIOSDevelopment, appstore.ProfileStateActive, 300, []string{development.ID}, []string{first.ID, second.ID})

	root := t.TempDir()
	c := config.Config{
		App: config.App{Name: "TixNow"},
		IOS: config.IOS{
			BundleID: app, TeamID: "ABCDE12345", Signing: config.SigningManual,
			Capabilities: config.Capabilities{AssociatedDomains: []string{"applinks:go.tixnow.com.br"}, PushNotifications: true, SignInWithApple: true},
			Extensions: []config.IOSExtension{
				{BundleID: notify, Capabilities: config.Capabilities{AppGroups: []string{"group.br.com.tixnow"}}},
				{BundleID: clip, Name: "TixNow Clip", Capabilities: config.Capabilities{AssociatedDomains: []string{"appclips:go.tixnow.com.br"}}},
			},
			Provisioning: config.Provisioning{Strategy: strategy},
		},
		Environments: map[string]*config.Environment{
			"staging":    {Name: "staging", Destinations: []string{config.DestinationFirebase}, IOSProfiles: []string{config.ProfileDevelopment, config.ProfileAdHoc}},
			"production": {Name: "production", Destinations: []string{config.DestinationTestFlight}},
		},
	}
	fixture.desired = FromConfig(root, c, "")
	return fixture
}

func (f tixnow) planner(t *testing.T) Planner {
	return Planner{API: f.server.Client(t), Now: func() time.Time { return f.server.Now }}
}

func findTarget(t *testing.T, plan Plan, bundleID string) TargetPlan {
	t.Helper()
	for _, target := range plan.Targets {
		if target.Target.BundleID == bundleID {
			return target
		}
	}
	t.Fatalf("no target %s", bundleID)
	return TargetPlan{}
}

func findProfile(t *testing.T, target TargetPlan, kind string) ProfilePlan {
	t.Helper()
	for _, profile := range target.Profiles {
		if profile.Kind == kind {
			return profile
		}
	}
	t.Fatalf("no %s profile for %s", kind, target.Target.BundleID)
	return ProfilePlan{}
}

func labels(capabilities []Capability) []string {
	var list []string
	for _, capability := range capabilities {
		list = append(list, capability.Type)
	}
	return list
}

func TestPlanShowsEveryDifferenceAndWritesNothing(t *testing.T) {
	f := newTixNow(t, "")
	plan, err := f.planner(t).Plan(context.Background(), f.desired)
	if err != nil {
		t.Fatal(err)
	}
	if writes := f.server.Writes(); len(writes) != 0 {
		t.Fatalf("plan must be read-only: %v", writes)
	}

	main := findTarget(t, plan, app)
	if got := labels(main.Enable); !slices.Equal(got, []string{"ASSOCIATED_DOMAINS", "APPLE_ID_AUTH"}) {
		t.Fatalf("capabilities to enable = %v", got)
	}
	if got := labels(main.Enabled); !slices.Equal(got, []string{"PUSH_NOTIFICATIONS"}) {
		t.Fatalf("already enabled = %v", got)
	}
	adHoc := findProfile(t, main, config.ProfileAdHoc)
	if adHoc.Action != Recreate || !slices.Contains(adHoc.Reasons, "invalid") || !slices.Contains(adHoc.Reasons, "1 registered device(s) missing") || adHoc.Name != "TixNow AdHoc" {
		t.Fatalf("ad-hoc = %+v", adHoc)
	}
	store := findProfile(t, main, config.ProfileAppStore)
	if store.Action != Recreate || !slices.Contains(store.Reasons, "expires in 10 days") {
		t.Fatalf("app-store = %+v", store)
	}
	// The capability change alone invalidates the healthy development profile.
	development := findProfile(t, main, config.ProfileDevelopment)
	if development.Action != Recreate || !slices.Contains(development.Reasons, "capabilities change (the profile becomes invalid)") {
		t.Fatalf("development = %+v", development)
	}

	extension := findTarget(t, plan, notify)
	if len(extension.Enable) != 0 {
		t.Fatalf("notifications extension already has App Groups: %v", labels(extension.Enable))
	}
	for _, profile := range extension.Profiles {
		if profile.Action != Keep {
			t.Fatalf("notifications %s = %+v", profile.Kind, profile)
		}
	}

	appClip := findTarget(t, plan, clip)
	if appClip.Bundle != nil || !slices.Equal(labels(appClip.Enable), []string{"ASSOCIATED_DOMAINS"}) {
		t.Fatalf("clip = %+v", appClip)
	}
	if profile := findProfile(t, appClip, config.ProfileAdHoc); profile.Action != Create || profile.Name != clip+" AdHoc" {
		t.Fatalf("clip ad-hoc = %+v", profile)
	}

	// 1 App ID + 3 capabilities + 3 (app) + 3 (clip) profiles.
	if plan.Changes() != 10 || plan.Blocked() != 0 {
		t.Fatalf("changes = %d, blocked = %d", plan.Changes(), plan.Blocked())
	}

	var out bytes.Buffer
	Render(&out, plan)
	for _, want := range []string{
		"+ enable Associated Domains (ASSOCIATED_DOMAINS)",
		"applinks:go.tixnow.com.br",
		"assign group.br.com.tixnow to the App ID in the Apple Developer portal",
		`~ ad-hoc profile "TixNow AdHoc": invalid; capabilities change (the profile becomes invalid); 1 registered device(s) missing → recreate`,
		`+ App ID not registered: register "TixNow Clip"`,
		"10 change(s).",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestApplyEnablesCapabilitiesRecreatesProfilesAndIsIdempotent(t *testing.T) {
	f := newTixNow(t, "")
	ctx := context.Background()
	plan, err := f.planner(t).Plan(ctx, f.desired)
	if err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(t.TempDir(), "Provisioning Profiles")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(install, f.adHoc.UUID+".mobileprovision")
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	result := Applier{API: f.server.Client(t), Out: &out, InstallDirs: []string{install}}.Apply(ctx, plan)
	if err := result.Err(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if result.Registered != 1 || result.Enabled != 3 || result.Created != 3 || result.Recreated != 3 {
		t.Fatalf("result = %+v", result)
	}

	main := f.server.Bundle(app)
	if !slices.Contains(main.Capabilities, "ASSOCIATED_DOMAINS") || !slices.Contains(main.Capabilities, "APPLE_ID_AUTH") {
		t.Fatalf("capabilities = %v", main.Capabilities)
	}
	profiles := f.server.ProfilesOf(app)
	if len(profiles) != 3 {
		t.Fatalf("one profile per type expected: %+v", profiles)
	}
	for _, profile := range profiles {
		if profile.State != appstore.ProfileStateActive {
			t.Fatalf("%s is %s", profile.Name, profile.State)
		}
		if profile.Type != appstore.ProfileIOSAppStore && len(profile.DeviceIDs) != 2 {
			t.Fatalf("%s must include every registered device: %v", profile.Name, profile.DeviceIDs)
		}
		if len(profile.CertificateIDs) != 1 {
			t.Fatalf("%s certificates = %v (the expired one must be skipped)", profile.Name, profile.CertificateIDs)
		}
	}
	// Existing names are kept: manual signing projects reference them.
	if names := []string{profiles[0].Name, profiles[1].Name, profiles[2].Name}; !slices.Equal(names, []string{"TixNow AdHoc", "TixNow AppStore", "TixNow Development"}) {
		t.Fatalf("names = %v", names)
	}
	if f.server.Bundle(clip) == nil || len(f.server.ProfilesOf(clip)) != 3 {
		t.Fatal("the App Clip must be registered with its profiles")
	}

	written := filepath.Join(f.desired.Directory, FileName(app, config.ProfileAdHoc))
	data, err := os.ReadFile(written)
	if err != nil {
		t.Fatal(err)
	}
	var newAdHoc appstoretest.Profile
	for _, profile := range profiles {
		if profile.Type == appstore.ProfileIOSAdHoc {
			newAdHoc = profile
		}
	}
	if string(data) != string(appstoretest.Content(newAdHoc.Name, newAdHoc.UUID)) {
		t.Fatalf("written profile = %s", data)
	}
	if _, err := os.Stat(filepath.Join(install, newAdHoc.UUID+".mobileprovision")); err != nil {
		t.Fatalf("not installed: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("the replaced profile must be removed from the install folder")
	}
	// The unchanged extension profiles are downloaded too.
	if _, err := os.Stat(filepath.Join(f.desired.Directory, FileName(notify, config.ProfileAppStore))); err != nil {
		t.Fatalf("kept profile not written: %v", err)
	}

	again, err := f.planner(t).Plan(ctx, f.desired)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changes() != 0 {
		var rendered bytes.Buffer
		Render(&rendered, again)
		t.Fatalf("second plan must be empty:\n%s", rendered.String())
	}
	before := len(f.server.Writes())
	if err := (Applier{API: f.server.Client(t), InstallDirs: []string{install}}).Apply(ctx, again).Err(); err != nil {
		t.Fatal(err)
	}
	if after := len(f.server.Writes()); after != before {
		t.Fatalf("second apply wrote %d time(s)", after-before)
	}
}

func TestFailureAfterDeletingReportsWhatWasDeleted(t *testing.T) {
	f := newTixNow(t, "")
	ctx := context.Background()
	// The first POST /v1/profiles conflicts on the name (real API rule), the
	// second one, after the delete, fails.
	plan, err := f.planner(t).Plan(ctx, f.desired)
	if err != nil {
		t.Fatal(err)
	}
	plan.Targets = plan.Targets[:1] // only the app
	main := &plan.Targets[0]
	main.Profiles = []ProfilePlan{findProfile(t, *main, config.ProfileAdHoc)}
	f.server.Fail["POST /v1/profiles"] = appstoretest.Failure{Status: http.StatusInternalServerError, Times: 1, After: 1}

	result := Applier{API: f.server.Client(t)}.Apply(ctx, plan)
	if len(result.Failures) != 1 {
		t.Fatalf("failures = %v", result.Failures)
	}
	failure := result.Failures[0]
	if failure.Deleted == nil || failure.Deleted.ID != f.adHoc.ID || !strings.Contains(failure.Step, "run `mobile-release profiles apply` again") {
		t.Fatalf("failure = %+v", failure)
	}
	backup, err := os.ReadFile(failure.Backup)
	if err != nil || string(backup) != string(appstoretest.Content(f.adHoc.Name, f.adHoc.UUID)) {
		t.Fatalf("backup = %q, %v", backup, err)
	}
	if !strings.Contains(result.Err().Error(), "deleted profile \"TixNow AdHoc\" (id "+f.adHoc.ID) {
		t.Fatalf("error must say what was deleted: %v", result.Err())
	}

	// Retrying converges: the profile is now missing and gets created.
	retry, err := f.planner(t).Plan(ctx, f.desired)
	if err != nil {
		t.Fatal(err)
	}
	// The name comes back from the local backup: manual signing projects
	// reference profiles by name.
	if profile := findProfile(t, findTarget(t, retry, app), config.ProfileAdHoc); profile.Action != Create || profile.Name != "TixNow AdHoc" {
		t.Fatalf("retry plan = %+v", profile)
	}
	if err := (Applier{API: f.server.Client(t)}).Apply(ctx, retry).Err(); err != nil {
		t.Fatal(err)
	}
	if profiles := f.server.ProfilesOf(app); profiles[0].Name != "TixNow AdHoc" || profiles[0].Type != appstore.ProfileIOSAdHoc {
		t.Fatalf("profiles after retry = %+v", profiles)
	}
}

func TestCreateFailureBeforeDeletingKeepsTheOldProfile(t *testing.T) {
	f := newTixNow(t, "")
	ctx := context.Background()
	plan, err := f.planner(t).Plan(ctx, f.desired)
	if err != nil {
		t.Fatal(err)
	}
	f.server.Fail["POST /v1/profiles"] = appstoretest.Failure{Status: http.StatusForbidden}
	result := Applier{API: f.server.Client(t)}.Apply(ctx, plan)
	if len(result.Failures) == 0 {
		t.Fatal("expected failures")
	}
	for _, failure := range result.Failures {
		if failure.Deleted != nil {
			t.Fatalf("nothing may be deleted when creation is refused: %+v", failure)
		}
	}
	if len(f.server.ProfilesOf(app)) != 3 {
		t.Fatal("old profiles must still exist")
	}
}

func TestCapabilityFailureSkipsThatAppIDOnly(t *testing.T) {
	f := newTixNow(t, "")
	ctx := context.Background()
	plan, err := f.planner(t).Plan(ctx, f.desired)
	if err != nil {
		t.Fatal(err)
	}
	// The second capability of the app fails; the App Clip still proceeds.
	f.server.Fail["POST /v1/bundleIdCapabilities"] = appstoretest.Failure{Status: http.StatusConflict, Times: 1, After: 1}
	result := Applier{API: f.server.Client(t)}.Apply(ctx, plan)
	if len(result.Failures) != 1 || result.Failures[0].Target != app || !strings.Contains(result.Failures[0].Step, "Sign in with Apple") {
		t.Fatalf("failures = %v", result.Failures)
	}
	for _, profile := range f.server.ProfilesOf(app) {
		if profile.ID != f.adHoc.ID && profile.ID != f.store.ID && profile.Type != appstore.ProfileIOSDevelopment {
			t.Fatalf("app profiles must be untouched, found %+v", profile)
		}
	}
	if len(f.server.ProfilesOf(clip)) != 3 {
		t.Fatal("the App Clip must still be completed")
	}
}

func TestDryRunMakesNoWrites(t *testing.T) {
	f := newTixNow(t, "")
	ctx := context.Background()
	plan, err := f.planner(t).Plan(ctx, f.desired)
	if err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(t.TempDir(), "install")
	var out bytes.Buffer
	result := Applier{API: f.server.Client(t), Out: &out, DryRun: true, InstallDirs: []string{install}}.Apply(ctx, plan)
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}
	if writes := f.server.Writes(); len(writes) != 0 {
		t.Fatalf("dry run wrote: %v", writes)
	}
	for _, dir := range []string{f.desired.Directory, install} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("dry run created %s", dir)
		}
	}
	for _, want := range []string{"+ register App ID " + clip, "+ enable Associated Domains on " + app, `~ recreate ad-hoc profile "TixNow AdHoc"`, "would include 1 certificate(s) and 2 device(s)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestMatchStrategyRegeneratesThroughMatch(t *testing.T) {
	f := newTixNow(t, config.ProvisioningMatch)
	ctx := context.Background()
	plan, err := f.planner(t).Plan(ctx, f.desired)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	match := func(_ context.Context, kind string, bundleIDs []string) error {
		calls = append(calls, kind+":"+strings.Join(bundleIDs, ","))
		return nil
	}
	if err := (Applier{API: f.server.Client(t), Match: match}).Apply(ctx, plan).Err(); err != nil {
		t.Fatal(err)
	}
	all := app + "," + notify + "," + clip
	if !slices.Equal(calls, []string{"development:" + all, "ad-hoc:" + all, "app-store:" + all}) {
		t.Fatalf("match calls = %v", calls)
	}
	for _, write := range f.server.Writes() {
		if strings.Contains(write, "/v1/profiles") {
			t.Fatalf("match owns the profiles; the API must not touch them: %v", f.server.Writes())
		}
	}
}

func TestXcodeStrategyOnlyFixesAppIDs(t *testing.T) {
	f := newTixNow(t, config.ProvisioningNone)
	ctx := context.Background()
	plan, err := f.planner(t).Plan(ctx, f.desired)
	if err != nil {
		t.Fatal(err)
	}
	if profile := findProfile(t, findTarget(t, plan, app), config.ProfileAdHoc); profile.Action != Xcode {
		t.Fatalf("ad-hoc = %+v", profile)
	}
	result := Applier{API: f.server.Client(t)}.Apply(ctx, plan)
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}
	if result.Enabled != 3 || result.Created+result.Recreated != 0 || len(result.Notes) == 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestBlockedWithoutCertificate(t *testing.T) {
	server := appstoretest.New(t)
	server.AddBundle("com.example.app")
	desired := FromConfig(t.TempDir(), config.Config{
		App:          config.App{Name: "Example"},
		IOS:          config.IOS{BundleID: "com.example.app", Signing: config.SigningManual},
		Environments: map[string]*config.Environment{"production": {Name: "production", Destinations: []string{config.DestinationTestFlight}}},
	}, "")
	plan, err := Planner{API: server.Client(t), Now: func() time.Time { return server.Now }}.Plan(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked() != 1 {
		t.Fatalf("blocked = %d", plan.Blocked())
	}
	result := Applier{API: server.Client(t)}.Apply(context.Background(), plan)
	if result.Err() == nil || !strings.Contains(result.Err().Error(), "no valid distribution certificate in the team") {
		t.Fatalf("err = %v", result.Err())
	}
}

func TestTeamMismatchWarns(t *testing.T) {
	f := newTixNow(t, "")
	f.server.Bundle(app).SeedID = "ZZZZZ99999"
	plan, err := f.planner(t).Plan(context.Background(), f.desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "belongs to team ZZZZZ99999 but ios.team_id is ABCDE12345") {
		t.Fatalf("warnings = %v", plan.Warnings)
	}
}
