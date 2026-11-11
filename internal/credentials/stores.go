package credentials

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ErrNotFound means the store has no value for the key.
var ErrNotFound = errors.New("not found")

// Store is a place credentials can be read from.
type Store interface {
	Name() string
	// Available reports whether the store can be used on this machine.
	Available() bool
	// Get returns the stored (single-line) value.
	Get(ctx context.Context, spec Spec, key Key) (string, error)
}

// Writer is a store that setup can save values to.
type Writer interface {
	Store
	Set(ctx context.Context, spec Spec, key Key, stored string) error
}

// Executor runs a command with stdin and returns stdout. Values travel only
// through stdin/stdout, never as arguments.
type Executor func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error)

// SystemExecutor runs real processes; stderr is kept for error messages.
func SystemExecutor(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return nil, fmt.Errorf("%s: %w: %s", name, err, firstLine(message))
	}
	return stdout.Bytes(), nil
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// --- Environment (CI) -----------------------------------------------------

// EnvStore reads the MOBILE_RELEASE_* variables documented for CI.
type EnvStore struct {
	Lookup func(string) (string, bool)
	// Vars overrides the variable name per credential id.
	Vars  map[string]string
	Label string
}

func (e EnvStore) Name() string {
	if e.Label != "" {
		return e.Label
	}
	return "env"
}

func (EnvStore) Available() bool { return true }

func (e EnvStore) variable(spec Spec) string {
	if name := e.Vars[spec.ID]; name != "" {
		return name
	}
	return spec.EnvName
}

func (e EnvStore) Get(_ context.Context, spec Spec, _ Key) (string, error) {
	lookup := e.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	name := e.variable(spec)
	if spec.Kind == File {
		if value, ok := lookup(name + "_BASE64"); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
		if path, ok := lookup(name + "_PATH"); ok && strings.TrimSpace(path) != "" {
			return "path:" + strings.TrimSpace(path), nil
		}
		// A custom name may point straight at base64 content.
		if e.Vars[spec.ID] != "" {
			if value, ok := lookup(name); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), nil
			}
		}
		return "", ErrNotFound
	}
	if value, ok := lookup(name); ok && value != "" {
		return value, nil
	}
	return "", ErrNotFound
}

// --- macOS Keychain -------------------------------------------------------

// KeychainStore keeps values as generic passwords (service + account=key).
type KeychainStore struct {
	Service string
	Exec    Executor
	GOOS    string
}

func (k KeychainStore) Name() string { return "keychain" }

func (k KeychainStore) Available() bool {
	goos := k.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	return goos == "darwin" && (k.Exec != nil || lookPath("security"))
}

func (k KeychainStore) service() string {
	if k.Service != "" {
		return k.Service
	}
	return "mobile-release"
}

func (k KeychainStore) exec() Executor {
	if k.Exec != nil {
		return k.Exec
	}
	return SystemExecutor
}

// keychainPrefix marks values written by setup: `security -i` reads its
// commands from stdin, so the value is stored base64-encoded to need no
// quoting and stay out of the process list.
const keychainPrefix = "mr-b64:"

func (k KeychainStore) Get(ctx context.Context, _ Spec, key Key) (string, error) {
	out, err := k.exec()(ctx, "security", []string{"find-generic-password", "-s", k.service(), "-a", key.String(), "-w"}, nil)
	if err != nil {
		return "", ErrNotFound
	}
	value := strings.TrimRight(string(out), "\r\n")
	if strings.HasPrefix(value, keychainPrefix) {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, keychainPrefix))
		if err != nil {
			return "", fmt.Errorf("keychain %s: %w", key, err)
		}
		return string(decoded), nil
	}
	return value, nil
}

func (k KeychainStore) Set(ctx context.Context, _ Spec, key Key, stored string) error {
	encoded := keychainPrefix + base64.StdEncoding.EncodeToString([]byte(stored))
	command := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", k.service(), key.String(), encoded)
	_, err := k.exec()(ctx, "security", []string{"-i"}, []byte(command))
	return err
}

// --- Generic command (get/set templates) ----------------------------------

// CommandStore runs user-configured commands: Get prints the value on
// stdout; Set receives it on stdin. Placeholders: {app} {env} {id} {key}.
type CommandStore struct {
	Label  string
	GetCmd []string
	SetCmd []string
	Exec   Executor
}

func (c CommandStore) Name() string { return c.Label }

func (c CommandStore) Available() bool {
	return len(c.GetCmd) > 0 && (c.Exec != nil || lookPath(c.GetCmd[0]))
}

func (c CommandStore) exec() Executor {
	if c.Exec != nil {
		return c.Exec
	}
	return SystemExecutor
}

func expand(template []string, key Key) []string {
	env := key.Env
	if env == "" {
		env = "shared"
	}
	replacer := strings.NewReplacer("{app}", key.App, "{env}", env, "{id}", key.ID, "{key}", key.String())
	out := make([]string, len(template))
	for i, part := range template {
		out[i] = replacer.Replace(part)
	}
	return out
}

func (c CommandStore) Get(ctx context.Context, _ Spec, key Key) (string, error) {
	argv := expand(c.GetCmd, key)
	out, err := c.exec()(ctx, argv[0], argv[1:], nil)
	if err != nil {
		return "", ErrNotFound
	}
	value := strings.TrimRight(string(out), "\r\n")
	if value == "" {
		return "", ErrNotFound
	}
	return value, nil
}

// CanWrite reports whether a set command is configured.
func (c CommandStore) CanWrite() bool { return len(c.SetCmd) > 0 }

func (c CommandStore) Set(ctx context.Context, _ Spec, key Key, stored string) error {
	if !c.CanWrite() {
		return fmt.Errorf("store %s has no set command", c.Label)
	}
	argv := expand(c.SetCmd, key)
	_, err := c.exec()(ctx, argv[0], argv[1:], []byte(stored))
	return err
}

// --- Bitwarden CLI ----------------------------------------------------------

// BitwardenStore reads items chosen once per key and remembered by item id
// in MapPath. A custom field named after the credential id wins, then the
// login password, then the notes.
type BitwardenStore struct {
	MapPath string
	Exec    Executor
	// Session is BW_SESSION after unlocking; passed through the environment.
	Session string
}

func (b *BitwardenStore) Name() string { return "bitwarden" }

func (b *BitwardenStore) Available() bool { return b.Exec != nil || lookPath("bw") }

func (b *BitwardenStore) exec() Executor {
	if b.Exec != nil {
		return b.Exec
	}
	session := b.Session
	return func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
		command := exec.CommandContext(ctx, name, args...)
		command.Env = os.Environ()
		if session != "" {
			command.Env = append(command.Env, "BW_SESSION="+session)
		}
		command.Stdin = bytes.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			return nil, fmt.Errorf("bw: %w: %s", err, firstLine(strings.TrimSpace(stderr.String())))
		}
		return stdout.Bytes(), nil
	}
}

// Status returns unauthenticated, locked or unlocked.
func (b *BitwardenStore) Status(ctx context.Context) string {
	out, err := b.exec()(ctx, "bw", []string{"status"}, nil)
	if err != nil {
		return "unauthenticated"
	}
	var status struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(out, &status) != nil {
		return "unauthenticated"
	}
	return status.Status
}

// Unlock exchanges the master password for a session. The password only
// reaches bw through the child's environment (--passwordenv), never argv.
func (b *BitwardenStore) Unlock(ctx context.Context, password string) error {
	if b.Exec != nil {
		out, err := b.Exec(ctx, "bw", []string{"unlock", "--raw", "--passwordenv", "MOBILE_RELEASE_BW_PASSWORD"}, []byte(password))
		if err != nil {
			return err
		}
		b.Session = strings.TrimSpace(string(out))
		return nil
	}
	command := exec.CommandContext(ctx, "bw", "unlock", "--raw", "--passwordenv", "MOBILE_RELEASE_BW_PASSWORD")
	command.Env = append(os.Environ(), "MOBILE_RELEASE_BW_PASSWORD="+password)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("bw unlock: %s", firstLine(strings.TrimSpace(stderr.String())))
	}
	b.Session = strings.TrimSpace(stdout.String())
	return nil
}

// Mapping is a remembered Bitwarden choice.
type Mapping struct {
	Item  string `toml:"item"`
	Field string `toml:"field,omitempty"`
}

func (b *BitwardenStore) loadMap() (map[string]Mapping, error) {
	mappings := map[string]Mapping{}
	data, err := os.ReadFile(b.MapPath)
	if errors.Is(err, os.ErrNotExist) {
		return mappings, nil
	}
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, &mappings); err != nil {
		return nil, fmt.Errorf("%s: %w", b.MapPath, err)
	}
	return mappings, nil
}

// Remember saves the item chosen for key (only the item id, never the value).
func (b *BitwardenStore) Remember(key Key, mapping Mapping) error {
	mappings, err := b.loadMap()
	if err != nil {
		return err
	}
	mappings[key.String()] = mapping
	data, err := toml.Marshal(mappings)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.MapPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(b.MapPath, data, 0o600)
}

// Forget drops every remembered choice.
func (b *BitwardenStore) Forget() error {
	err := os.Remove(b.MapPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Mapped returns the remembered choice for key, trying the shared key too.
func (b *BitwardenStore) Mapped(key Key) (Mapping, bool) {
	mappings, err := b.loadMap()
	if err != nil {
		return Mapping{}, false
	}
	if mapping, ok := mappings[key.String()]; ok {
		return mapping, true
	}
	return Mapping{}, false
}

// Item is a Bitwarden search result without secret fields.
type Item struct {
	ID       string
	Name     string
	Username string
	Fields   []string
}

// Search lists items matching term, exposing only names.
func (b *BitwardenStore) Search(ctx context.Context, term string) ([]Item, error) {
	out, err := b.exec()(ctx, "bw", []string{"list", "items", "--search", term}, nil)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Login *struct {
			Username string `json:"username"`
		} `json:"login"`
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("bw list: %w", err)
	}
	items := make([]Item, 0, len(raw))
	for _, entry := range raw {
		item := Item{ID: entry.ID, Name: entry.Name}
		if entry.Login != nil {
			item.Username = entry.Login.Username
		}
		for _, field := range entry.Fields {
			item.Fields = append(item.Fields, field.Name)
		}
		items = append(items, item)
	}
	return items, nil
}

func (b *BitwardenStore) Get(ctx context.Context, spec Spec, key Key) (string, error) {
	mapping, ok := b.Mapped(key)
	if !ok {
		return "", ErrNotFound
	}
	return b.Read(ctx, spec, mapping)
}

// Read extracts the value from a chosen item.
func (b *BitwardenStore) Read(ctx context.Context, spec Spec, mapping Mapping) (string, error) {
	out, err := b.exec()(ctx, "bw", []string{"get", "item", mapping.Item}, nil)
	if err != nil {
		return "", err
	}
	var item struct {
		Notes string `json:"notes"`
		Login *struct {
			Password string `json:"password"`
		} `json:"login"`
		Fields []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(out, &item); err != nil {
		return "", fmt.Errorf("bw get item: %w", err)
	}
	field := mapping.Field
	if field == "" {
		field = spec.ID
	}
	for _, entry := range item.Fields {
		if entry.Name == field && entry.Value != "" {
			return entry.Value, nil
		}
	}
	if mapping.Field == "" || mapping.Field == "password" {
		if item.Login != nil && item.Login.Password != "" {
			return item.Login.Password, nil
		}
	}
	if strings.TrimSpace(item.Notes) != "" {
		return strings.TrimSpace(item.Notes), nil
	}
	return "", fmt.Errorf("bitwarden item has no value for %s", spec.ID)
}

// Set creates a secure note named after the key and remembers it.
func (b *BitwardenStore) Set(ctx context.Context, spec Spec, key Key, stored string) error {
	note := map[string]any{
		"type":       2,
		"name":       "mobile-release: " + key.String(),
		"notes":      stored,
		"secureNote": map[string]int{"type": 0},
		"fields":     []any{},
	}
	payload, err := json.Marshal(note)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	out, err := b.exec()(ctx, "bw", []string{"create", "item"}, []byte(encoded))
	if err != nil {
		return err
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &created); err != nil || created.ID == "" {
		return fmt.Errorf("bw create item: unexpected response")
	}
	return b.Remember(key, Mapping{Item: created.ID, Field: "notes"})
}
