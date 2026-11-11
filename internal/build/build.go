// Package build holds values shared by every provider for one build.
package build

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
)

// Kind is the artifact being produced.
type Kind string

const (
	APK          Kind = "apk"
	AAB          Kind = "aab"
	IPA          Kind = "ipa"
	IOSSimulator Kind = "ios-simulator"
)

// Context is everything a provider needs for one build.
type Context struct {
	Config      config.Config
	Environment *config.Environment
	// BuildNumber is empty when the project's own number is kept.
	BuildNumber string
	Commit      string
	// Defines are resolved compile-time values (Dart defines for Flutter,
	// environment variables for other providers).
	Defines map[string]string
	// EnvFileVars are the KEY=VALUE pairs from the environment's env_file,
	// passed as process environment to non-Flutter builds.
	EnvFileVars []string
	// GradleEnv carries ORG_GRADLE_PROJECT_* signing and version values.
	GradleEnv []string
	// Release is false for QA builds, which skip release signing.
	Release bool
}

// EnvName returns the environment name or "qa".
func (c Context) EnvName() string {
	if c.Environment == nil {
		return "qa"
	}
	return c.Environment.Name
}

// TimestampBuildNumber is UTC yyDDDHHMM, e.g. 263141530: it only grows,
// needs no store access and stays below Android's 2,100,000,000 versionCode
// limit until 2099.
func TimestampBuildNumber(now time.Time) string {
	return now.UTC().Format("06") + fmt.Sprintf("%03d", now.UTC().YearDay()) + now.UTC().Format("1504")
}

// ExpandDefines replaces {commit}, {build_number} and {env} in define values.
func ExpandDefines(defines map[string]string, commit, buildNumber, env string) map[string]string {
	replacer := strings.NewReplacer("{commit}", commit, "{build_number}", buildNumber, "{env}", env)
	expanded := make(map[string]string, len(defines))
	for key, value := range defines {
		expanded[key] = replacer.Replace(value)
	}
	return expanded
}

// SortedPairs renders a map as sorted KEY=VALUE pairs.
func SortedPairs(values map[string]string) []string {
	pairs := make([]string, 0, len(values))
	for key, value := range values {
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)
	return pairs
}

// ReadEnvFile parses KEY=VALUE lines, ignoring blanks and # comments and
// stripping matching surrounding quotes. Values are never logged.
func ReadEnvFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pairs []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		key, value, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, line)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		pairs = append(pairs, key+"="+value)
	}
	return pairs, scanner.Err()
}
