// Command mobile-release builds and distributes Flutter, Expo, Kotlin
// Multiplatform and native mobile apps from a release.toml, asking for what
// is missing instead of requiring environment variables.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lucaswilliameufrasio/mobile-release/internal/tui"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

const usage = `mobile-release — guided builds and releases for mobile apps

Usage:
  mobile-release init      detect the project and write release.toml
  mobile-release setup     ask for missing credentials and store them
  mobile-release doctor    check tools, configuration and credentials
  mobile-release deploy    build and publish an environment
  mobile-release qa        build an APK and an iOS simulator app
  mobile-release version

Common flags: -dir <project> -config <release.toml> -yes (never prompt)
deploy/qa:    -env <name> -platform all|android|ios -dry-run -notes <text>

Run "mobile-release <command> -h" for every flag.
`

var lookPath = exec.LookPath

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			os.Exit(130)
		}
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "mobile-release:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	args = legacyArgs(args)
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	command, rest := args[0], args[1:]
	switch command {
	case "init":
		return runInit(ctx, rest)
	case "setup":
		return runSetup(ctx, rest)
	case "doctor":
		return runDoctor(ctx, rest)
	case "deploy":
		return runDeploy(ctx, rest)
	case "qa":
		return runQA(ctx, rest)
	case "version", "-version", "--version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", command, usage)
}

// legacyArgs maps the v1 form `mobile-release [-config x] [-platform p]
// <qa|internal>` to the current commands; schema v1 files become an
// "internal" environment.
func legacyArgs(args []string) []string {
	if len(args) == 0 || !strings.HasPrefix(args[0], "-") {
		return args
	}
	flags := flag.NewFlagSet("mobile-release", flag.ContinueOnError)
	configPath := flags.String("config", "release.toml", "")
	platform := flags.String("platform", "all", "")
	if err := flags.Parse(args); err != nil || flags.NArg() == 0 {
		return args
	}
	switch flags.Arg(0) {
	case "internal":
		return []string{"deploy", "-config", *configPath, "-platform", *platform, "-env", "internal", "-yes"}
	case "qa":
		return []string{"qa", "-config", *configPath, "-platform", *platform}
	case "doctor":
		return []string{"doctor", "-config", *configPath}
	}
	return args
}
