package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"strings"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/adopt"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/runner"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/tui"
)

func runAdopt(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("adopt", flag.ContinueOnError)
	var common commonFlags
	common.register(flags)
	pinned := flags.String("version", "", "mobile-release version to pin (default: this binary's version)")
	workflow := flags.Bool("workflow", false, "also add .github/workflows/release.yml")
	dryRun := flags.Bool("dry-run", false, "print the plan without writing files")
	if err := flags.Parse(args); err != nil {
		return err
	}
	workflowSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "workflow" {
			workflowSet = true
		}
	})
	a, err := newApp(common)
	if err != nil {
		return err
	}
	target := *pinned
	if target == "" {
		target = version
	}
	if target == "dev" || target == "(devel)" {
		return errors.New("this is a development build: pass -version vX.Y.Z to pin a release")
	}

	if _, err := os.Stat(a.configPath); errors.Is(err, os.ErrNotExist) {
		if *dryRun {
			a.printf("+ %s would be created by `mobile-release init`\n", a.relative(a.configPath))
			return nil
		}
		a.printf("No %s yet: detecting the project.\n", a.relative(a.configPath))
		if err := a.writeConfig(); err != nil {
			return err
		}
		a.printf("Wrote %s.\n", a.relative(a.configPath))
	}
	if err := a.loadConfig(); err != nil {
		return err
	}
	if !workflowSet && a.interactive {
		*workflow, err = tui.Confirm("Add a manual release workflow for GitHub Actions?", true)
		if err != nil {
			return err
		}
	}
	ref := ""
	if *workflow {
		ref = resolveTagCommit(ctx, target)
		if ref == "" {
			a.printf("warning: could not resolve the commit of %s; the workflow will use the tag\n", target)
		}
	}

	changes, err := adopt.Plan(adopt.Options{Root: a.root, Config: a.config, Version: target, Workflow: *workflow, WorkflowRef: ref})
	if err != nil {
		return err
	}
	a.printf("\nPlan (mobile-release %s)\n", target)
	for _, change := range changes {
		printChange(a, change)
	}
	if !adopt.Pending(changes) {
		a.printf("\nNothing to change.\n")
		return nil
	}
	if *dryRun {
		a.printf("\nDry run: nothing was written.\n")
		return nil
	}
	if !common.yes {
		if !a.interactive {
			return errors.New("refusing to write files without confirmation: pass -yes in non-interactive runs")
		}
		ok, err := tui.Confirm("Apply these changes?", true)
		if err != nil {
			return err
		}
		if !ok {
			return tui.ErrCancelled
		}
	}
	if err := adopt.Apply(a.root, changes); err != nil {
		return err
	}
	a.printf("\nDone. Next:\n  make release-install   # install mobile-release %s\n  make release-setup     # store the credentials once\n  make deploy            # build and publish\n", target)
	return nil
}

func printChange(a *app, change adopt.Change) {
	switch change.Action {
	case adopt.Skip:
		a.printf("  = %s: skipped (%s)\n", change.Path, change.Reason)
		return
	case adopt.Update:
		a.printf("  ~ %s: %s\n", change.Path, change.Reason)
	case adopt.Append:
		a.printf("  + %s: append\n", change.Path)
	case adopt.Create:
		a.printf("  + %s: create\n", change.Path)
	}
	if change.Action == adopt.Update {
		return
	}
	for _, line := range strings.Split(strings.TrimRight(change.Content, "\n"), "\n") {
		a.printf("      %s\n", line)
	}
}

// resolveTagCommit returns the commit a release tag points to, or "".
func resolveTagCommit(ctx context.Context, tag string) string {
	out, err := runner.Exec{}.Output(ctx, runner.Command{
		Name: "git",
		Args: []string{"ls-remote", "https://github.com/" + adopt.Repository + ".git", "refs/tags/" + tag, "refs/tags/" + tag + "^{}"},
	})
	if err != nil {
		return ""
	}
	return peeledCommit(out)
}

// peeledCommit picks the commit of a tag from `git ls-remote` output: the
// peeled ^{} line of an annotated tag, else the only line.
func peeledCommit(out string) string {
	commit := ""
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// The peeled ^{} entry is the commit of an annotated tag.
		if strings.HasSuffix(fields[1], "^{}") || commit == "" {
			commit = fields[0]
		}
	}
	return commit
}
