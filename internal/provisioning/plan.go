package provisioning

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/appstore"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

// RenewWithinDays renews profiles that expire sooner than this.
const RenewWithinDays = 30

// Action is what apply does with a profile.
type Action string

const (
	Keep     Action = "keep"
	Create   Action = "create"
	Recreate Action = "recreate"
	// Blocked profiles cannot be created (no certificate or devices).
	Blocked Action = "blocked"
	// Xcode profiles are left to Xcode automatic signing (strategy none).
	Xcode Action = "xcode"
)

// ProfilePlan is the state and action of one profile.
type ProfilePlan struct {
	Kind string // development, ad-hoc, app-store
	// Current is the profile in use (nil when missing).
	Current *appstore.Profile
	// Others are further profiles of the same type, left alone.
	Others  int
	Action  Action
	Reasons []string
	// Name is the name used when (re)creating.
	Name string
	// ExpiresIn is the number of whole days left (Current only).
	ExpiresIn int
}

// Changes reports whether apply would change the profile.
func (p ProfilePlan) Changes() bool {
	return p.Action == Create || p.Action == Recreate
}

// TargetPlan is the state of one bundle id.
type TargetPlan struct {
	Target Target
	// Bundle is nil when the App ID is not registered yet.
	Bundle   *appstore.BundleID
	Enable   []Capability
	Enabled  []Capability
	Profiles []ProfilePlan
}

// Plan is the difference for one app.
type Plan struct {
	Desired  Desired
	Targets  []TargetPlan
	Warnings []string
	// Certificates holds the valid certificates by group (development,
	// distribution), used to create profiles.
	Certificates map[string][]appstore.Certificate
	Devices      []appstore.Device
}

// Changes counts the writes apply would make.
func (p Plan) Changes() int {
	count := 0
	for _, target := range p.Targets {
		if target.Bundle == nil {
			count++
		}
		count += len(target.Enable)
		for _, profile := range target.Profiles {
			if profile.Changes() {
				count++
			}
		}
	}
	return count
}

// Blocked counts profiles that cannot be fixed by apply.
func (p Plan) Blocked() int {
	count := 0
	for _, target := range p.Targets {
		for _, profile := range target.Profiles {
			if profile.Action == Blocked {
				count++
			}
		}
	}
	return count
}

// Planner reads App Store Connect; it never writes.
type Planner struct {
	API API
	Now func() time.Time
}

func (p Planner) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Plan compares desired with App Store Connect.
func (p Planner) Plan(ctx context.Context, desired Desired) (Plan, error) {
	plan := Plan{Desired: desired, Certificates: map[string][]appstore.Certificate{}}
	if len(desired.Targets) == 0 {
		plan.Warnings = append(plan.Warnings, "ios.bundle_id is not set: nothing to manage")
		return plan, nil
	}
	if err := p.loadAccount(ctx, &plan); err != nil {
		return plan, err
	}
	for _, target := range desired.Targets {
		targetPlan, err := p.planTarget(ctx, &plan, target)
		if err != nil {
			return plan, fmt.Errorf("%s: %w", target.BundleID, err)
		}
		plan.Targets = append(plan.Targets, targetPlan)
	}
	return plan, nil
}

// loadAccount fetches the valid certificates and the devices once per app.
func (p Planner) loadAccount(ctx context.Context, plan *Plan) error {
	now := p.now()
	groups := map[string]bool{}
	devices := false
	for _, kind := range plan.Desired.Kinds {
		groups[certificateGroup(kind)] = true
		devices = devices || needsDevices(kind)
	}
	for _, kind := range []string{config.ProfileDevelopment, config.ProfileAppStore} {
		group := certificateGroup(kind)
		if !groups[group] {
			continue
		}
		certificates, err := p.API.Certificates(ctx, certificateTypes(kind))
		if err != nil {
			return err
		}
		for _, certificate := range certificates {
			if certificate.Expires.IsZero() || certificate.Expires.After(now) {
				plan.Certificates[group] = append(plan.Certificates[group], certificate)
			}
		}
	}
	if devices {
		list, err := p.API.Devices(ctx)
		if err != nil {
			return err
		}
		plan.Devices = list
	}
	return nil
}

func (p Planner) planTarget(ctx context.Context, plan *Plan, target Target) (TargetPlan, error) {
	result := TargetPlan{Target: target}
	bundle, err := p.API.FindBundleID(ctx, target.BundleID)
	if err != nil {
		return result, err
	}
	result.Bundle = bundle
	var profiles []appstore.Profile
	if bundle == nil {
		result.Enable = target.Capabilities
	} else {
		if plan.Desired.TeamID != "" && bundle.SeedID != "" && bundle.SeedID != plan.Desired.TeamID {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s belongs to team %s but ios.team_id is %s: is the API key from another team?", target.BundleID, bundle.SeedID, plan.Desired.TeamID))
		}
		enabled, err := p.API.Capabilities(ctx, bundle.ID)
		if err != nil {
			return result, err
		}
		have := map[string]bool{}
		for _, capability := range enabled {
			have[capability.Type] = true
		}
		for _, capability := range target.Capabilities {
			if have[capability.Type] {
				result.Enabled = append(result.Enabled, capability)
			} else {
				result.Enable = append(result.Enable, capability)
			}
		}
		if profiles, err = p.API.Profiles(ctx, bundle.ID); err != nil {
			return result, err
		}
	}
	for _, kind := range plan.Desired.Kinds {
		profilePlan, err := p.planProfile(ctx, plan, result, kind, profiles)
		if err != nil {
			return result, err
		}
		result.Profiles = append(result.Profiles, profilePlan)
	}
	return result, nil
}

func (p Planner) planProfile(ctx context.Context, plan *Plan, target TargetPlan, kind string, all []appstore.Profile) (ProfilePlan, error) {
	result := ProfilePlan{Kind: kind, Name: firstNonEmpty(previousName(plan.Desired, target.Target.BundleID, kind), defaultProfileName(target.Target.BundleID, kind))}
	wanted := profileType(kind)
	var candidates []appstore.Profile
	for _, profile := range all {
		if profile.Type == wanted {
			candidates = append(candidates, profile)
		}
	}
	// Prefer an active profile, then the one that expires last.
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if (left.State == appstore.ProfileStateActive) != (right.State == appstore.ProfileStateActive) {
			return left.State == appstore.ProfileStateActive
		}
		return left.Expires.After(right.Expires)
	})
	if len(candidates) > 0 {
		current := candidates[0]
		result.Current = &current
		result.Name = current.Name
		result.Others = len(candidates) - 1
		result.ExpiresIn = int(math.Floor(current.Expires.Sub(p.now()).Hours() / 24))
	}

	var reasons []string
	switch {
	case target.Bundle == nil:
		reasons = append(reasons, "App ID not registered")
	case result.Current == nil:
		reasons = append(reasons, "missing")
	default:
		current := result.Current
		if current.State == appstore.ProfileStateInvalid {
			reasons = append(reasons, "invalid")
		} else if current.State != appstore.ProfileStateActive && current.State != "" {
			reasons = append(reasons, "state "+current.State)
		}
		if !current.Expires.IsZero() && !current.Expires.After(p.now()) {
			reasons = append(reasons, "expired")
		} else if !current.Expires.IsZero() && result.ExpiresIn < RenewWithinDays {
			reasons = append(reasons, fmt.Sprintf("expires in %d days", result.ExpiresIn))
		}
		if len(target.Enable) > 0 {
			reasons = append(reasons, "capabilities change (the profile becomes invalid)")
		}
		certificates, err := p.API.ProfileCertificateIDs(ctx, current.ID)
		if err != nil {
			return result, err
		}
		if !containsAny(certificates, certificateIDs(plan.Certificates[certificateGroup(kind)])) {
			reasons = append(reasons, "no valid "+certificateGroup(kind)+" certificate in the profile")
		}
		if needsDevices(kind) {
			devices, err := p.API.ProfileDeviceIDs(ctx, current.ID)
			if err != nil {
				return result, err
			}
			if missing := missingCount(deviceIDs(plan.Devices), devices); missing > 0 {
				reasons = append(reasons, fmt.Sprintf("%d registered device(s) missing", missing))
			}
		}
	}
	result.Reasons = reasons
	switch {
	case len(reasons) == 0:
		result.Action = Keep
	case plan.Desired.Strategy == config.ProvisioningNone:
		result.Action = Xcode
	case len(plan.Certificates[certificateGroup(kind)]) == 0:
		result.Action = Blocked
		result.Reasons = append(result.Reasons, "no valid "+certificateGroup(kind)+" certificate in the team: create one first (fastlane match or Xcode)")
	case needsDevices(kind) && len(plan.Devices) == 0:
		result.Action = Blocked
		result.Reasons = append(result.Reasons, "no registered devices: "+kind+" profiles need at least one")
	case result.Current == nil:
		result.Action = Create
	default:
		result.Action = Recreate
	}
	return result, nil
}

func certificateIDs(certificates []appstore.Certificate) []string {
	ids := make([]string, 0, len(certificates))
	for _, certificate := range certificates {
		ids = append(ids, certificate.ID)
	}
	return ids
}

func deviceIDs(devices []appstore.Device) []string {
	ids := make([]string, 0, len(devices))
	for _, device := range devices {
		ids = append(ids, device.ID)
	}
	return ids
}

func containsAny(have, wanted []string) bool {
	set := map[string]bool{}
	for _, id := range have {
		set[id] = true
	}
	for _, id := range wanted {
		if set[id] {
			return true
		}
	}
	return false
}

func missingCount(wanted, have []string) int {
	set := map[string]bool{}
	for _, id := range have {
		set[id] = true
	}
	count := 0
	for _, id := range wanted {
		if !set[id] {
			count++
		}
	}
	return count
}
