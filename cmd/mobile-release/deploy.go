package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/credentials"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/fastlane"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/release"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/runner"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/tui"
)

type deployFlags struct {
	common     commonFlags
	env        string
	platform   string
	dryRun     bool
	notes      string
	allowDirty bool
}

func parseDeployFlags(name string, args []string) (deployFlags, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	var parsed deployFlags
	parsed.common.register(flags)
	flags.StringVar(&parsed.env, "env", "", "environment from release.toml")
	flags.StringVar(&parsed.platform, "platform", "", "all, android or ios")
	flags.BoolVar(&parsed.dryRun, "dry-run", false, "print the commands without running them")
	flags.StringVar(&parsed.notes, "notes", "", "release notes (default: environment and commit)")
	flags.BoolVar(&parsed.allowDirty, "allow-dirty", false, "allow uncommitted changes")
	return parsed, flags.Parse(args)
}

func (d deployFlags) args(command string) []string {
	args := []string{command, "-dir", d.common.dir, "-config", d.common.config, "-env", d.env, "-platform", d.platform}
	if d.common.yes {
		args = append(args, "-yes")
	}
	if d.dryRun {
		args = append(args, "-dry-run")
	}
	if d.allowDirty {
		args = append(args, "-allow-dirty")
	}
	if d.notes != "" {
		args = append(args, "-notes", d.notes)
	}
	return args
}

func runDeploy(ctx context.Context, args []string) error {
	parsed, err := parseDeployFlags("deploy", args)
	if err != nil {
		return err
	}
	a, err := newApp(parsed.common)
	if err != nil {
		return err
	}
	if err := a.loadConfig(); err != nil {
		return err
	}
	env, err := a.chooseEnvironment(parsed.env, "release")
	if err != nil {
		return err
	}
	platforms, platformName, err := a.choosePlatforms(parsed.platform)
	if err != nil {
		return err
	}
	parsed.env, parsed.platform = env.Name, platformName
	if err := a.wrapExecStores(env.Name, parsed.args("deploy")); err != nil {
		return err
	}

	problems := a.preflight(ctx, env, platforms, parsed.allowDirty || parsed.dryRun)
	required, result, err := a.resolve(ctx, env, platforms)
	if err != nil {
		return err
	}
	if len(result.Missing) > 0 && a.interactive && len(problems) == 0 {
		a.printf("\n%d credential(s) missing for %s:\n%s\n", len(result.Missing), env.Name, describeMissing(result.Missing))
		configure, err := tui.Confirm("Configure them now?", true)
		if err != nil {
			return err
		}
		if configure {
			if err := a.setupSpecs(ctx, env, result.Missing); err != nil {
				return err
			}
			required, result, err = a.resolve(ctx, env, platforms)
			if err != nil {
				return err
			}
		}
	}
	if len(result.Missing) > 0 && !parsed.dryRun {
		problems = append(problems, "missing credentials (run `mobile-release setup`):\n"+describeMissing(result.Missing))
	}

	a.printSummary(env, platformName, required, result)
	if len(problems) > 0 {
		return errors.New("cannot release:\n  - " + strings.Join(problems, "\n  - "))
	}
	if !parsed.dryRun && !parsed.common.yes {
		if !a.interactive {
			return errors.New("refusing to publish without confirmation: pass -yes in non-interactive runs")
		}
		var confirmed bool
		if env.NeedsConfirmation() {
			confirmed, err = tui.TypeToConfirm("Publish "+env.Name+" to "+strings.Join(env.Destinations, ", ")+"?", env.Name)
		} else {
			confirmed, err = tui.Confirm("Publish "+env.Name+"?", false)
		}
		if err != nil {
			return err
		}
		if !confirmed {
			return tui.ErrCancelled
		}
	}

	service, cleanup, err := a.service(result.Set, parsed.dryRun)
	if err != nil {
		return err
	}
	defer cleanup()
	outcome, err := service.Deploy(ctx, release.Request{Environment: env.Name, Android: platforms.Android, IOS: platforms.IOS, ReleaseNotes: parsed.notes})
	a.printArtifacts(outcome)
	if err != nil {
		return err
	}
	if parsed.dryRun {
		a.printf("\nDry run finished; nothing was built or published.\n")
	} else {
		a.printf("\n%s released to %s.\n", env.Name, strings.Join(env.Destinations, ", "))
	}
	return nil
}

func runQA(ctx context.Context, args []string) error {
	parsed, err := parseDeployFlags("qa", args)
	if err != nil {
		return err
	}
	a, err := newApp(parsed.common)
	if err != nil {
		return err
	}
	if err := a.loadConfig(); err != nil {
		return err
	}
	platforms, _, err := a.choosePlatforms(parsed.platform)
	if err != nil {
		return err
	}
	if platforms.IOS && runtime.GOOS != "darwin" && !parsed.dryRun {
		a.printf("Skipping iOS: simulator builds need macOS.\n")
		platforms.IOS = false
	}
	service, cleanup, err := a.service(credentials.NewSet(), parsed.dryRun)
	if err != nil {
		return err
	}
	defer cleanup()
	outcome, err := service.QA(ctx, parsed.env, platforms.Android, platforms.IOS)
	a.printArtifacts(outcome)
	return err
}

// preflight lists blocking problems for a release.
func (a *app) preflight(ctx context.Context, env *config.Environment, platforms credentials.Platforms, allowDirty bool) []string {
	var problems []string
	if platforms.IOS && runtime.GOOS != "darwin" {
		problems = append(problems, "iOS releases need macOS with Xcode (use -platform android here)")
	}
	if env.EnvFile != "" {
		if _, err := os.Stat(a.root + "/" + env.EnvFile); err != nil {
			problems = append(problems, fmt.Sprintf("env_file %s not found", env.EnvFile))
		}
	}
	if !allowDirty {
		status, err := runner.Exec{Root: a.root}.Output(ctx, runner.Command{Name: "git", Args: []string{"status", "--porcelain", "--untracked-files=no"}})
		if err == nil && status != "" {
			problems = append(problems, "uncommitted changes: release from a commit (or pass -allow-dirty)")
		}
	}
	for _, tool := range requiredTools(a.config, platforms) {
		if _, err := lookPath(tool.name); err != nil {
			problems = append(problems, fmt.Sprintf("%s not found (%s)", tool.name, tool.hint))
		}
	}
	return problems
}

func (a *app) printSummary(env *config.Environment, platformName string, required []credentials.Spec, result credentials.Result) {
	a.printf("\nRelease plan\n")
	a.printf("  environment:  %s\n", env.Name)
	a.printf("  destinations: %s\n", strings.Join(env.Destinations, ", "))
	a.printf("  platforms:    %s\n", platformName)
	a.printf("  build number: %s\n", describeBuildNumber(env.BuildNumber))
	if env.EnvFile != "" {
		a.printf("  env file:     %s\n", env.EnvFile)
	}
	if len(required) > 0 {
		a.printf("  credentials (values are never shown):\n")
		printSources(a, required, result)
	}
}

func describeBuildNumber(strategy string) string {
	switch strategy {
	case config.BuildNumberTimestamp:
		return "timestamp (UTC yyDDDHHMM)"
	case config.BuildNumberStore:
		return "latest in the store + 1"
	case config.BuildNumberProject:
		return "kept from the project"
	}
	return strategy
}

func (a *app) printArtifacts(result release.Result) {
	if len(result.Artifacts) == 0 {
		return
	}
	a.printf("\nArtifacts\n")
	for _, artifact := range result.Artifacts {
		a.printf("  %s %s  %s\n", artifact.Platform, artifact.Kind, artifact.Path)
	}
}

// service wires the release service with materialized credentials.
func (a *app) service(set *credentials.Set, dryRun bool) (release.Service, func(), error) {
	secretsDir, err := os.MkdirTemp("", "mobile-release-secrets-")
	if err != nil {
		return release.Service{}, func() {}, err
	}
	cleanup := func() { os.RemoveAll(secretsDir) }
	if err := set.Materialize(secretsDir); err != nil {
		cleanup()
		return release.Service{}, func() {}, err
	}
	var commands runner.Runner = runner.Exec{Root: a.root, Out: os.Stdout, Err: os.Stderr, Echo: true}
	if dryRun {
		commands = runner.Dry{Out: os.Stdout}
	}
	cache, err := fastlane.DefaultCacheDir()
	if err != nil {
		cleanup()
		return release.Service{}, func() {}, err
	}
	lanes := &fastlane.Lanes{Runner: commands, ProjectDir: a.root, CacheDir: cache}
	return release.Service{
		Root:        a.root,
		Config:      a.config,
		Runner:      commands,
		Lanes:       lanes,
		Credentials: set,
		Out:         os.Stdout,
		DryRun:      dryRun,
		Git:         runner.Exec{Root: a.root},
	}, cleanup, nil
}
