package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/credentials"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/provisioning"
)

type tool struct {
	name string
	hint string
}

// requiredTools lists the executables a release of the project needs.
func requiredTools(c config.Config, platforms credentials.Platforms) []tool {
	tools := []tool{
		{"git", "install git"},
		{"bundle", "Ruby >= 3.2 (4.0 recommended) with Bundler, e.g. `mise use -g ruby@4.0`"},
	}
	switch c.Project.Type {
	case config.TypeFlutter:
		tools = append(tools, tool{"flutter", "install Flutter (fvm or mise) and put it on PATH"})
	case config.TypeExpo:
		tools = append(tools, tool{"node", "install Node.js"}, tool{"npx", "comes with npm"})
	}
	if platforms.Android && c.AndroidEnabled() {
		tools = append(tools, tool{"java", "install a JDK (17+), e.g. `mise use -g java@21`"})
	}
	if platforms.IOS && c.IOSEnabled() && runtime.GOOS == "darwin" {
		tools = append(tools, tool{"xcodebuild", "install Xcode and run `xcode-select --install`"})
		if c.IOS.XcodeGenSpec != "" {
			tools = append(tools, tool{"xcodegen", "`brew install xcodegen`"})
		}
		if c.Project.Preset == config.PresetJirau {
			tools = append(tools, tool{"cargo", "install Rust with rustup"})
		}
	}
	return tools
}

func runDoctor(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	var common commonFlags
	common.register(flags)
	envName := flags.String("env", "", "only check this environment")
	if err := flags.Parse(args); err != nil {
		return err
	}
	a, err := newApp(common)
	if err != nil {
		return err
	}
	if *envName != "" {
		// Load exec stores first so the report is printed once.
		if err := a.loadConfig(); err == nil {
			if _, err := a.config.Environment(*envName); err != nil {
				return err
			}
			if err := a.wrapExecStores(*envName, append([]string{"doctor"}, args...)); err != nil {
				return err
			}
		}
	}
	ok := true
	mark := func(good bool) string {
		if good {
			return "✓"
		}
		ok = false
		return "✗"
	}

	a.printf("Project\n")
	if err := a.loadConfig(); err != nil {
		a.printf("  ✗ %v\n", err)
		return fmt.Errorf("fix release.toml first")
	}
	a.printf("  ✓ %s (%s%s), app id %s\n", a.relative(a.configPath), a.config.Project.Type, presetSuffix(a.config), a.config.App.ID)

	all := credentials.Platforms{Android: a.config.AndroidEnabled(), IOS: a.config.IOSEnabled()}
	a.printf("\nTools\n")
	for _, tool := range requiredTools(a.config, all) {
		_, err := lookPath(tool.name)
		line := fmt.Sprintf("  %s %s", mark(err == nil), tool.name)
		if err != nil {
			line += "  → " + tool.hint
		}
		a.printf("%s\n", line)
	}
	if all.IOS && runtime.GOOS != "darwin" {
		a.printf("  · iOS builds need macOS; only Android can be released from this machine\n")
	}

	a.printf("\nCredential stores (priority order)\n")
	for _, store := range a.stores {
		state := "available"
		if !store.Available() {
			state = "not available here"
		}
		a.printf("  · %s: %s\n", store.Name(), state)
	}
	for _, exec := range a.execs {
		state := "used when an environment is chosen"
		if !exec.Available() {
			state = "command not found"
		} else if exec.Applied() {
			state = "active"
		}
		a.printf("  · %s (exec): %s\n", exec.Name, state)
	}

	names := a.config.EnvironmentNames()
	if *envName != "" {
		if _, err := a.config.Environment(*envName); err != nil {
			return err
		}
		names = []string{*envName}
	}
	for _, name := range names {
		env := a.config.Environments[name]
		a.printf("\nEnvironment %s → %s\n", name, strings.Join(env.Destinations, ", "))
		if env.EnvFile != "" {
			_, err := os.Stat(filepath.Join(a.root, env.EnvFile))
			a.printf("  %s env file %s\n", mark(err == nil), env.EnvFile)
		}
		required, result, err := a.resolve(ctx, env, all)
		if err != nil {
			a.printf("  ✗ %v\n", err)
			ok = false
			continue
		}
		if len(required) == 0 {
			a.printf("  ✓ no credentials needed\n")
		}
		printSources(a, required, result)
		if len(result.Missing) > 0 {
			ok = false
		}
	}
	if !a.checkProfiles(ctx, *envName) {
		ok = false
	}
	if !ok {
		a.printf("\nSome checks failed. `mobile-release setup` asks for missing credentials.\n")
		return fmt.Errorf("doctor found problems")
	}
	a.printf("\nEverything looks ready.\n")
	return nil
}

func presetSuffix(c config.Config) string {
	if c.Project.Preset == "" {
		return ""
	}
	return ", preset " + c.Project.Preset
}

// checkProfiles reports App IDs and provisioning profiles that need work:
// expiring within provisioning.RenewWithinDays (warning), and invalid,
// expired or missing profiles (failure, unless Xcode manages them). It
// never fails on network problems or when no API key is configured.
func (a *app) checkProfiles(ctx context.Context, envName string) bool {
	if !a.config.IOSEnabled() || a.config.IOS.BundleID == "" {
		return true
	}
	a.printf("\nProvisioning profiles (App Store Connect)\n")
	set, err := a.resolveAppStoreKey(ctx, envName, false)
	if err != nil {
		a.printf("  · skipped: no App Store Connect API key (`mobile-release setup` asks for it)\n")
		return true
	}
	client, err := a.provisioningClient(set, map[string]provisioning.API{})
	if err != nil {
		a.printf("  ! %v\n", err)
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	plan, err := provisioning.Planner{API: client, Now: profilesNow}.Plan(ctx, provisioning.FromConfig(a.root, a.config, envName))
	if err != nil {
		a.printf("  ! could not check App Store Connect: %v\n", err)
		return true
	}
	ok := true
	for _, warning := range plan.Warnings {
		a.printf("  ! %s\n", warning)
	}
	for _, target := range plan.Targets {
		if target.Bundle == nil {
			a.printf("  ✗ %s: App ID not registered\n", target.Target.BundleID)
			ok = false
		}
		for _, capability := range target.Enable {
			a.printf("  ! %s: %s is not enabled\n", target.Target.BundleID, capability.Label)
		}
		for _, profile := range target.Profiles {
			line := fmt.Sprintf("%s %s", target.Target.BundleID, profile.Kind)
			switch {
			case profile.Action == provisioning.Keep:
				a.printf("  ✓ %s expires in %d days\n", line, profile.ExpiresIn)
			case onlyExpiring(profile) || profile.Action == provisioning.Xcode:
				a.printf("  ! %s: %s\n", line, strings.Join(profile.Reasons, "; "))
			default:
				a.printf("  ✗ %s: %s\n", line, strings.Join(profile.Reasons, "; "))
				ok = false
			}
		}
	}
	if plan.Changes() > 0 {
		a.printf("  → `mobile-release profiles plan` shows the details; `mobile-release profiles apply` fixes them\n")
	}
	return ok
}

// onlyExpiring is a profile that still works but expires soon.
func onlyExpiring(profile provisioning.ProfilePlan) bool {
	if profile.Current == nil || len(profile.Reasons) == 0 {
		return false
	}
	for _, reason := range profile.Reasons {
		if !strings.HasPrefix(reason, "expires in ") {
			return false
		}
	}
	return true
}
