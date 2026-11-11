package credentials

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

// memory is a writable in-memory store.
type memory struct {
	name   string
	values map[string]string
}

func (m *memory) Name() string    { return m.name }
func (m *memory) Available() bool { return true }
func (m *memory) Get(_ context.Context, _ Spec, key Key) (string, error) {
	if value, ok := m.values[key.String()]; ok {
		return value, nil
	}
	return "", ErrNotFound
}
func (m *memory) Set(_ context.Context, _ Spec, key Key, stored string) error {
	m.values[key.String()] = stored
	return nil
}

func spec(t *testing.T, id string) Spec {
	t.Helper()
	s, err := Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestResolverPriorityAndEnvironmentFallback(t *testing.T) {
	first := &memory{name: "first", values: map[string]string{
		"app/" + AndroidKeyAlias: "from-first",
	}}
	second := &memory{name: "second", values: map[string]string{
		"app/" + AndroidKeyAlias:                   "from-second",
		"app/production/" + FirebaseServiceAccount: base64.StdEncoding.EncodeToString([]byte(`{"env":"prd"}`)),
		"app/" + FirebaseServiceAccount:            base64.StdEncoding.EncodeToString([]byte(`{"env":"shared"}`)),
	}}
	resolver := Resolver{Stores: []Store{first, second}}
	result, err := resolver.Resolve(context.Background(),
		[]Spec{spec(t, AndroidKeyAlias), spec(t, FirebaseServiceAccount), spec(t, MatchPassword)},
		"app", "production")
	if err != nil {
		t.Fatal(err)
	}
	sources := result.Set.Sources()
	if sources[AndroidKeyAlias] != "first" || string(result.Set.values[AndroidKeyAlias].Data) != "from-first" {
		t.Fatalf("priority broken: %v", sources)
	}
	if string(result.Set.values[FirebaseServiceAccount].Data) != `{"env":"prd"}` {
		t.Fatal("environment-specific value must win over the shared one")
	}
	if len(result.Missing) != 1 || result.Missing[0].ID != MatchPassword {
		t.Fatalf("missing = %v", result.Missing)
	}
}

func TestEnvStoreForCI(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "play.json")
	if err := os.WriteFile(keyPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"MOBILE_RELEASE_ANDROID_KEY_ALIAS":       "upload",
		"MOBILE_RELEASE_GOOGLE_PLAY_JSON_PATH":   keyPath,
		"MOBILE_RELEASE_ANDROID_KEYSTORE_BASE64": base64.StdEncoding.EncodeToString([]byte("JKS")),
	}
	store := EnvStore{Lookup: func(key string) (string, bool) { value, ok := values[key]; return value, ok }}
	result, err := Resolver{Stores: []Store{store}}.Resolve(context.Background(),
		[]Spec{spec(t, AndroidKeyAlias), spec(t, PlayServiceAccount), spec(t, AndroidKeystore)}, "app", "")
	if err != nil || len(result.Missing) != 0 {
		t.Fatalf("err=%v missing=%v", err, result.Missing)
	}
	if err := result.Set.Materialize(filepath.Join(dir, "secrets")); err != nil {
		t.Fatal(err)
	}
	if result.Set.Path(PlayServiceAccount) != keyPath {
		t.Fatal("existing *_PATH files are used in place")
	}
	data, err := os.ReadFile(result.Set.Path(AndroidKeystore))
	if err != nil || string(data) != "JKS" {
		t.Fatalf("keystore = %q err=%v", data, err)
	}
	info, _ := os.Stat(result.Set.Path(AndroidKeystore))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("materialized file mode = %v", info.Mode())
	}
}

type recorded struct {
	name  string
	args  []string
	stdin string
}

func recorder(outputs map[string]string, calls *[]recorded) Executor {
	return func(_ context.Context, name string, args []string, stdin []byte) ([]byte, error) {
		*calls = append(*calls, recorded{name, args, string(stdin)})
		key := name + " " + strings.Join(args, " ")
		if out, ok := outputs[key]; ok {
			return []byte(out), nil
		}
		return nil, nil
	}
}

func TestKeychainRoundTripKeepsValuesOutOfArgs(t *testing.T) {
	var calls []recorded
	outputs := map[string]string{}
	keychain := KeychainStore{Exec: recorder(outputs, &calls), GOOS: "darwin"}
	key := Key{App: "app", ID: MatchPassword}
	if err := keychain.Set(context.Background(), spec(t, MatchPassword), key, `pa ss"word`); err != nil {
		t.Fatal(err)
	}
	set := calls[0]
	if set.name != "security" || strings.Join(set.args, " ") != "-i" {
		t.Fatalf("set must use security -i, got %v", set)
	}
	if strings.Contains(set.stdin, "pa ss") {
		t.Fatal("value must be encoded on stdin")
	}
	stored := strings.Fields(set.stdin)[len(strings.Fields(set.stdin))-1]
	outputs["security find-generic-password -s mobile-release -a app/match.password -w"] = stored + "\n"
	value, err := keychain.Get(context.Background(), spec(t, MatchPassword), key)
	if err != nil || value != `pa ss"word` {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if (KeychainStore{GOOS: "linux", Exec: recorder(nil, &calls)}).Available() {
		t.Fatal("keychain is macOS only")
	}
}

func TestBitwardenChoiceIsRemembered(t *testing.T) {
	var calls []recorded
	outputs := map[string]string{
		"bw list items --search keystore": `[{"id":"i1","name":"Upload key","login":{"username":"ci"},"fields":[{"name":"android.keystore_password"}]}]`,
		"bw get item i1":                  `{"login":{"password":"from-login"},"fields":[{"name":"android.keystore_password","value":"from-field"}],"notes":"n"}`,
		"bw create item":                  `{"id":"created"}`,
	}
	store := &BitwardenStore{MapPath: filepath.Join(t.TempDir(), "bitwarden.toml"), Exec: recorder(outputs, &calls)}
	items, err := store.Search(context.Background(), "keystore")
	if err != nil || len(items) != 1 || items[0].Username != "ci" {
		t.Fatalf("items=%v err=%v", items, err)
	}
	key := Key{App: "app", ID: AndroidKeystorePassword}
	if _, err := store.Get(context.Background(), spec(t, AndroidKeystorePassword), key); err != ErrNotFound {
		t.Fatal("unmapped keys are not found")
	}
	if err := store.Remember(key, Mapping{Item: "i1"}); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(context.Background(), spec(t, AndroidKeystorePassword), key)
	if err != nil || value != "from-field" {
		t.Fatalf("custom field named after the id wins, got %q err=%v", value, err)
	}
	info, _ := os.Stat(store.MapPath)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("map mode = %v", info.Mode())
	}
	if err := store.Set(context.Background(), spec(t, MatchPassword), Key{App: "app", ID: MatchPassword}, "secret-value"); err != nil {
		t.Fatal(err)
	}
	create := calls[len(calls)-1]
	if strings.Contains(strings.Join(create.args, " "), "secret") {
		t.Fatal("created item must travel on stdin")
	}
	payload, _ := base64.StdEncoding.DecodeString(create.stdin)
	if !strings.Contains(string(payload), "secret-value") || !strings.Contains(string(payload), "mobile-release: app/match.password") {
		t.Fatalf("payload = %s", payload)
	}
	if mapping, ok := store.Mapped(Key{App: "app", ID: MatchPassword}); !ok || mapping.Item != "created" {
		t.Fatalf("created item not remembered: %+v", mapping)
	}
}

func TestCommandStorePlaceholders(t *testing.T) {
	var calls []recorded
	store := CommandStore{
		Label:  "vault",
		GetCmd: []string{"vault-cli", "read", "{app}/{env}/{id}"},
		SetCmd: []string{"vault-cli", "write", "{key}"},
		Exec:   recorder(map[string]string{"vault-cli read app/shared/match.password": "pw\n"}, &calls),
	}
	value, err := store.Get(context.Background(), spec(t, MatchPassword), Key{App: "app", ID: MatchPassword})
	if err != nil || value != "pw" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if err := store.Set(context.Background(), spec(t, MatchPassword), Key{App: "app", Env: "production", ID: MatchPassword}, "new"); err != nil {
		t.Fatal(err)
	}
	last := calls[len(calls)-1]
	if strings.Join(last.args, " ") != "write app/production/match.password" || last.stdin != "new" {
		t.Fatalf("set call = %+v", last)
	}
}

func TestRequiredFollowsDestinations(t *testing.T) {
	parse := func(toml string) (config.Config, *config.Environment) {
		c, err := config.Parse([]byte(toml))
		if err != nil {
			t.Fatal(err)
		}
		c.ApplyDefaults()
		env, _ := c.Environment("main")
		return c, env
	}
	ids := func(specs []Spec) string {
		var out []string
		for _, s := range specs {
			out = append(out, s.ID)
		}
		return strings.Join(out, ",")
	}
	base := `schema_version = 2
[project]
type = "flutter"
[android]
package_name = "a"
[ios]
bundle_id = "a"
team_id = "ABCDE12345"
`
	c, env := parse(base + `signing = "match"
[environments.main]
destinations = ["firebase"]
[environments.main.firebase]
android_app_id = "x"
ios_app_id = "y"
`)
	if got := ids(Required(c, env, Platforms{Android: true, IOS: true})); got != "android.key_alias,android.key_password,android.keystore,android.keystore_password,firebase.service_account,match.git_url,match.password" {
		t.Fatalf("firebase both: %s", got)
	}
	if got := ids(Required(c, env, Platforms{IOS: true})); got != "firebase.service_account,match.git_url,match.password" {
		t.Fatalf("firebase ios only: %s", got)
	}

	c, env = parse(base + `[environments.main]
build_number = "store"
destinations = ["play", "testflight"]
`)
	if got := ids(Required(c, env, Platforms{Android: true, IOS: true})); got != "android.key_alias,android.key_password,android.keystore,android.keystore_password,appstore.issuer_id,appstore.key_id,appstore.private_key,play.service_account" {
		t.Fatalf("stores: %s", got)
	}
}

func TestUserConfigOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[stores.injector]
type = "exec"
command = ["tool", "run", "--env", "{env}", "--"]
env_names = { production = "prd" }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	user, err := LoadUserConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(user.Order, ",") != "keychain,injector,bitwarden,env" {
		t.Fatalf("default order = %v", user.Order)
	}
	_, execs := user.Build(filepath.Join(t.TempDir(), "bw.toml"))
	if len(execs) != 1 {
		t.Fatalf("execs = %v", execs)
	}
	argv := execs[0].Argv("production", []string{"/bin/mobile-release", "deploy"})
	if strings.Join(argv, " ") != "tool run --env prd -- /bin/mobile-release deploy" {
		t.Fatalf("argv = %v", argv)
	}

	if err := os.WriteFile(path, []byte(`order = ["missing"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUserConfig(path); err == nil {
		t.Fatal("unknown store in order must fail")
	}
}

func TestGradleEnvNeedsEverySigningValue(t *testing.T) {
	set := NewSet()
	set.Put(Value{Spec: specs[AndroidKeystore], Data: []byte("JKS")})
	if err := set.Materialize(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if set.GradleEnv() != nil {
		t.Fatal("partial signing must not be injected")
	}
}
