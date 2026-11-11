package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/credentials"
)

type tool struct {
	name string
	hint string
}

// requiredTools lists the executables a release of the project needs.
func requiredTools(c config.Config, platforms credentials.Platforms) []tool {
	tools := []tool{
		{"git", "install git"},
		{"bundle", "Ruby >= 3.2 with Bundler, e.g. `mise use -g ruby@3.4`"},
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
