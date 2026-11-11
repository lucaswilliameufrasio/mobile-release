package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/credentials"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/project"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/tui"
)

// app is the state shared by every subcommand.
type app struct {
	root        string
	configPath  string
	config      config.Config
	user        credentials.UserConfig
	stores      []credentials.Store
	execs       []credentials.ExecStore
	bitwarden   *credentials.BitwardenStore
	interactive bool
	out         io.Writer
}

type commonFlags struct {
	config string
	dir    string
	yes    bool
}

func (c *commonFlags) register(flags *flag.FlagSet) {
	flags.StringVar(&c.config, "config", "release.toml", "path to release.toml, relative to -dir")
	flags.StringVar(&c.dir, "dir", ".", "project root")
	flags.BoolVar(&c.yes, "yes", false, "never prompt (CI); fail instead of asking")
}

func newApp(common commonFlags) (*app, error) {
	root, err := filepath.Abs(common.dir)
	if err != nil {
		return nil, err
	}
	a := &app{root: root, configPath: filepath.Join(root, common.config), out: os.Stdout}
	a.interactive = !common.yes && tui.Interactive()
	userPath, err := credentials.UserConfigPath()
	if err != nil {
		return nil, err
	}
	a.user, err = credentials.LoadUserConfig(userPath)
	if err != nil {
		return nil, err
	}
	bitwardenMap := filepath.Join(filepath.Dir(userPath), "bitwarden.toml")
	a.stores, a.execs = a.user.Build(bitwardenMap)
	for _, store := range a.stores {
		if bitwarden, ok := store.(*credentials.BitwardenStore); ok {
			a.bitwarden = bitwarden
		}
	}
	if a.bitwarden == nil {
		a.bitwarden = &credentials.BitwardenStore{MapPath: bitwardenMap}
	}
	a.bitwarden.Session = os.Getenv("BW_SESSION")
	return a, nil
}

// loadConfig reads release.toml, applies defaults and env overrides, and
// validates it.
func (a *app) loadConfig() error {
	if _, err := os.Stat(a.configPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s not found: run `mobile-release init` first", a.relative(a.configPath))
	}
	c, err := config.Load(a.configPath)
	if err != nil {
		return err
	}
	if c.Project.Type == "" {
		c.Project.Type = project.Detect(a.root)
	}
	c.ApplyEnv(os.LookupEnv)
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return err
	}
	a.config = c
	return nil
}

func (a *app) relative(path string) string {
	if relative, err := filepath.Rel(a.root, path); err == nil {
		return relative
	}
	return path
}

func (a *app) printf(format string, args ...any) {
	fmt.Fprintf(a.out, format, args...)
}

// chooseEnvironment returns the flag value or asks.
func (a *app) chooseEnvironment(value, purpose string) (*config.Environment, error) {
	if value != "" {
		return a.config.Environment(value)
	}
	names := a.config.EnvironmentNames()
	if len(names) == 1 {
		return a.config.Environment(names[0])
	}
	if !a.interactive {
		return nil, fmt.Errorf("choose an environment with -env (%s)", strings.Join(names, ", "))
	}
	var options []tui.Option
	for _, name := range names {
		env := a.config.Environments[name]
		hint := strings.Join(env.Destinations, ", ")
		if env.Description != "" {
			hint = env.Description + " · " + hint
		}
		options = append(options, tui.Option{Label: name, Hint: hint, Value: name})
	}
	name, err := tui.Select("Which environment do you want to "+purpose+"?", "", options, "")
	if err != nil {
		return nil, err
	}
	return a.config.Environment(name)
}

// choosePlatforms returns the flag value or asks among enabled platforms.
func (a *app) choosePlatforms(value string) (credentials.Platforms, string, error) {
	android, ios := a.config.AndroidEnabled(), a.config.IOSEnabled()
	if value == "" && android && ios && a.interactive {
		choice, err := tui.Select("Which platforms?", "", []tui.Option{
			{Label: "Android and iOS", Value: "all"},
			{Label: "Android", Value: "android"},
			{Label: "iOS", Value: "ios"},
		}, "all")
		if err != nil {
			return credentials.Platforms{}, "", err
		}
		value = choice
	}
	if value == "" {
		value = "all"
	}
	switch value {
	case "all":
		return credentials.Platforms{Android: android, IOS: ios}, value, nil
	case "android":
		if !android {
			return credentials.Platforms{}, "", errors.New("android is disabled in release.toml")
		}
		return credentials.Platforms{Android: true}, value, nil
	case "ios":
		if !ios {
			return credentials.Platforms{}, "", errors.New("ios is disabled in release.toml")
		}
		return credentials.Platforms{IOS: true}, value, nil
	}
	return credentials.Platforms{}, "", fmt.Errorf("invalid platform %q: use all, android or ios", value)
}

// wrapExecStores re-runs this process under every available exec store not
// applied yet (e.g. a secrets manager's `run --` command), so its secrets
// arrive as environment variables. args must reproduce the choices made.
func (a *app) wrapExecStores(env string, args []string) error {
	for _, store := range a.execs {
		if store.Applied() || !store.Available() {
			continue
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		argv := store.Argv(env, append([]string{self}, args...))
		binary, err := lookPath(argv[0])
		if err != nil {
			return fmt.Errorf("store %s: %w", store.Name, err)
		}
		a.printf("==> Loading credentials through %s\n", store.Name)
		environ := append(os.Environ(), store.MarkApplied())
		if runtime.GOOS == "windows" {
			return errors.New("exec stores are not supported on Windows")
		}
		return syscall.Exec(binary, argv, environ)
	}
	return nil
}

// resolve finds the credentials needed by env on platforms.
func (a *app) resolve(ctx context.Context, env *config.Environment, platforms credentials.Platforms) ([]credentials.Spec, credentials.Result, error) {
	required := credentials.Required(a.config, env, platforms)
	result, err := credentials.Resolver{Stores: a.stores}.Resolve(ctx, required, a.config.App.ID, env.Name)
	return required, result, err
}

func describeMissing(missing []credentials.Spec) string {
	var lines []string
	for _, spec := range missing {
		variable := spec.EnvName
		if spec.Kind == credentials.File {
			variable += "_PATH or " + spec.EnvName + "_BASE64"
		}
		lines = append(lines, fmt.Sprintf("  - %s (CI: %s)", spec.Label, variable))
	}
	return strings.Join(lines, "\n")
}

func printSources(a *app, required []credentials.Spec, result credentials.Result) {
	sources := result.Set.Sources()
	for _, spec := range required {
		if source, ok := sources[spec.ID]; ok {
			a.printf("  ✓ %s ← %s\n", spec.Label, source)
		} else {
			a.printf("  ✗ %s (missing)\n", spec.Label)
		}
	}
}

func boolPtr(value bool) *bool { return &value }
