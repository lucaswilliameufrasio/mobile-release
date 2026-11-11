package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/adopt"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/runner"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/tui"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/upgrade"
)

func runUpgrade(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	var common commonFlags
	common.register(flags)
	to := flags.String("to", "", "version to move to (default: the latest release of this major version)")
	install := flags.Bool("install", false, "also install the new version of the CLI (go install)")
	dryRun := flags.Bool("dry-run", false, "print the plan without changing anything")
	if err := flags.Parse(args); err != nil {
		return err
	}
	a, err := newApp(common)
	if err != nil {
		return err
	}

	stable := []upgrade.Release{}
	if releases, err := upgrade.Fetch(ctx, nil); err == nil {
		stable = upgrade.Stable(releases)
	} else if *to == "" {
		return fmt.Errorf("%w (pass -to vX.Y.Z to choose a version without listing releases)", err)
	}

	installed := version
	installedVersion, installedOK := upgrade.Parse(installed)
	major := installedVersion.Major
	if !installedOK {
		major = 2
	}
	target := *to
	if target == "" {
		same, next := upgrade.Target(stable, major)
		if same == nil {
			return fmt.Errorf("no v%d release found", major)
		}
		target = same.Tag
		if next != nil {
			a.printf("Note: %s is a new major version with a new module path; upgrade to it manually (see its release notes).\n\n", next.Tag)
		}
	}
	targetVersion, ok := upgrade.Parse(target)
	if !ok {
		return fmt.Errorf("invalid version %q", target)
	}
	if targetVersion.Major != major {
		return fmt.Errorf("%s changes the major version (module path); upgrade to it manually", target)
	}

	inProject := false
	var pins upgrade.Pins
	if _, err := os.Stat(a.configPath); err == nil {
		if err := a.loadConfig(); err != nil {
			return err
		}
		inProject = true
		pins = upgrade.ReadPins(a.root)
	}

	a.printf("mobile-release %s\n", target)
	a.printf("  installed CLI:  %s\n", installed)
	newerCLI := installedOK && lessTag(target, installed)
	if newerCLI {
		a.printf("  (the installed CLI is newer than the latest listed release; the list can lag about a minute behind a new release)\n")
	}
	if inProject {
		a.printf("  project pins:   Makefile %s, workflow %s\n", orDash(pins.Makefile), orDash(pins.Workflow))
	}

	from := pins.Oldest()
	if installedOK && (from == "" || lessTag(installed, from)) {
		from = installed
	}
	if notes := upgrade.Between(stable, from, target); len(notes) > 0 {
		a.printf("\nWhat changed\n")
		for _, release := range notes {
			a.printf("\n## %s\n%s\n", release.Tag, indent(release.Notes))
		}
	}

	var changes []adopt.Change
	if inProject {
		changes, err = upgrade.PlanProject(a.root, a.config, target, resolveTagCommit(ctx, target))
		if err != nil {
			return err
		}
		a.printf("\nProject\n")
		for _, change := range changes {
			printChange(a, change)
		}
	}
	installNeeded := !installedOK || lessTag(installed, target)
	installCommand := "go install " + adopt.Module + "@" + target
	if !adopt.Pending(changes) && !installNeeded {
		switch {
		case newerCLI && inProject:
			a.printf("\nNothing to change: the project pins the latest release and the CLI is newer.\n")
		case newerCLI:
			a.printf("\nNothing to change: the CLI is newer than the latest release.\n")
		default:
			a.printf("\nAlready on %s.\n", target)
		}
		return nil
	}
	if *dryRun {
		if installNeeded {
			a.printf("\nCLI: would run `%s`\n", installCommand)
		}
		a.printf("\nDry run: nothing was changed.\n")
		return nil
	}

	if adopt.Pending(changes) {
		apply := common.yes
		if !apply {
			if !a.interactive {
				return errors.New("refusing to write files without confirmation: pass -yes in non-interactive runs")
			}
			if apply, err = tui.Confirm("Update the project to "+target+"?", true); err != nil {
				return err
			}
		}
		if apply {
			if err := adopt.Apply(a.root, changes); err != nil {
				return err
			}
			a.printf("Project pinned to %s.\n", target)
		}
	}

	if installNeeded {
		doInstall := *install
		if !doInstall && a.interactive && !common.yes {
			if doInstall, err = tui.Confirm("Install mobile-release "+target+" now (go install)?", true); err != nil {
				return err
			}
		}
		if !doInstall {
			a.printf("\nTo update the CLI: %s\n", installCommand)
			return nil
		}
		a.printf("$ %s\n", installCommand)
		if err := (runner.Exec{Out: os.Stdout, Err: os.Stderr}).Run(ctx, runner.Command{
			Name: "go", Args: []string{"install", adopt.Module + "@" + target},
		}); err != nil {
			return err
		}
		a.printf("Installed mobile-release %s.\n", target)
	}
	return nil
}

func lessTag(left, right string) bool {
	l, okLeft := upgrade.Parse(left)
	r, okRight := upgrade.Parse(right)
	return okLeft && okRight && l.Less(r)
}

func orDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func indent(text string) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")), "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}
