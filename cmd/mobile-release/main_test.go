package main

import (
	"strings"
	"testing"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/credentials"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/project"
)

func TestLegacyArgs(t *testing.T) {
	cases := map[string]string{
		"-platform android internal": "deploy -config release.toml -platform android -env internal -yes",
		"-config x.toml qa":          "qa -config x.toml -platform all",
		"deploy -env staging":        "deploy -env staging",
		"":                           "",
	}
	for input, want := range cases {
		args := strings.Fields(input)
		if got := strings.Join(legacyArgs(args), " "); got != want {
			t.Errorf("%q: got %q want %q", input, got, want)
		}
	}
}

func TestProposalIsValidAndMinimal(t *testing.T) {
	c := proposal(project.Info{
		Type: config.TypeFlutter, Name: "shop", AndroidPackage: "com.example.shop", IOSBundleID: "com.example.shop",
		IOSWorkspace: "ios/Runner.xcworkspace", IOSScheme: "Runner", HasAndroid: true, HasIOS: true,
	})
	data, err := config.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, unwanted := range []string{"workspace", "ota", "release_status", "groups"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("proposal should leave %q to defaults:\n%s", unwanted, text)
		}
	}
	parsed, err := config.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	parsed.ApplyDefaults()
	if err := parsed.Validate(); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}

	androidOnly := proposal(project.Info{Type: config.TypeNative, Name: "app", AndroidPackage: "com.example", HasAndroid: true})
	production := androidOnly.Environments["production"]
	if androidOnly.IOSEnabled() || strings.Join(production.Destinations, ",") != "play" {
		t.Fatalf("android-only proposal = %+v %v", androidOnly.IOS, production.Destinations)
	}
}

func TestDescribeMissingNamesCIVariables(t *testing.T) {
	keystore, _ := credentials.Lookup(credentials.AndroidKeystore)
	alias, _ := credentials.Lookup(credentials.AndroidKeyAlias)
	text := describeMissing([]credentials.Spec{keystore, alias})
	for _, want := range []string{
		"MOBILE_RELEASE_ANDROID_KEYSTORE_PATH or MOBILE_RELEASE_ANDROID_KEYSTORE_BASE64",
		"Android key alias (CI: MOBILE_RELEASE_ANDROID_KEY_ALIAS)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}
