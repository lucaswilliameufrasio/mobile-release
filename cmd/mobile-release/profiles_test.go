package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/appstore"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/appstore/appstoretest"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/provisioning"
)

const tixnowToml = `schema_version = 2

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

[environments.production]
destinations = ["play", "testflight"]
`

const shopToml = `schema_version = 2

[project]
type = "native"

[app]
name = "Shop"

[android]
enabled = false

[ios]
bundle_id = "com.example.shop"
team_id = "ABCDE12345"
signing = "manual"

[environments.production]
destinations = ["testflight"]
`

const androidOnlyToml = `schema_version = 2

[project]
type = "native"

[app]
name = "Droid"

[android]
package_name = "com.example.droid"

[ios]
enabled = false

[environments.production]
destinations = ["play"]
`

// profilesWorkspace sets up three projects, env credentials and a fake App
// Store Connect, and returns the -apps file.
func profilesWorkspace(t *testing.T) (*appstoretest.Server, string, []string) {
	t.Helper()
	server := appstoretest.New(t)
	server.Now = time.Now().UTC().Truncate(time.Second)
	distribution := server.AddCertificate("DISTRIBUTION", 300)
	tixnow := server.AddBundle("br.com.tixnow.app")
	server.AddProfile(tixnow, "TixNow AppStore", appstore.ProfileIOSAppStore, appstore.ProfileStateActive, 200, []string{distribution.ID}, nil)
	shop := server.AddBundle("com.example.shop")
	server.AddProfile(shop, "Shop AppStore", appstore.ProfileIOSAppStore, appstore.ProfileStateActive, 200, []string{distribution.ID}, nil)

	workspace := t.TempDir()
	var roots []string
	for name, content := range map[string]string{"tixnow": tixnowToml, "shop": shopToml, "droid": androidOnlyToml} {
		root := filepath.Join(workspace, name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "release.toml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root)
	}
	list := filepath.Join(workspace, "apps.txt")
	if err := os.WriteFile(list, []byte("# every app we maintain\ntixnow\nshop   # store app\n\ndroid\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	userConfig := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(userConfig, []byte(`order = ["env"]`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOBILE_RELEASE_USER_CONFIG", userConfig)
	t.Setenv("MOBILE_RELEASE_APP_STORE_KEY_ID", server.Key.KeyID)
	t.Setenv("MOBILE_RELEASE_APP_STORE_ISSUER_ID", server.Key.IssuerID)
	t.Setenv("MOBILE_RELEASE_APP_STORE_PRIVATE_KEY_BASE64", base64.StdEncoding.EncodeToString(server.Key.PrivateKey))

	previousAPI, previousNow, previousInstall := newProvisioningAPI, profilesNow, installDirs
	newProvisioningAPI = func(key appstore.Key) (provisioning.API, error) {
		if key.KeyID != server.Key.KeyID || string(key.PrivateKey) != string(server.Key.PrivateKey) {
			t.Fatalf("unexpected key %s", key.KeyID)
		}
		clientsCreated++
		if clientsCreated > 1 {
			t.Fatal("one client per API key is expected across apps")
		}
		return server.Client(t), nil
	}
	profilesNow = func() time.Time { return server.Now }
	installDirs = func() []string { return nil }
	t.Cleanup(func() { newProvisioningAPI, profilesNow, installDirs = previousAPI, previousNow, previousInstall })
	return server, list, roots
}

// clientsCreated counts App Store Connect clients made by one run.
var clientsCreated int

func runProfilesCapture(t *testing.T, args ...string) (string, error) {
	t.Helper()
	clientsCreated = 0
	var out bytes.Buffer
	err := runProfilesTo(context.Background(), args, &out)
	text := out.String()
	if strings.Contains(text, "PRIVATE KEY") || strings.Contains(text, base64.StdEncoding.EncodeToString([]byte("-----BEGIN"))[:10]) {
		t.Fatalf("output leaks the API key:\n%s", text)
	}
	return text, err
}

func TestProfilesPlanAndApplyAcrossApps(t *testing.T) {
	server, list, _ := profilesWorkspace(t)
	workspace := filepath.Dir(list)

	out, err := runProfilesCapture(t, "plan", "-apps", list)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"TixNow (", "+ enable Associated Domains (ASSOCIATED_DOMAINS)", `~ app-store profile "TixNow AppStore": capabilities change`,
		"Shop (", "Up to date.", "skipped: iOS is disabled",
		"3 app(s): 2 change(s), 0 blocked, 0 with errors.", "Nothing was changed.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output lacks %q:\n%s", want, out)
		}
	}
	if writes := server.Writes(); len(writes) != 0 {
		t.Fatalf("plan wrote: %v", writes)
	}
	if _, err := runProfilesCapture(t, "plan", "-check", "-apps", list); err == nil {
		t.Fatal("-check must fail while something differs")
	}

	out, err = runProfilesCapture(t, "apply", "-dry-run", "-apps", list)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if writes := server.Writes(); len(writes) != 0 {
		t.Fatalf("dry run wrote: %v", writes)
	}
	if _, err := os.Stat(filepath.Join(workspace, "tixnow", "dist")); !os.IsNotExist(err) {
		t.Fatal("dry run must not write profiles")
	}
	if !strings.Contains(out, "Dry run: nothing was changed") || !strings.Contains(out, "would be recreated") {
		t.Fatalf("dry run output:\n%s", out)
	}

	if _, err := runProfilesCapture(t, "apply", "-apps", list); err == nil || !strings.Contains(err.Error(), "pass -yes") {
		t.Fatalf("non-interactive apply must refuse without -yes, got %v", err)
	}
	if writes := server.Writes(); len(writes) != 0 {
		t.Fatalf("refused apply wrote: %v", writes)
	}

	out, err = runProfilesCapture(t, "apply", "-yes", "-apps", list)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(server.Writes()[0], "POST /v1/bundleIdCapabilities") {
		t.Fatalf("writes = %v", server.Writes())
	}
	for _, file := range []string{
		filepath.Join(workspace, "tixnow", "dist", "profiles", "br.com.tixnow.app_app-store.mobileprovision"),
		filepath.Join(workspace, "shop", "dist", "profiles", "com.example.shop_app-store.mobileprovision"),
	} {
		if _, err := os.Stat(file); err != nil {
			t.Errorf("%s: %v\n%s", file, err, out)
		}
	}
	if !strings.Contains(out, "copy them to the Mac that builds") {
		t.Errorf("Linux runs must say where the profiles are:\n%s", out)
	}

	if out, err := runProfilesCapture(t, "plan", "-check", "-apps", list); err != nil {
		t.Fatalf("after apply the plan must be clean: %v\n%s", err, out)
	}
}

func TestProfilesSingleProjectAndPositionalDirs(t *testing.T) {
	_, _, roots := profilesWorkspace(t)
	var shop string
	for _, root := range roots {
		if strings.HasSuffix(root, "shop") {
			shop = root
		}
	}
	out, err := runProfilesCapture(t, "plan", "-dir", shop)
	if err != nil || !strings.Contains(out, "1 app(s): 0 change(s)") {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = runProfilesCapture(t, "plan", roots[0], roots[1])
	if err != nil || !strings.Contains(out, "2 app(s)") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestProfilesReportMissingCredentials(t *testing.T) {
	_, list, _ := profilesWorkspace(t)
	t.Setenv("MOBILE_RELEASE_APP_STORE_PRIVATE_KEY_BASE64", "")
	out, err := runProfilesCapture(t, "plan", "-apps", list)
	if err == nil || !strings.Contains(out, "MOBILE_RELEASE_APP_STORE_PRIVATE_KEY_PATH or MOBILE_RELEASE_APP_STORE_PRIVATE_KEY_BASE64") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestProjectDirs(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, "apps.txt")
	if err := os.WriteFile(list, []byte("a\n/abs/b # comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs, err := projectDirs(".", list, []string{"c"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(dirs, ",") != "c,"+filepath.Join(dir, "a")+",/abs/b" {
		t.Fatalf("dirs = %v", dirs)
	}
	if dirs, _ := projectDirs("here", "", nil); len(dirs) != 1 || dirs[0] != "here" {
		t.Fatalf("default = %v", dirs)
	}
	empty := filepath.Join(dir, "empty.txt")
	os.WriteFile(empty, []byte("# nothing\n"), 0o644)
	if _, err := projectDirs(".", empty, nil); err == nil {
		t.Fatal("an empty list must be an error")
	}
}

func doctorProfiles(t *testing.T, root string) (string, bool) {
	t.Helper()
	a, err := newApp(commonFlags{config: "release.toml", dir: root, yes: true})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.out = &out
	if err := a.loadConfig(); err != nil {
		t.Fatal(err)
	}
	clientsCreated = 0
	ok := a.checkProfiles(context.Background(), "")
	return out.String(), ok
}

func TestDoctorWarnsAboutExpiringAndFailsOnInvalidProfiles(t *testing.T) {
	server, list, _ := profilesWorkspace(t)
	shop := filepath.Join(filepath.Dir(list), "shop")

	out, ok := doctorProfiles(t, shop)
	if !ok || !strings.Contains(out, "✓ com.example.shop app-store expires in 200 days") {
		t.Fatalf("healthy: ok=%v\n%s", ok, out)
	}

	for _, profile := range server.Profiles {
		if profile.Name == "Shop AppStore" {
			profile.Expires = server.Now.AddDate(0, 0, 12)
		}
	}
	out, ok = doctorProfiles(t, shop)
	if !ok || !strings.Contains(out, "! com.example.shop app-store: expires in 12 days") {
		t.Fatalf("expiring must only warn: ok=%v\n%s", ok, out)
	}

	for _, profile := range server.Profiles {
		if profile.Name == "Shop AppStore" {
			profile.State = appstore.ProfileStateInvalid
		}
	}
	out, ok = doctorProfiles(t, shop)
	if ok || !strings.Contains(out, "✗ com.example.shop app-store: invalid") || !strings.Contains(out, "mobile-release profiles apply") {
		t.Fatalf("invalid must fail: ok=%v\n%s", ok, out)
	}
	if writes := server.Writes(); len(writes) != 0 {
		t.Fatalf("doctor wrote: %v", writes)
	}

	t.Setenv("MOBILE_RELEASE_APP_STORE_KEY_ID", "")
	out, ok = doctorProfiles(t, shop)
	if !ok || !strings.Contains(out, "skipped: no App Store Connect API key") {
		t.Fatalf("no key: ok=%v\n%s", ok, out)
	}
}

func TestSetupAsksForTheAPIKeyOnlyWhenProvisioningIsDeclared(t *testing.T) {
	parse := func(content string) bool {
		t.Helper()
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "release.toml"), []byte(content), 0o644)
		a, err := newApp(commonFlags{config: "release.toml", dir: dir, yes: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.loadConfig(); err != nil {
			t.Fatal(err)
		}
		return declaresProvisioning(a.config)
	}
	if !parse(tixnowToml) {
		t.Fatal("capabilities declare provisioning")
	}
	automatic := strings.Replace(shopToml, `signing = "manual"`, `signing = "automatic"`, 1)
	if parse(automatic) {
		t.Fatal("an automatic-signing app without declarations must not be asked for more")
	}
	if !parse(strings.Replace(automatic, `destinations = ["testflight"]`, `destinations = ["testflight"]`+"\n"+`ios_profiles = ["development"]`, 1)) {
		t.Fatal("ios_profiles declares provisioning")
	}
	if parse(androidOnlyToml) {
		t.Fatal("android-only apps have no profiles")
	}
}
