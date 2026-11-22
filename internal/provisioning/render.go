package provisioning

import (
	"fmt"
	"io"
	"strings"
)

// Render prints the plan doctor-style:
//
//	✓ in place   + to create/enable   ~ to recreate   ✗ blocked   ! warning
func Render(w io.Writer, plan Plan) {
	desired := plan.Desired
	team := desired.TeamID
	if team == "" {
		team = "team not set"
	} else {
		team = "team " + team
	}
	fmt.Fprintf(w, "%s (%s, %s, profiles: %s via %s)\n", firstNonEmpty(desired.Name, desired.Root), desired.Root, team, describeKinds(desired.Kinds), describeStrategy(desired.Strategy))
	for _, warning := range plan.Warnings {
		fmt.Fprintf(w, "  ! %s\n", warning)
	}
	for _, target := range plan.Targets {
		fmt.Fprintf(w, "  %s\n", target.Target.BundleID)
		if target.Bundle == nil {
			fmt.Fprintf(w, "    + App ID not registered: register %q\n", sanitizeName(target.Target.Name))
		} else {
			fmt.Fprintf(w, "    ✓ App ID registered\n")
		}
		for _, capability := range target.Enabled {
			fmt.Fprintf(w, "    ✓ %s\n", capability.Label)
			if capability.Note != "" {
				fmt.Fprintf(w, "      · %s\n", capability.Note)
			}
		}
		for _, capability := range target.Enable {
			fmt.Fprintf(w, "    + enable %s (%s)\n", capability.Label, capability.Type)
			if capability.Note != "" {
				fmt.Fprintf(w, "      · %s\n", capability.Note)
			}
		}
		for _, profile := range target.Profiles {
			fmt.Fprintf(w, "    %s\n", describeProfile(profile))
		}
	}
	changes, blocked := plan.Changes(), plan.Blocked()
	switch {
	case changes == 0 && blocked == 0:
		fmt.Fprintf(w, "  Up to date.\n")
	default:
		fmt.Fprintf(w, "  %d change(s)", changes)
		if blocked > 0 {
			fmt.Fprintf(w, ", %d blocked", blocked)
		}
		fmt.Fprintf(w, ".\n")
	}
}

func describeProfile(profile ProfilePlan) string {
	name := ""
	if profile.Current != nil {
		name = fmt.Sprintf(" %q", profile.Current.Name)
	}
	reasons := strings.Join(profile.Reasons, "; ")
	switch profile.Action {
	case Keep:
		text := fmt.Sprintf("✓ %s profile%s, expires %s (%d days)", profile.Kind, name, profile.Current.Expires.Format("2006-01-02"), profile.ExpiresIn)
		if profile.Others > 0 {
			text += fmt.Sprintf("; %d other profile(s) of this type left alone", profile.Others)
		}
		return text
	case Create:
		return fmt.Sprintf("+ %s profile: %s → create %q", profile.Kind, reasons, profile.Name)
	case Recreate:
		return fmt.Sprintf("~ %s profile%s: %s → recreate", profile.Kind, name, reasons)
	case Xcode:
		return fmt.Sprintf("! %s profile%s: %s → Xcode regenerates it on the next archive", profile.Kind, name, reasons)
	}
	return fmt.Sprintf("✗ %s profile%s: %s", profile.Kind, name, reasons)
}

func describeKinds(kinds []string) string {
	if len(kinds) == 0 {
		return "none"
	}
	return strings.Join(kinds, ", ")
}

func describeStrategy(strategy string) string {
	switch strategy {
	case "api":
		return "App Store Connect API"
	case "match":
		return "fastlane match"
	}
	return "Xcode automatic signing"
}
