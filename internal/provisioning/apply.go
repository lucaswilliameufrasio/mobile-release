package provisioning

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/appstore"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

// MatchFunc regenerates the profiles of kind for bundleIDs with fastlane
// match (strategy "match").
type MatchFunc func(ctx context.Context, kind string, bundleIDs []string) error

// Applier makes App Store Connect match a plan.
type Applier struct {
	API API
	Out io.Writer
	// DryRun prints every step without calling the API or writing files.
	DryRun bool
	Match  MatchFunc
	// InstallDirs receive a copy of every profile as <uuid>.mobileprovision
	// (the macOS provisioning profiles folders). Empty: not installed.
	InstallDirs []string
}

// Failure is a step that did not complete.
type Failure struct {
	Target string
	Step   string
	Err    error
	// Deleted is set when a profile was deleted and its replacement could
	// not be created: the app has no profile of that type until a retry.
	Deleted *appstore.Profile
	// Backup is the local copy of the deleted profile.
	Backup string
}

func (f Failure) String() string {
	text := fmt.Sprintf("%s: %s: %v", f.Target, f.Step, f.Err)
	if f.Deleted != nil {
		text += fmt.Sprintf("\n      deleted profile %q (id %s, uuid %s); a copy is in %s", f.Deleted.Name, f.Deleted.ID, f.Deleted.UUID, f.Backup)
	}
	return text
}

// Result summarizes an apply.
type Result struct {
	Registered int
	Enabled    int
	Created    int
	Recreated  int
	Installed  []string
	Notes      []string
	Failures   []Failure
}

// Err is non-nil when some step failed.
func (r Result) Err() error {
	if len(r.Failures) == 0 {
		return nil
	}
	lines := make([]string, 0, len(r.Failures))
	for _, failure := range r.Failures {
		lines = append(lines, "  - "+failure.String())
	}
	return errors.New("some steps failed:\n" + strings.Join(lines, "\n"))
}

func (a Applier) printf(format string, args ...any) {
	if a.Out != nil {
		fmt.Fprintf(a.Out, format, args...)
	}
}

// Apply runs the plan. It continues with the other targets when one fails
// and reports every failure.
func (a Applier) Apply(ctx context.Context, plan Plan) Result {
	var result Result
	matchKinds := map[string]bool{}
	for index := range plan.Targets {
		target := &plan.Targets[index]
		if !a.prepareTarget(ctx, target, &result) {
			continue
		}
		for _, profile := range target.Profiles {
			switch profile.Action {
			case Blocked:
				result.Failures = append(result.Failures, Failure{Target: target.Target.BundleID, Step: profile.Kind + " profile", Err: errors.New(strings.Join(profile.Reasons, "; "))})
			case Xcode:
				result.Notes = append(result.Notes, fmt.Sprintf("%s %s: left to Xcode automatic signing (-allowProvisioningUpdates regenerates it on the next archive)", target.Target.BundleID, profile.Kind))
			case Create, Recreate:
				if plan.Desired.Strategy == config.ProvisioningMatch {
					matchKinds[profile.Kind] = true
					continue
				}
				a.replaceProfile(ctx, plan, *target, profile, &result)
			case Keep:
				if plan.Desired.Strategy == config.ProvisioningAPI && profile.Current != nil {
					a.store(plan.Desired, target.Target.BundleID, profile.Kind, *profile.Current, nil, &result)
				}
			}
		}
	}
	a.runMatch(ctx, plan, matchKinds, &result)
	return result
}

// prepareTarget registers the App ID and enables capabilities; profiles are
// only touched when this succeeds.
func (a Applier) prepareTarget(ctx context.Context, target *TargetPlan, result *Result) bool {
	id := target.Target.BundleID
	if target.Bundle == nil {
		name := sanitizeName(target.Target.Name)
		a.printf("  + register App ID %s (%q)\n", id, name)
		if a.DryRun {
			target.Bundle = &appstore.BundleID{ID: "(new)", Identifier: id}
		} else {
			bundle, err := a.API.CreateBundleID(ctx, id, name)
			if err != nil {
				result.Failures = append(result.Failures, Failure{Target: id, Step: "register App ID", Err: err})
				return false
			}
			target.Bundle = &bundle
		}
		result.Registered++
	}
	for _, capability := range target.Enable {
		a.printf("  + enable %s on %s\n", capability.Label, id)
		if a.DryRun {
			result.Enabled++
			continue
		}
		if _, err := a.API.EnableCapability(ctx, target.Bundle.ID, capability.Type, capability.Settings); err != nil {
			result.Failures = append(result.Failures, Failure{Target: id, Step: "enable " + capability.Label + " (profiles of this App ID were not regenerated; run apply again)", Err: err})
			return false
		}
		result.Enabled++
	}
	return true
}

// replaceProfile creates the new profile before deleting the old one. When
// the API refuses a second profile with the same name, the old one is
// backed up locally, deleted and recreated; if that creation fails the
// failure says exactly what was deleted.
func (a Applier) replaceProfile(ctx context.Context, plan Plan, target TargetPlan, profile ProfilePlan, result *Result) {
	id := target.Target.BundleID
	verb := "create"
	if profile.Current != nil {
		verb = "recreate"
	}
	a.printf("  ~ %s %s profile %q for %s (%s)\n", verb, profile.Kind, profile.Name, id, strings.Join(profile.Reasons, ", "))
	request := appstore.CreateProfileRequest{
		Name:           profile.Name,
		Type:           profileType(profile.Kind),
		BundleID:       target.Bundle.ID,
		CertificateIDs: certificateIDs(plan.Certificates[certificateGroup(profile.Kind)]),
	}
	if needsDevices(profile.Kind) {
		request.DeviceIDs = deviceIDs(plan.Devices)
	}
	if a.DryRun {
		a.printf("    (would include %d certificate(s) and %d device(s), download it to %s)\n", len(request.CertificateIDs), len(request.DeviceIDs), filepath.Join(plan.Desired.Directory, FileName(id, profile.Kind)))
		a.count(profile, result)
		return
	}
	backup := ""
	if profile.Current != nil && len(profile.Current.Content) > 0 {
		path, err := a.backup(plan.Desired, id, profile.Kind, *profile.Current)
		if err != nil {
			result.Failures = append(result.Failures, Failure{Target: id, Step: profile.Kind + " profile backup (nothing was deleted)", Err: err})
			return
		}
		backup = path
	}

	created, err := a.API.CreateProfile(ctx, request)
	deleted := false
	if err != nil && profile.Current != nil && appstore.IsConflict(err) {
		// The name is taken by the profile being replaced.
		if deleteErr := a.API.DeleteProfile(ctx, profile.Current.ID); deleteErr != nil {
			result.Failures = append(result.Failures, Failure{Target: id, Step: profile.Kind + " profile: delete the old profile (nothing changed)", Err: deleteErr})
			return
		}
		deleted = true
		created, err = a.API.CreateProfile(ctx, request)
	}
	if err != nil {
		failure := Failure{Target: id, Step: profile.Kind + " profile: create (nothing was deleted)", Err: err}
		if deleted {
			failure.Step = profile.Kind + " profile: create after deleting the old one (run `mobile-release profiles apply` again to create it)"
			failure.Deleted, failure.Backup = profile.Current, backup
		}
		result.Failures = append(result.Failures, failure)
		return
	}
	if profile.Current != nil && !deleted {
		if err := a.API.DeleteProfile(ctx, profile.Current.ID); err != nil {
			result.Notes = append(result.Notes, fmt.Sprintf("%s: the new %s profile is ready, but the old one (%s, id %s) could not be deleted: %v", id, profile.Kind, profile.Current.Name, profile.Current.ID, err))
		}
	}
	a.count(profile, result)
	a.store(plan.Desired, id, profile.Kind, created, profile.Current, result)
}

func (a Applier) count(profile ProfilePlan, result *Result) {
	if profile.Current == nil {
		result.Created++
	} else {
		result.Recreated++
	}
}

func (a Applier) backup(desired Desired, bundleID, kind string, profile appstore.Profile) (string, error) {
	dir := filepath.Join(desired.Directory, ".backup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, backupName(bundleID, kind, firstNonEmpty(profile.UUID, profile.ID)))
	return path, os.WriteFile(path, profile.Content, 0o600)
}

func backupName(bundleID, kind, uuid string) string {
	return strings.TrimSuffix(FileName(bundleID, kind), ".mobileprovision") + "_" + uuid + ".mobileprovision"
}

// store writes the profile to the project directory and installs it,
// removing the replaced profile's installed copy.
func (a Applier) store(desired Desired, bundleID, kind string, profile appstore.Profile, replaced *appstore.Profile, result *Result) {
	if len(profile.Content) == 0 || a.DryRun {
		return
	}
	if err := os.MkdirAll(desired.Directory, 0o700); err != nil {
		result.Failures = append(result.Failures, Failure{Target: bundleID, Step: kind + " profile: write", Err: err})
		return
	}
	path := filepath.Join(desired.Directory, FileName(bundleID, kind))
	if current, err := os.ReadFile(path); err != nil || string(current) != string(profile.Content) {
		if err := os.WriteFile(path, profile.Content, 0o600); err != nil {
			result.Failures = append(result.Failures, Failure{Target: bundleID, Step: kind + " profile: write", Err: err})
			return
		}
		result.Installed = append(result.Installed, path)
	}
	if !desired.Install {
		return
	}
	for _, dir := range a.InstallDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			result.Failures = append(result.Failures, Failure{Target: bundleID, Step: kind + " profile: install", Err: err})
			return
		}
		if replaced != nil && replaced.UUID != "" && replaced.UUID != profile.UUID {
			_ = os.Remove(filepath.Join(dir, replaced.UUID+".mobileprovision"))
		}
		target := filepath.Join(dir, firstNonEmpty(profile.UUID, profile.ID)+".mobileprovision")
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if err := os.WriteFile(target, profile.Content, 0o600); err != nil {
			result.Failures = append(result.Failures, Failure{Target: bundleID, Step: kind + " profile: install", Err: err})
			return
		}
		result.Installed = append(result.Installed, target)
	}
}

// runMatch regenerates profiles with fastlane match, one run per kind for
// every bundle id of the app, so the match repository stays the source of
// truth for the build.
func (a Applier) runMatch(ctx context.Context, plan Plan, kinds map[string]bool, result *Result) {
	if len(kinds) == 0 {
		return
	}
	var bundleIDs []string
	for _, target := range plan.Targets {
		bundleIDs = append(bundleIDs, target.Target.BundleID)
	}
	for _, kind := range plan.Desired.Kinds {
		if !kinds[kind] {
			continue
		}
		a.printf("  ~ fastlane match %s --force for %s\n", matchType(kind), strings.Join(bundleIDs, ", "))
		if a.Match == nil && a.DryRun {
			result.Recreated++
			continue
		}
		if a.Match == nil {
			result.Failures = append(result.Failures, Failure{Target: plan.Desired.Name, Step: "match " + matchType(kind), Err: errors.New("fastlane match is not available here")})
			continue
		}
		if err := a.Match(ctx, kind, bundleIDs); err != nil {
			result.Failures = append(result.Failures, Failure{Target: plan.Desired.Name, Step: "match " + matchType(kind), Err: err})
			continue
		}
		result.Recreated++
	}
}
