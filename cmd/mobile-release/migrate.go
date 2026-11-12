package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"strings"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/migrate"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/tui"
)

func runMigrate(_ context.Context, args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	var common commonFlags
	common.register(flags)
	dryRun := flags.Bool("dry-run", false, "print the changes without writing release.toml")
	if err := flags.Parse(args); err != nil {
		return err
	}
	a, err := newApp(common)
	if err != nil {
		return err
	}
	original, err := os.ReadFile(a.configPath)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("no release.toml: run `mobile-release adopt` or `mobile-release init`")
	}
	if err != nil {
		return err
	}
	result, err := migrate.Run(original)
	if err != nil {
		return err
	}

	lines := strings.Split(string(original), "\n")
	a.printf("%s (schema %d)\n", a.relative(a.configPath), result.From)
	for _, finding := range result.Findings {
		switch {
		case finding.Fixed && finding.Line > 0:
			a.printf("  - %d: %s   # %s %s\n", finding.Line, strings.TrimSpace(lines[finding.Line-1]), finding.Key, finding.Message)
		case finding.Fixed:
			a.printf("  ~ %s\n", finding.Message)
		default:
			label := finding.Message
			if finding.Key != "" {
				label = finding.Key + ": " + label
			}
			a.printf("  ! %s\n", label)
		}
	}
	if !result.Changed(original) {
		a.printf("\nNothing to migrate.\n")
		return nil
	}
	if result.Rewritten {
		a.printf("\nNew file:\n%s\n", indent(string(result.Content)))
	}
	if *dryRun {
		a.printf("\nDry run: nothing was written.\n")
		return nil
	}
	if !common.yes {
		if !a.interactive {
			return errors.New("refusing to write release.toml without confirmation: pass -yes in non-interactive runs")
		}
		question := "Apply these changes to release.toml?"
		if result.Rewritten {
			question = "Rewrite release.toml as schema 2 (comments are not kept)?"
		}
		ok, err := tui.Confirm(question, true)
		if err != nil {
			return err
		}
		if !ok {
			return tui.ErrCancelled
		}
	}
	if err := os.WriteFile(a.configPath, result.Content, 0o644); err != nil {
		return err
	}
	a.printf("\nWrote %s; the effective configuration is unchanged.\n", a.relative(a.configPath))
	return nil
}
