package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/internal/credentials"
	"github.com/lucaswilliameufrasio/mobile-release/internal/tui"
)

func runSetup(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	var common commonFlags
	common.register(flags)
	envName := flags.String("env", "", "environment to set up (default: every environment)")
	forget := flags.Bool("forget-bitwarden", false, "forget the Bitwarden items chosen before")
	if err := flags.Parse(args); err != nil {
		return err
	}
	a, err := newApp(common)
	if err != nil {
		return err
	}
	if *forget {
		if err := a.bitwarden.Forget(); err != nil {
			return err
		}
		a.printf("Bitwarden choices forgotten.\n")
		return nil
	}
	if err := a.loadConfig(); err != nil {
		return err
	}
	if !a.interactive {
		return errors.New("setup is interactive; in CI provide the MOBILE_RELEASE_* variables instead")
	}
	names := a.config.EnvironmentNames()
	if *envName != "" {
		if _, err := a.config.Environment(*envName); err != nil {
			return err
		}
		names = []string{*envName}
	}
	if len(names) == 1 {
		if err := a.wrapExecStores(names[0], append([]string{"setup", "-env", names[0]}, args...)); err != nil {
			return err
		}
	}
	all := credentials.Platforms{Android: a.config.AndroidEnabled(), IOS: a.config.IOSEnabled()}
	for _, name := range names {
		env := a.config.Environments[name]
		required, result, err := a.resolve(ctx, env, all)
		if err != nil {
			return err
		}
		a.printf("\nEnvironment %s (%s)\n", name, strings.Join(env.Destinations, ", "))
		printSources(a, required, result)
		if len(result.Missing) == 0 {
			continue
		}
		if err := a.setupSpecs(ctx, env, result.Missing); err != nil {
			return err
		}
	}
	a.printf("\nDone. Check everything with `mobile-release doctor`.\n")
	return nil
}

// writers lists the stores setup can save to, in priority order.
func (a *app) writers() []credentials.Writer {
	var writers []credentials.Writer
	for _, store := range a.stores {
		writer, ok := store.(credentials.Writer)
		if !ok || !store.Available() {
			continue
		}
		if command, isCommand := store.(credentials.CommandStore); isCommand && !command.CanWrite() {
			continue
		}
		writers = append(writers, writer)
	}
	return writers
}

// setupSpecs asks for each missing credential and stores it.
func (a *app) setupSpecs(ctx context.Context, env *config.Environment, missing []credentials.Spec) error {
	for index, spec := range missing {
		a.printf("\n[%d/%d] %s\n", index+1, len(missing), spec.Label)
		if spec.Help != "" {
			a.printf("      %s\n", spec.Help)
		}
		key, err := a.chooseScope(env, spec)
		if err != nil {
			return err
		}
		if err := a.provideValue(ctx, spec, key); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) chooseScope(env *config.Environment, spec credentials.Spec) (credentials.Key, error) {
	shared := credentials.Key{App: a.config.App.ID, ID: spec.ID}
	scoped := credentials.Key{App: a.config.App.ID, Env: env.Name, ID: spec.ID}
	if len(a.config.Environments) == 1 {
		return shared, nil
	}
	initial := "env"
	if spec.Shared {
		initial = "shared"
	}
	choice, err := tui.Select("Use this value for which environments?", "", []tui.Option{
		{Label: "Every environment", Hint: "e.g. the upload keystore", Value: "shared"},
		{Label: "Only " + env.Name, Hint: "e.g. a different Firebase project", Value: "env"},
	}, initial)
	if err != nil {
		return credentials.Key{}, err
	}
	if choice == "shared" {
		return shared, nil
	}
	return scoped, nil
}

func (a *app) provideValue(ctx context.Context, spec credentials.Spec, key credentials.Key) error {
	options := []tui.Option{}
	if spec.Kind == credentials.File {
		options = append(options, tui.Option{Label: "Choose a file", Hint: "stored base64-encoded", Value: "file"})
	} else {
		options = append(options, tui.Option{Label: "Type or paste the value", Hint: "hidden while typing", Value: "text"})
	}
	if a.bitwarden.Available() {
		options = append(options, tui.Option{Label: "Use an existing Bitwarden item", Hint: "remembered by item id", Value: "bitwarden"})
	}
	method := options[0].Value
	if len(options) > 1 {
		choice, err := tui.Select("How do you want to provide it?", "", options, method)
		if err != nil {
			return err
		}
		method = choice
	}
	if method == "bitwarden" {
		return a.pickBitwarden(ctx, spec, key)
	}

	var data []byte
	if method == "file" {
		path, err := tui.Input("Path to the file", tui.InputOptions{
			Placeholder: "~/keys/" + strings.ReplaceAll(spec.ID, ".", "-") + spec.Extension,
			Validate: func(value string) error {
				info, err := os.Stat(expandHome(strings.TrimSpace(value)))
				if err != nil {
					return fmt.Errorf("Cannot read %s.", value)
				}
				if info.IsDir() {
					return errors.New("That is a directory.")
				}
				return nil
			},
		})
		if err != nil {
			return err
		}
		data, err = os.ReadFile(expandHome(strings.TrimSpace(path)))
		if err != nil {
			return err
		}
	} else {
		value, err := tui.Input(spec.Label, tui.InputOptions{Secret: true, Validate: tui.Required})
		if err != nil {
			return err
		}
		data = []byte(value)
	}

	writer, err := a.chooseWriter()
	if err != nil {
		return err
	}
	stored := credentials.EncodeStored(spec, data)
	if err := writer.Set(ctx, spec, key, stored); err != nil {
		return fmt.Errorf("save %s in %s: %w", spec.Label, writer.Name(), err)
	}
	a.printf("      saved in %s\n", writer.Name())
	return nil
}

func (a *app) chooseWriter() (credentials.Writer, error) {
	writers := a.writers()
	if len(writers) == 0 {
		return nil, errors.New("no store can save values on this machine: install the Bitwarden CLI (bw), use macOS Keychain, " +
			"or add a store with a set command to the personal config (see README)")
	}
	if len(writers) == 1 {
		return writers[0], nil
	}
	var options []tui.Option
	initial := writers[0].Name()
	for _, writer := range writers {
		options = append(options, tui.Option{Label: writer.Name(), Value: writer.Name()})
		if writer.Name() == a.user.DefaultStore {
			initial = writer.Name()
		}
	}
	name, err := tui.Select("Where should it be saved?", "", options, initial)
	if err != nil {
		return nil, err
	}
	for _, writer := range writers {
		if writer.Name() == name {
			return writer, nil
		}
	}
	return nil, fmt.Errorf("unknown store %s", name)
}

func (a *app) unlockBitwarden(ctx context.Context) error {
	switch a.bitwarden.Status(ctx) {
	case "unlocked":
		return nil
	case "locked":
		password, err := tui.Input("Bitwarden master password", tui.InputOptions{Secret: true, Validate: tui.Required})
		if err != nil {
			return err
		}
		return a.bitwarden.Unlock(ctx, password)
	default:
		return errors.New("Bitwarden CLI is not logged in: run `bw login` first")
	}
}

func (a *app) pickBitwarden(ctx context.Context, spec credentials.Spec, key credentials.Key) error {
	if err := a.unlockBitwarden(ctx); err != nil {
		return err
	}
	term := strings.Fields(spec.Label)[0]
	for {
		search, err := tui.Input("Search Bitwarden items", tui.InputOptions{Initial: term, Validate: tui.Required})
		if err != nil {
			return err
		}
		items, err := a.bitwarden.Search(ctx, search)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			a.printf("      no items match %q\n", search)
			term = search
			continue
		}
		var options []tui.Option
		for _, item := range items {
			options = append(options, tui.Option{Label: item.Name, Hint: item.Username, Value: item.ID})
		}
		options = append(options, tui.Option{Label: "Search again", Value: ""})
		id, err := tui.Select("Which item holds "+spec.Label+"?", "Only names are shown; values are never displayed.", options, "")
		if err != nil {
			return err
		}
		if id == "" {
			term = search
			continue
		}
		var item credentials.Item
		for _, candidate := range items {
			if candidate.ID == id {
				item = candidate
			}
		}
		field, err := chooseField(item)
		if err != nil {
			return err
		}
		mapping := credentials.Mapping{Item: id, Field: field}
		if _, err := a.bitwarden.Read(ctx, spec, mapping); err != nil {
			a.printf("      %v\n", err)
			continue
		}
		if err := a.bitwarden.Remember(key, mapping); err != nil {
			return err
		}
		a.printf("      linked to Bitwarden item %q\n", item.Name)
		return nil
	}
}

func chooseField(item credentials.Item) (string, error) {
	options := []tui.Option{{Label: "Password", Value: "password"}, {Label: "Notes", Value: "notes"}}
	for _, field := range item.Fields {
		options = append(options, tui.Option{Label: "Field: " + field, Value: field})
	}
	return tui.Select("Which part of the item?", "", options, "password")
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
