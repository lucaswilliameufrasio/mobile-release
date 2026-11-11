package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Resolver looks credentials up in Stores, in order.
type Resolver struct {
	Stores []Store
}

// Result is the outcome of a resolution.
type Result struct {
	Set     *Set
	Missing []Spec
}

// Keys returns the lookup order for a credential: environment-specific
// first, then shared.
func Keys(app, env, id string) []Key {
	keys := []Key{}
	if env != "" {
		keys = append(keys, Key{App: app, Env: env, ID: id})
	}
	return append(keys, Key{App: app, ID: id})
}

// Resolve finds every spec; values that no store has are reported Missing.
func (r Resolver) Resolve(ctx context.Context, specs []Spec, app, env string) (Result, error) {
	result := Result{Set: NewSet()}
	for _, spec := range specs {
		value, found, err := r.find(ctx, spec, app, env)
		if err != nil {
			return result, err
		}
		if !found {
			result.Missing = append(result.Missing, spec)
			continue
		}
		result.Set.Put(value)
	}
	return result, nil
}

func (r Resolver) find(ctx context.Context, spec Spec, app, env string) (Value, bool, error) {
	for _, store := range r.Stores {
		if !store.Available() {
			continue
		}
		for _, key := range Keys(app, env, spec.ID) {
			stored, err := store.Get(ctx, spec, key)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return Value{}, false, fmt.Errorf("%s (%s): %w", spec.Label, store.Name(), err)
			}
			value := Value{Spec: spec, Source: store.Name()}
			if path, ok := strings.CutPrefix(stored, "path:"); ok && spec.Kind == File {
				if _, err := os.Stat(path); err != nil {
					return Value{}, false, fmt.Errorf("%s: %w", spec.Label, err)
				}
				value.Path = path
				return value, true, nil
			}
			data, err := decodeStored(spec, stored)
			if err != nil {
				return Value{}, false, err
			}
			value.Data = data
			return value, true, nil
		}
	}
	return Value{}, false, nil
}

// --- Personal configuration -------------------------------------------------

// UserConfig is ~/.config/mobile-release/config.toml (os.UserConfigDir). It
// is personal: it is never read from the project.
type UserConfig struct {
	// Order lists store names by priority. Built-in: keychain, bitwarden, env.
	Order []string `toml:"order"`
	// DefaultStore is where setup saves values when none is chosen.
	DefaultStore string                 `toml:"default_store"`
	Stores       map[string]StoreConfig `toml:"stores"`
}

// StoreConfig declares a custom store.
//
//	type = "exec":    run mobile-release under Command, which injects the
//	                  secrets as environment variables (e.g. `<tool> run --`).
//	type = "command": Get prints a value; Set receives it on stdin.
//
// Placeholders in commands: {app} {env} {id} {key}.
type StoreConfig struct {
	Type    string   `toml:"type"`
	Command []string `toml:"command"`
	Get     []string `toml:"get"`
	Set     []string `toml:"set"`
	Service string   `toml:"service"`
	// EnvNames maps mobile-release environments to the tool's names.
	EnvNames map[string]string `toml:"env_names"`
	// Vars maps credential ids to the injected variable names (exec stores).
	Vars map[string]string `toml:"vars"`
}

// DefaultOrder is used when no personal configuration exists.
var DefaultOrder = []string{"keychain", "bitwarden", "env"}

// UserConfigPath returns the personal configuration path.
func UserConfigPath() (string, error) {
	if path := os.Getenv("MOBILE_RELEASE_USER_CONFIG"); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mobile-release", "config.toml"), nil
}

// LoadUserConfig reads path; a missing file yields the defaults.
func LoadUserConfig(path string) (UserConfig, error) {
	config := UserConfig{}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return config, err
	}
	if err == nil {
		decoder := toml.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return config, fmt.Errorf("%s: %w", path, err)
		}
	}
	if len(config.Order) == 0 {
		config.Order = defaultOrder(config.Stores)
	}
	return config, config.validate()
}

// defaultOrder is keychain, then custom stores alphabetically, then
// bitwarden and env.
func defaultOrder(stores map[string]StoreConfig) []string {
	custom := make([]string, 0, len(stores))
	for name := range stores {
		custom = append(custom, name)
	}
	sort.Strings(custom)
	order := []string{"keychain"}
	order = append(order, custom...)
	return append(order, "bitwarden", "env")
}

func (u UserConfig) validate() error {
	for _, name := range u.Order {
		if name == "keychain" || name == "bitwarden" || name == "env" {
			continue
		}
		store, ok := u.Stores[name]
		if !ok {
			return fmt.Errorf("credentials order references unknown store %q", name)
		}
		switch store.Type {
		case "exec":
			if len(store.Command) == 0 {
				return fmt.Errorf("store %q: exec stores need command", name)
			}
		case "command":
			if len(store.Get) == 0 {
				return fmt.Errorf("store %q: command stores need get", name)
			}
		case "keychain", "bitwarden":
		default:
			return fmt.Errorf("store %q: type must be exec, command, keychain or bitwarden", name)
		}
	}
	return nil
}

// ExecStore is a store that works by re-running the CLI under a command.
type ExecStore struct {
	Name     string
	Command  []string
	EnvNames map[string]string
}

// appliedVar lists exec stores already wrapping this process.
const appliedVar = "MOBILE_RELEASE_EXEC_APPLIED"

// Applied reports whether this process already runs under the store.
func (e ExecStore) Applied() bool {
	for _, name := range strings.Split(os.Getenv(appliedVar), ",") {
		if name == e.Name {
			return true
		}
	}
	return false
}

// Argv returns the command line that re-runs self under the store.
func (e ExecStore) Argv(env string, self []string) []string {
	if mapped := e.EnvNames[env]; mapped != "" {
		env = mapped
	}
	argv := expand(e.Command, Key{Env: env})
	return append(argv, self...)
}

// MarkApplied returns the environment value marking the store as applied.
func (e ExecStore) MarkApplied() string {
	current := os.Getenv(appliedVar)
	if current == "" {
		return appliedVar + "=" + e.Name
	}
	return appliedVar + "=" + current + "," + e.Name
}

// Available reports whether the wrapping command exists.
func (e ExecStore) Available() bool {
	return len(e.Command) > 0 && lookPath(e.Command[0])
}

// Build returns the ordered stores; exec stores are returned separately so
// the CLI can re-run itself under them before resolving.
func (u UserConfig) Build(bitwardenMap string) ([]Store, []ExecStore) {
	var stores []Store
	var execs []ExecStore
	for _, name := range u.Order {
		custom, isCustom := u.Stores[name]
		switch {
		case name == "keychain" || (isCustom && custom.Type == "keychain"):
			stores = append(stores, KeychainStore{Service: custom.Service})
		case name == "bitwarden" || (isCustom && custom.Type == "bitwarden"):
			stores = append(stores, &BitwardenStore{MapPath: bitwardenMap})
		case name == "env":
			stores = append(stores, EnvStore{})
		case custom.Type == "exec":
			exec := ExecStore{Name: name, Command: custom.Command, EnvNames: custom.EnvNames}
			execs = append(execs, exec)
			if exec.Applied() {
				stores = append(stores, EnvStore{Label: name, Vars: custom.Vars})
			}
		case custom.Type == "command":
			stores = append(stores, CommandStore{Label: name, GetCmd: custom.Get, SetCmd: custom.Set})
		}
	}
	return stores, execs
}
