package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/project"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/tui"
)

const configHeader = `# mobile-release configuration. Commit this file: it holds no secrets.
# Credentials are asked by ` + "`mobile-release setup`" + ` and kept in your secret
# stores (or MOBILE_RELEASE_* variables in CI).

`

func runInit(_ context.Context, args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	var common commonFlags
	common.register(flags)
	force := flags.Bool("force", false, "overwrite an existing release.toml")
	if err := flags.Parse(args); err != nil {
		return err
	}
	a, err := newApp(common)
	if err != nil {
		return err
	}
	if _, err := os.Stat(a.configPath); err == nil && !*force {
		return fmt.Errorf("%s already exists (use -force to overwrite)", a.relative(a.configPath))
	}
	if err := a.writeConfig(); err != nil {
		return err
	}
	a.printf("\nWrote %s.\nNext:\n  mobile-release setup    # store the credentials once\n  mobile-release doctor   # check tools and credentials\n  mobile-release deploy   # build and publish\n", a.relative(a.configPath))
	return nil
}

// writeConfig detects the project, asks (when interactive) and writes
// release.toml with only the chosen values.
func (a *app) writeConfig() error {
	info := project.Inspect(a.root)
	c := proposal(info)
	if a.interactive {
		if err := wizard(&c, info); err != nil {
			return err
		}
	} else if info.Type == "" {
		return errors.New("could not detect the project type; run init in a terminal")
	}
	// Write only what was chosen; defaults stay implicit.
	data, err := config.Marshal(c)
	if err != nil {
		return err
	}
	check, err := config.Parse(data)
	if err != nil {
		return err
	}
	check.ApplyDefaults()
	if err := check.Validate(); err != nil {
		return err
	}
	return os.WriteFile(a.configPath, append([]byte(configHeader), data...), 0o644)
}

// proposal turns what was detected into a starting configuration.
func proposal(info project.Info) config.Config {
	c := config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Project:       config.Project{Type: info.Type, Preset: info.Preset},
		App:           config.App{Name: info.Name},
		Android:       config.Android{PackageName: info.AndroidPackage},
		IOS: config.IOS{
			BundleID:  info.IOSBundleID,
			Workspace: info.IOSWorkspace,
			Project:   info.IOSProject,
			Scheme:    info.IOSScheme,
		},
		Environments: map[string]*config.Environment{},
	}
	if info.Type == config.TypeFlutter {
		// Defaults already cover Runner.xcworkspace.
		c.IOS.Workspace, c.IOS.Scheme = "", ""
	}
	if !info.HasAndroid {
		c.Android.Enabled = boolPtr(false)
	}
	if !info.HasIOS {
		c.IOS.Enabled = boolPtr(false)
	}
	env := &config.Environment{Name: "production", BuildNumber: config.BuildNumberStore, Destinations: []string{}}
	if info.HasAndroid {
		env.Destinations = append(env.Destinations, config.DestinationPlay)
	}
	if info.HasIOS {
		env.Destinations = append(env.Destinations, config.DestinationTestFlight)
	}
	c.Environments["production"] = env
	return c
}

var teamID = regexp.MustCompile(`^[A-Z0-9]{10}$`)

func wizard(c *config.Config, info project.Info) error {
	types := []tui.Option{
		{Label: "Flutter", Value: config.TypeFlutter},
		{Label: "Expo (React Native)", Value: config.TypeExpo},
		{Label: "Kotlin Multiplatform", Value: config.TypeKMP},
		{Label: "Native (Gradle + Xcode)", Hint: "including jirau projects", Value: config.TypeNative},
	}
	kind, err := tui.Select("Project type", detectedHint(info.Type), types, firstNonEmpty(info.Type, config.TypeNative))
	if err != nil {
		return err
	}
	c.Project.Type = kind
	if kind != config.TypeNative {
		c.Project.Preset = ""
	}
	if c.App.Name, err = tui.Input("App name", tui.InputOptions{Initial: c.App.Name, Validate: tui.Required}); err != nil {
		return err
	}
	if c.AndroidEnabled() {
		if c.Android.PackageName, err = tui.Input("Android application id", tui.InputOptions{Initial: c.Android.PackageName, Placeholder: "com.example.app"}); err != nil {
			return err
		}
		signing, err := tui.Select("Android release signing", "", []tui.Option{
			{Label: "mobile-release injects the upload keystore", Hint: "recommended; asked once by setup", Value: config.AndroidSigningInjected},
			{Label: "The Gradle project signs by itself", Hint: "e.g. key.properties", Value: config.AndroidSigningProject},
		}, config.AndroidSigningInjected)
		if err != nil {
			return err
		}
		c.Android.Signing = signing
	}
	if c.IOSEnabled() {
		if c.IOS.BundleID, err = tui.Input("iOS bundle id", tui.InputOptions{Initial: c.IOS.BundleID, Placeholder: "com.example.app"}); err != nil {
			return err
		}
		signing, err := tui.Select("iOS signing", "", []tui.Option{
			{Label: "Automatic", Hint: "Xcode manages profiles with the App Store Connect key", Value: config.SigningAutomatic},
			{Label: "fastlane match", Hint: "certificates in a private git repository", Value: config.SigningMatch},
			{Label: "Manual", Hint: "profiles already installed on this Mac", Value: config.SigningManual},
		}, config.SigningAutomatic)
		if err != nil {
			return err
		}
		c.IOS.Signing = signing
		if c.IOS.TeamID, err = tui.Input("Apple team id (optional)", tui.InputOptions{
			Initial: c.IOS.TeamID,
			Help:    "10 characters, from developer.apple.com > Membership. Required for match.",
			Validate: func(value string) error {
				if value == "" && signing != config.SigningMatch {
					return nil
				}
				if !teamID.MatchString(value) {
					return errors.New("Use the 10-character team id (letters and digits).")
				}
				return nil
			},
		}); err != nil {
			return err
		}
	}

	c.Environments = map[string]*config.Environment{}
	suggestions := []string{"staging", "production"}
	for {
		suggestion := ""
		for _, name := range suggestions {
			if _, taken := c.Environments[name]; !taken {
				suggestion = name
				break
			}
		}
		env, err := environmentWizard(*c, info, suggestion)
		if err != nil {
			return err
		}
		c.Environments[env.Name] = env
		more, err := tui.Confirm("Add another environment?", len(c.Environments) < 2)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
}

var environmentName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func environmentWizard(c config.Config, info project.Info, suggestion string) (*config.Environment, error) {
	name, err := tui.Input("Environment name", tui.InputOptions{
		Initial: suggestion,
		Help:    "Lowercase letters, digits and dashes, e.g. staging or production.",
		Validate: func(value string) error {
			if !environmentName.MatchString(value) {
				return errors.New("Use lowercase letters, digits and dashes.")
			}
			if _, taken := c.Environments[value]; taken {
				return errors.New("That environment already exists.")
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	env := &config.Environment{Name: name}

	if len(info.EnvFiles) > 0 {
		options := []tui.Option{{Label: "None", Value: ""}}
		initial := ""
		for _, file := range info.EnvFiles {
			options = append(options, tui.Option{Label: file, Value: file})
			if (name == "production" && strings.Contains(file, "pr")) || (name != "production" && file == ".env") {
				initial = file
			}
		}
		if env.EnvFile, err = tui.Select("Configuration file for "+name, "Passed to the build (Dart defines for Flutter, environment for others).", options, initial); err != nil {
			return nil, err
		}
	}

	destinations := []tui.Option{{Label: "Firebase App Distribution", Hint: "testers", Value: config.DestinationFirebase}}
	if c.AndroidEnabled() {
		destinations = append(destinations, tui.Option{Label: "Google Play", Hint: "a track, internal by default", Value: config.DestinationPlay})
	}
	if c.IOSEnabled() {
		destinations = append(destinations,
			tui.Option{Label: "TestFlight", Value: config.DestinationTestFlight},
			tui.Option{Label: "App Store review", Hint: "submits for review", Value: config.DestinationAppStore})
	}
	if c.Project.Type == config.TypeExpo {
		destinations = append(destinations, tui.Option{Label: "OTA update (EAS)", Value: config.DestinationOTA})
	}
	destinations = append(destinations, tui.Option{Label: "Artifacts only", Hint: "build into dist/", Value: config.DestinationArtifacts})
	initial := []string{config.DestinationFirebase}
	if name == "production" {
		initial = []string{config.DestinationPlay, config.DestinationTestFlight}
	}
	if env.Destinations, err = tui.MultiSelect("Where does "+name+" publish?", "", destinations, initial); err != nil {
		return nil, err
	}

	defaultNumber := config.BuildNumberTimestamp
	if env.Has(config.DestinationPlay) || env.Has(config.DestinationTestFlight) || env.Has(config.DestinationAppStore) {
		defaultNumber = config.BuildNumberStore
	}
	if env.BuildNumber, err = tui.Select("Build number", "", []tui.Option{
		{Label: "Timestamp", Hint: "UTC yyDDDHHMM, no store access", Value: config.BuildNumberTimestamp},
		{Label: "Store + 1", Hint: "asks Google Play / TestFlight", Value: config.BuildNumberStore},
		{Label: "Keep the project's number", Value: config.BuildNumberProject},
	}, defaultNumber); err != nil {
		return nil, err
	}

	if env.Has(config.DestinationFirebase) {
		help := "Firebase console > Project settings > Your apps (1:123:android:abc...)."
		if c.AndroidEnabled() {
			if env.Firebase.AndroidAppID, err = tui.Input("Firebase Android app id for "+name, tui.InputOptions{Help: help, Validate: tui.Required}); err != nil {
				return nil, err
			}
		}
		if c.IOSEnabled() {
			if env.Firebase.IOSAppID, err = tui.Input("Firebase iOS app id for "+name, tui.InputOptions{Help: help, Validate: tui.Required}); err != nil {
				return nil, err
			}
		}
		groups, err := tui.Input("Firebase tester groups", tui.InputOptions{Initial: "internal-testers", Help: "Comma separated."})
		if err != nil {
			return nil, err
		}
		for _, group := range strings.Split(groups, ",") {
			if strings.TrimSpace(group) != "" {
				env.Firebase.Groups = append(env.Firebase.Groups, strings.TrimSpace(group))
			}
		}
	}
	return env, nil
}

func detectedHint(kind string) string {
	if kind == "" {
		return "Could not detect it automatically."
	}
	return "Detected: " + kind
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
