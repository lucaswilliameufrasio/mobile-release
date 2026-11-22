package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/appstore"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/credentials"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/fastlane"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/provisioning"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/runner"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/tui"
)

const profilesUsage = `mobile-release profiles — keep iOS App IDs, capabilities and provisioning profiles in sync

Usage:
  mobile-release profiles plan  [flags] [project dirs...]   show what differs (read-only)
  mobile-release profiles apply [flags] [project dirs...]   fix it, after confirmation

Without project dirs the current project (-dir) is used. -apps <file> reads
project dirs from a file (one per line, # comments, relative to the file).

Flags:
  -apps <file>   list of project directories
  -env <name>    only the profile types of this environment (and its credentials)
  -config <file> release.toml name inside each project (default release.toml)
  -dry-run       apply: print every step without changing anything
  -yes           apply: do not ask (CI); plan/apply never prompt with it
  -check         plan: exit with an error when something must change (CI drift check)
`

// newProvisioningAPI builds the App Store Connect client; tests replace it.
var newProvisioningAPI = func(key appstore.Key) (provisioning.API, error) {
	return appstore.New(key)
}

// profilesNow is the clock of the planner; tests replace it.
var profilesNow = time.Now

type profilesFlags struct {
	common commonFlags
	apps   string
	env    string
	dryRun bool
	check  bool
}

// profileProject is one app of a run.
type profileProject struct {
	app     *app
	set     *credentials.Set
	api     provisioning.API
	desired provisioning.Desired
	plan    provisioning.Plan
	err     error
}

func runProfiles(ctx context.Context, args []string) error {
	return runProfilesTo(ctx, args, os.Stdout)
}

func runProfilesTo(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || (args[0] != "plan" && args[0] != "apply") {
		fmt.Fprint(out, profilesUsage)
		if len(args) == 0 || args[0] == "-h" || args[0] == "help" {
			return nil
		}
		return fmt.Errorf("unknown profiles command %q", args[0])
	}
	command := args[0]
	flags := flag.NewFlagSet("profiles "+command, flag.ContinueOnError)
	flags.SetOutput(out)
	var parsed profilesFlags
	parsed.common.register(flags)
	flags.StringVar(&parsed.apps, "apps", "", "file listing project directories, one per line")
	flags.StringVar(&parsed.env, "env", "", "only this environment's profile types and credentials")
	if command == "apply" {
		flags.BoolVar(&parsed.dryRun, "dry-run", false, "print the steps without changing anything")
	} else {
		flags.BoolVar(&parsed.check, "check", false, "exit with an error when something must change")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	dirs, err := projectDirs(parsed.common.dir, parsed.apps, flags.Args())
	if err != nil {
		return err
	}

	projects := make([]*profileProject, 0, len(dirs))
	clients := map[string]provisioning.API{}
	for index, dir := range dirs {
		project := &profileProject{}
		projects = append(projects, project)
		common := parsed.common
		common.dir = dir
		a, err := newApp(common)
		if err != nil {
			project.err = err
			continue
		}
		a.out = out
		project.app = a
		if err := a.loadConfig(); err != nil {
			project.err = err
			continue
		}
		if index == 0 && parsed.env != "" {
			// Exec stores inject credentials for one environment: re-run
			// this command under them before resolving anything.
			if err := a.wrapExecStores(parsed.env, append([]string{"profiles"}, args...)); err != nil {
				return err
			}
		}
		if parsed.env != "" {
			if _, err := a.config.Environment(parsed.env); err != nil {
				project.err = err
				continue
			}
		}
		if !a.config.IOSEnabled() || a.config.IOS.BundleID == "" {
			project.err = errSkipped
			continue
		}
		project.desired = provisioning.FromConfig(a.root, a.config, parsed.env)
		project.set, project.err = a.resolveAppStoreKey(ctx, parsed.env, len(dirs) == 1)
		if project.err != nil {
			continue
		}
		project.api, project.err = a.provisioningClient(project.set, clients)
	}

	failed := 0
	changes, blocked := 0, 0
	for _, project := range projects {
		if project.err == nil {
			planner := provisioning.Planner{API: project.api, Now: profilesNow}
			project.plan, project.err = planner.Plan(ctx, project.desired)
		}
		printProjectHeader(out, project)
		switch {
		case errors.Is(project.err, errSkipped):
			fmt.Fprintf(out, "  · skipped: iOS is disabled or ios.bundle_id is not set\n\n")
			continue
		case project.err != nil:
			fmt.Fprintf(out, "  ✗ %v\n\n", project.err)
			failed++
			continue
		}
		provisioning.Render(out, project.plan)
		fmt.Fprintln(out)
		changes += project.plan.Changes()
		blocked += project.plan.Blocked()
	}
	fmt.Fprintf(out, "%d app(s): %d change(s), %d blocked, %d with errors.\n", len(projects), changes, blocked, failed)

	if command == "plan" {
		if failed > 0 {
			return fmt.Errorf("%d app(s) could not be checked", failed)
		}
		if parsed.check && changes+blocked > 0 {
			return fmt.Errorf("App Store Connect differs from release.toml: run `mobile-release profiles apply`")
		}
		if changes > 0 {
			fmt.Fprintf(out, "Nothing was changed. Run `mobile-release profiles apply` to apply.\n")
		}
		return nil
	}
	return applyProfiles(ctx, out, parsed, projects, changes, failed)
}

var errSkipped = errors.New("skipped")

func applyProfiles(ctx context.Context, out io.Writer, parsed profilesFlags, projects []*profileProject, changes, failed int) error {
	if changes > 0 && !parsed.dryRun && !parsed.common.yes {
		if !tui.Interactive() {
			return errors.New("refusing to change App Store Connect without confirmation: pass -yes in non-interactive runs")
		}
		confirmed, err := tui.Confirm(fmt.Sprintf("Apply %d change(s) to App Store Connect?", changes), false)
		if err != nil {
			return err
		}
		if !confirmed {
			return tui.ErrCancelled
		}
	}
	var problems []string
	for _, project := range projects {
		if project.err != nil {
			continue
		}
		fmt.Fprintf(out, "==> %s\n", projectLabel(project))
		match, cleanup := project.matchFunc(ctx, parsed.dryRun, out)
		applier := provisioning.Applier{API: project.api, Out: out, DryRun: parsed.dryRun, Match: match, InstallDirs: installDirs()}
		result := applier.Apply(ctx, project.plan)
		cleanup()
		printApplyResult(out, project, result, parsed.dryRun)
		if err := result.Err(); err != nil {
			problems = append(problems, projectLabel(project)+": "+err.Error())
		}
	}
	if parsed.dryRun {
		fmt.Fprintf(out, "\nDry run: nothing was changed in App Store Connect and no file was written.\n")
	}
	if failed > 0 {
		problems = append(problems, fmt.Sprintf("%d app(s) could not be checked (see above)", failed))
	}
	if len(problems) > 0 {
		return errors.New("profiles apply did not finish:\n" + strings.Join(problems, "\n") + "\nFix the cause and run the same command again: it only redoes what is still missing.")
	}
	if !parsed.dryRun {
		fmt.Fprintf(out, "\nDone. `mobile-release profiles plan` should now report no changes.\n")
	}
	return nil
}

func printApplyResult(out io.Writer, project *profileProject, result provisioning.Result, dryRun bool) {
	verb := ""
	if dryRun {
		verb = "would be "
	}
	fmt.Fprintf(out, "  %d App ID(s) %sregistered, %d capability(ies) %senabled, %d profile(s) %screated, %d %srecreated\n",
		result.Registered, verb, result.Enabled, verb, result.Created, verb, result.Recreated, verb)
	for _, note := range result.Notes {
		fmt.Fprintf(out, "  · %s\n", note)
	}
	if len(result.Installed) > 0 {
		fmt.Fprintf(out, "  Profiles written:\n")
		for _, path := range result.Installed {
			fmt.Fprintf(out, "    %s\n", path)
		}
	}
	if !dryRun && project.desired.Strategy == config.ProvisioningAPI {
		if len(installDirs()) == 0 {
			fmt.Fprintf(out, "  · profiles are in %s; copy them to the Mac that builds (or run apply there to install them)\n", project.app.relative(project.desired.Directory))
		}
	}
	for _, failure := range result.Failures {
		fmt.Fprintf(out, "  ✗ %s\n", failure.String())
	}
}

func printProjectHeader(out io.Writer, project *profileProject) {
	if project.err == nil {
		return
	}
	fmt.Fprintf(out, "%s\n", projectLabel(project))
}

func projectLabel(project *profileProject) string {
	if project.app == nil {
		return "(project)"
	}
	if project.app.config.App.Name != "" {
		return project.app.config.App.Name + " (" + project.app.root + ")"
	}
	return project.app.root
}

// projectDirs combines positional dirs and the -apps file; with neither,
// the -dir project is used.
func projectDirs(defaultDir, listFile string, positional []string) ([]string, error) {
	dirs := append([]string{}, positional...)
	if listFile != "" {
		file, err := os.Open(listFile)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		base := filepath.Dir(listFile)
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if index := strings.Index(line, "#"); index >= 0 {
				line = strings.TrimSpace(line[:index])
			}
			if line == "" {
				continue
			}
			line = expandHome(line)
			if !filepath.IsAbs(line) {
				line = filepath.Join(base, line)
			}
			dirs = append(dirs, line)
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		if len(dirs) == len(positional) {
			return nil, fmt.Errorf("%s lists no project", listFile)
		}
	}
	if len(dirs) == 0 {
		dirs = []string{defaultDir}
	}
	return dirs, nil
}

// resolveAppStoreKey finds the API key: environment-scoped for -env,
// otherwise shared, falling back to the first environment that has it.
func (a *app) resolveAppStoreKey(ctx context.Context, envName string, offerSetup bool) (*credentials.Set, error) {
	specs := append(credentials.AppStoreAPI(), credentials.ProfileLanes(a.config)...)
	resolver := credentials.Resolver{Stores: a.stores}
	scopes := []string{envName}
	if envName == "" {
		scopes = append([]string{""}, a.config.EnvironmentNames()...)
	}
	var result credentials.Result
	for _, scope := range scopes {
		var err error
		result, err = resolver.Resolve(ctx, specs, a.config.App.ID, scope)
		if err != nil {
			return nil, err
		}
		if !missingAPIKey(result.Missing) {
			break
		}
	}
	if missingAPIKey(result.Missing) && offerSetup && a.interactive {
		a.printf("\nThe App Store Connect API key is missing for %s:\n%s\n", a.config.App.ID, describeMissing(result.Missing))
		configure, err := tui.Confirm("Configure it now?", true)
		if err != nil {
			return nil, err
		}
		if configure {
			if err := a.setupShared(ctx, result.Missing); err != nil {
				return nil, err
			}
			return a.resolveAppStoreKey(ctx, envName, false)
		}
	}
	if missingAPIKey(result.Missing) {
		return nil, fmt.Errorf("missing credentials (run `mobile-release setup`):\n%s", describeMissing(result.Missing))
	}
	return result.Set, nil
}

func missingAPIKey(missing []credentials.Spec) bool {
	for _, spec := range missing {
		if spec.ID == credentials.AppStoreKeyID || spec.ID == credentials.AppStoreIssuerID || spec.ID == credentials.AppStorePrivateKey {
			return true
		}
	}
	return false
}

// setupShared asks for credentials shared by every environment.
func (a *app) setupShared(ctx context.Context, missing []credentials.Spec) error {
	for index, spec := range missing {
		a.printf("\n[%d/%d] %s\n", index+1, len(missing), spec.Label)
		if spec.Help != "" {
			a.printf("      %s\n", spec.Help)
		}
		if err := a.provideValue(ctx, spec, credentials.Key{App: a.config.App.ID, ID: spec.ID}); err != nil {
			return err
		}
	}
	return nil
}

// provisioningClient reuses one client (and token) per API key across apps.
func (a *app) provisioningClient(set *credentials.Set, clients map[string]provisioning.API) (provisioning.API, error) {
	keyID, err := set.Bytes(credentials.AppStoreKeyID)
	if err != nil {
		return nil, err
	}
	issuerID, err := set.Bytes(credentials.AppStoreIssuerID)
	if err != nil {
		return nil, err
	}
	private, err := set.Bytes(credentials.AppStorePrivateKey)
	if err != nil {
		return nil, err
	}
	cacheKey := strings.TrimSpace(string(issuerID)) + "/" + strings.TrimSpace(string(keyID))
	if client, ok := clients[cacheKey]; ok {
		return client, nil
	}
	client, err := newProvisioningAPI(appstore.Key{KeyID: string(keyID), IssuerID: string(issuerID), PrivateKey: private})
	if err != nil {
		return nil, err
	}
	clients[cacheKey] = client
	return client, nil
}

// matchFunc runs the embedded profiles lane for strategy "match".
func (p *profileProject) matchFunc(ctx context.Context, dryRun bool, out io.Writer) (provisioning.MatchFunc, func()) {
	if p.desired.Strategy != config.ProvisioningMatch {
		return nil, func() {}
	}
	if missing := missingSpecs(p.set, credentials.ProfileLanes(p.app.config)); len(missing) > 0 {
		return func(context.Context, string, []string) error {
			return fmt.Errorf("missing credentials for fastlane match (run `mobile-release setup`):\n%s", describeMissing(missing))
		}, func() {}
	}
	if runtime.GOOS != "darwin" && !dryRun {
		return func(context.Context, string, []string) error {
			return errors.New("fastlane match needs macOS (it installs into the keychain): run `mobile-release profiles apply` on a Mac or a macOS CI runner; App IDs and capabilities are already done")
		}, func() {}
	}
	secrets, err := os.MkdirTemp("", "mobile-release-secrets-")
	if err != nil {
		return func(context.Context, string, []string) error { return err }, func() {}
	}
	cleanup := func() { os.RemoveAll(secrets) }
	if err := p.set.Materialize(secrets); err != nil {
		cleanup()
		return func(context.Context, string, []string) error { return err }, func() {}
	}
	var commands runner.Runner = runner.Exec{Root: p.app.root, Out: out, Err: os.Stderr, Echo: true}
	if dryRun {
		commands = runner.Dry{Out: out}
	}
	cache, err := fastlane.DefaultCacheDir()
	if err != nil {
		cleanup()
		return func(context.Context, string, []string) error { return err }, func() {}
	}
	lanes := &fastlane.Lanes{Runner: commands, ProjectDir: p.app.root, CacheDir: cache}
	return func(ctx context.Context, kind string, bundleIDs []string) error {
		env := append(p.set.LaneEnv(),
			"MOBILE_RELEASE_MATCH_TYPE="+provisioning.MatchType(kind),
			"MOBILE_RELEASE_IOS_BUNDLE_IDS="+strings.Join(bundleIDs, ","),
			"MOBILE_RELEASE_IOS_TEAM_ID="+p.app.config.IOS.TeamID,
		)
		return lanes.Run(ctx, "ios", "profiles", env)
	}, cleanup
}

func missingSpecs(set *credentials.Set, specs []credentials.Spec) []credentials.Spec {
	var missing []credentials.Spec
	for _, spec := range specs {
		if !set.Has(spec.ID) {
			missing = append(missing, spec)
		}
	}
	return missing
}

// installDirs are the folders Xcode and fastlane read profiles from; only
// macOS has them.
var installDirs = func() []string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, "Library", "MobileDevice", "Provisioning Profiles"),
		// Xcode 16 and later read this folder.
		filepath.Join(home, "Library", "Developer", "Xcode", "UserData", "Provisioning Profiles"),
	}
}
