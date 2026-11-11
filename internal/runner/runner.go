// Package runner executes external commands. Command arguments are logged;
// values passed through Env are never logged, so secrets must travel there.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// Command is one external process.
type Command struct {
	Name string
	Args []string
	// Dir is relative to the runner's root when not absolute.
	Dir string
	// Env holds extra KEY=VALUE pairs added to the inherited environment.
	Env []string
}

// String renders the command for logs and dry runs, without Env values.
func (c Command) String() string {
	parts := append([]string{c.Name}, c.Args...)
	text := strings.Join(parts, " ")
	if c.Dir != "" && c.Dir != "." {
		text = "(cd " + c.Dir + ") " + text
	}
	if names := c.EnvNames(); len(names) > 0 {
		text = "[" + strings.Join(names, " ") + "] " + text
	}
	return text
}

// EnvNames lists the names of the extra variables, sorted.
func (c Command) EnvNames() []string {
	names := make([]string, 0, len(c.Env))
	for _, pair := range c.Env {
		name, _, _ := strings.Cut(pair, "=")
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Runner runs commands from a project root.
type Runner interface {
	Run(context.Context, Command) error
	Output(context.Context, Command) (string, error)
	LookPath(string) error
}

// Exec runs real processes.
type Exec struct {
	Root     string
	Out, Err io.Writer
	// Echo prints each command before running it.
	Echo bool
}

func (e Exec) prepare(ctx context.Context, c Command) *exec.Cmd {
	process := exec.CommandContext(ctx, c.Name, c.Args...)
	process.Dir = e.dir(c.Dir)
	process.Env = append(os.Environ(), c.Env...)
	process.Stdin = os.Stdin
	return process
}

func (e Exec) dir(dir string) string {
	if dir == "" {
		return e.Root
	}
	if strings.HasPrefix(dir, "/") {
		return dir
	}
	if e.Root == "" {
		return dir
	}
	return e.Root + "/" + dir
}

func (e Exec) Run(ctx context.Context, c Command) error {
	if e.Echo && e.Out != nil {
		fmt.Fprintln(e.Out, "$ "+c.String())
	}
	process := e.prepare(ctx, c)
	process.Stdout = e.Out
	process.Stderr = e.Err
	if err := process.Run(); err != nil {
		return fmt.Errorf("%s: %w", c.Name, err)
	}
	return nil
}

func (e Exec) Output(ctx context.Context, c Command) (string, error) {
	process := e.prepare(ctx, c)
	var stdout bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = e.Err
	if err := process.Run(); err != nil {
		return "", fmt.Errorf("%s: %w", c.Name, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (e Exec) LookPath(name string) error {
	_, err := exec.LookPath(name)
	return err
}

// Fake records commands for tests and dry runs. Outputs maps a command
// string (without env) to its output; Missing lists tools reported absent.
type Fake struct {
	Calls   []string
	Envs    [][]string
	Outputs map[string]string
	Missing map[string]bool
	Fail    map[string]error
}

func (f *Fake) record(c Command) error {
	f.Calls = append(f.Calls, c.String())
	f.Envs = append(f.Envs, c.Env)
	key := strings.Join(append([]string{c.Name}, c.Args...), " ")
	if err := f.Fail[key]; err != nil {
		return err
	}
	return nil
}

func (f *Fake) Run(_ context.Context, c Command) error {
	return f.record(c)
}

func (f *Fake) Output(_ context.Context, c Command) (string, error) {
	if err := f.record(c); err != nil {
		return "", err
	}
	return f.Outputs[strings.Join(append([]string{c.Name}, c.Args...), " ")], nil
}

func (f *Fake) LookPath(name string) error {
	if f.Missing[name] {
		return fmt.Errorf("%s not found", name)
	}
	return nil
}

// Dry prints commands instead of running them; Output returns "".
type Dry struct {
	Out io.Writer
}

func (d Dry) Run(_ context.Context, c Command) error {
	fmt.Fprintln(d.Out, "(dry-run) "+c.String())
	return nil
}

func (d Dry) Output(_ context.Context, c Command) (string, error) {
	fmt.Fprintln(d.Out, "(dry-run) "+c.String())
	return "", nil
}

func (Dry) LookPath(name string) error {
	_, err := exec.LookPath(name)
	return err
}
