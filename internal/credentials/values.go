package credentials

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Key addresses a stored value: per app, optionally per environment.
type Key struct {
	App string
	Env string // empty for values shared by every environment
	ID  string
}

func (k Key) String() string {
	if k.Env == "" {
		return k.App + "/" + k.ID
	}
	return k.App + "/" + k.Env + "/" + k.ID
}

// Value is a resolved credential.
type Value struct {
	Spec   Spec
	Source string
	// Data holds the text, or the file bytes for File specs.
	Data []byte
	// Path is set for File specs given as an existing file (CI *_PATH).
	Path string
}

// Set holds the resolved values of one run.
type Set struct {
	values map[string]Value
	paths  map[string]string
}

// NewSet returns an empty set.
func NewSet() *Set {
	return &Set{values: map[string]Value{}, paths: map[string]string{}}
}

// Put stores a value.
func (s *Set) Put(value Value) {
	s.values[value.Spec.ID] = value
}

// Has reports whether id was resolved.
func (s *Set) Has(id string) bool {
	_, ok := s.values[id]
	return ok
}

// Sources maps credential id to the store it came from, for summaries.
func (s *Set) Sources() map[string]string {
	sources := make(map[string]string, len(s.values))
	for id, value := range s.values {
		sources[id] = value.Source
	}
	return sources
}

// Materialize writes File values into dir (0700) as 0600 files.
func (s *Set) Materialize(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ids := make([]string, 0, len(s.values))
	for id := range s.values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		value := s.values[id]
		if value.Spec.Kind != File {
			continue
		}
		if value.Path != "" {
			s.paths[id] = value.Path
			continue
		}
		path := filepath.Join(dir, strings.ReplaceAll(id, ".", "-")+value.Spec.Extension)
		if err := os.WriteFile(path, value.Data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", id, err)
		}
		s.paths[id] = path
	}
	return nil
}

func (s *Set) text(id string) string {
	return string(s.values[id].Data)
}

// Path returns the materialized path of a File credential.
func (s *Set) Path(id string) string {
	return s.paths[id]
}

// LaneEnv is the environment for the shared fastlane lanes.
func (s *Set) LaneEnv() []string {
	var env []string
	for _, id := range []string{AppStoreKeyID, AppStoreIssuerID, MatchGitURL, MatchPassword, ExpoToken} {
		if s.Has(id) {
			env = append(env, specs[id].EnvName+"="+s.text(id))
		}
	}
	for _, id := range []string{PlayServiceAccount, FirebaseServiceAccount, AppStorePrivateKey} {
		if path := s.Path(id); path != "" {
			env = append(env, specs[id].EnvName+"_PATH="+path)
		}
	}
	// Variables read natively by fastlane match and the Expo CLI.
	if s.Has(MatchGitURL) {
		env = append(env, "MATCH_GIT_URL="+s.text(MatchGitURL))
	}
	if s.Has(MatchPassword) {
		env = append(env, "MATCH_PASSWORD="+s.text(MatchPassword))
	}
	if s.Has(ExpoToken) {
		env = append(env, "EXPO_TOKEN="+s.text(ExpoToken))
	}
	return env
}

// GradleEnv injects the release signing into any Gradle build through
// ORG_GRADLE_PROJECT_* variables, so passwords never appear as arguments.
func (s *Set) GradleEnv() []string {
	path := s.Path(AndroidKeystore)
	if path == "" || !s.Has(AndroidKeystorePassword) || !s.Has(AndroidKeyAlias) || !s.Has(AndroidKeyPassword) {
		return nil
	}
	return []string{
		"ORG_GRADLE_PROJECT_android.injected.signing.store.file=" + path,
		"ORG_GRADLE_PROJECT_android.injected.signing.store.password=" + s.text(AndroidKeystorePassword),
		"ORG_GRADLE_PROJECT_android.injected.signing.key.alias=" + s.text(AndroidKeyAlias),
		"ORG_GRADLE_PROJECT_android.injected.signing.key.password=" + s.text(AndroidKeyPassword),
	}
}

// EncodeStored turns a value into the single-line form kept in stores:
// files are base64; text is kept as is.
func EncodeStored(spec Spec, data []byte) string {
	if spec.Kind == File {
		return base64.StdEncoding.EncodeToString(data)
	}
	return string(data)
}

// decodeStored reverses encodeStored.
func decodeStored(spec Spec, stored string) ([]byte, error) {
	stored = strings.TrimSpace(stored)
	if spec.Kind != File {
		return []byte(stored), nil
	}
	data, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return nil, fmt.Errorf("%s: stored value is not base64: %w", spec.ID, err)
	}
	return data, nil
}
