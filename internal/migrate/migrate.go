// Package migrate brings a release.toml to the current schema and style.
//
// Schema v1 files are rewritten as v2 (comments cannot be kept). Schema v2
// files are edited line by line: keys whose value equals the default are
// removed and everything else, comments included, is kept. Every result is
// checked to resolve to exactly the same effective configuration.
package migrate

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

// Finding is one thing migrate changes or reports.
type Finding struct {
	Line    int // 1-based, in the original file; 0 for whole-file changes
	Key     string
	Message string
	// Fixed is true when the change is applied automatically.
	Fixed bool
}

// Result is the outcome of a migration.
type Result struct {
	From     int
	Findings []Finding
	// Content is the migrated file; equal to the input when nothing changes.
	Content []byte
	// Rewritten means comments could not be kept (schema v1).
	Rewritten bool
}

// Changed reports whether the file changes.
func (r Result) Changed(original []byte) bool {
	return !bytes.Equal(r.Content, original)
}

const v1Header = "# Migrated from schema v1 by `mobile-release migrate`. Comments from the\n# previous file could not be kept.\n\n"

// Run migrates data.
func Run(data []byte) (Result, error) {
	var probe struct {
		SchemaVersion int `toml:"schema_version"`
	}
	if err := toml.Unmarshal(data, &probe); err != nil {
		return Result{}, fmt.Errorf("release.toml: %w", err)
	}
	original, err := config.Parse(data)
	if err != nil {
		return Result{}, err
	}
	result := Result{From: probe.SchemaVersion, Content: data}
	if probe.SchemaVersion == 1 {
		marshaled, err := config.Marshal(original)
		if err != nil {
			return Result{}, err
		}
		result.Content = append([]byte(v1Header), marshaled...)
		result.Rewritten = true
		result.Findings = append(result.Findings, Finding{Message: fmt.Sprintf("schema 1 → %d (the file is rewritten)", config.CurrentSchemaVersion), Fixed: true})
	}

	cleaned, findings, err := removeDefaults(result.Content)
	if err != nil {
		return Result{}, err
	}
	result.Content = cleaned
	if !result.Rewritten {
		result.Findings = append(result.Findings, findings...)
	}
	result.Findings = append(result.Findings, advice(result.Content)...)

	if err := sameEffective(data, result.Content); err != nil {
		return Result{}, err
	}
	return result, nil
}

// sameEffective refuses any migration that changes the resolved config.
func sameEffective(before, after []byte) error {
	resolve := func(data []byte) (string, error) {
		c, err := config.Parse(data)
		if err != nil {
			return "", err
		}
		c.ApplyDefaults()
		// android.track only feeds each environment's play.track default,
		// which is compared through the environments.
		c.Android.Track = ""
		out, err := config.Marshal(c)
		return string(out), err
	}
	left, err := resolve(before)
	if err != nil {
		return err
	}
	right, err := resolve(after)
	if err != nil {
		return fmt.Errorf("migrated file is invalid: %w", err)
	}
	if left != right {
		return fmt.Errorf("refusing to migrate: the result would change the effective configuration")
	}
	return nil
}

var (
	tableHeader = regexp.MustCompile(`^\s*\[([^\[\]]+)\]\s*(#.*)?$`)
	keyValue    = regexp.MustCompile(`^\s*([A-Za-z0-9_.\-"']+)\s*=\s*(.+?)\s*$`)
)

// defaults maps dotted key paths to their default values for c.
func defaults(c config.Config) map[string]any {
	values := map[string]any{
		"artifacts.directory": "dist",
		"android.signing":     config.AndroidSigningInjected,
		"android.enabled":     true,
		"ios.enabled":         true,
		"ios.configuration":   "Release",
		"ios.signing":         config.SigningAutomatic,
		"android.track":       "internal",
	}
	switch c.Project.Type {
	case config.TypeFlutter:
		values["flutter.obfuscate"] = true
		values["flutter.split_debug_info"] = "build/symbols"
		if c.IOS.Project == "" {
			values["ios.workspace"] = "ios/Runner.xcworkspace"
		}
		if c.IOS.Workspace == "" || c.IOS.Workspace == "ios/Runner.xcworkspace" {
			values["ios.scheme"] = "Runner"
		}
	case config.TypeExpo, config.TypeNative:
		values["android.project_dir"] = "android"
	case config.TypeKMP:
		values["android.project_dir"] = "."
	}
	if c.Project.Preset == config.PresetJirau {
		values["ios.xcodegen_spec"] = "ios/project.yml"
	}
	derivedID := c
	derivedID.App.ID = ""
	derivedID.ApplyDefaults()
	if derivedID.App.ID != "" {
		values["app.id"] = derivedID.App.ID
	}
	track := c.Android.Track
	if track == "" {
		track = "internal"
	}
	for name, env := range c.Environments {
		prefix := "environments." + name + "."
		values[prefix+"build_number"] = config.BuildNumberTimestamp
		values[prefix+"play.track"] = track
		values[prefix+"play.release_status"] = "completed"
		values[prefix+"ota.mode"] = "eas"
		values[prefix+"ota.channel"] = name
		if len(env.Firebase.Testers) == 0 {
			values[prefix+"firebase.groups"] = []any{"internal-testers"}
		}
	}
	return values
}

func unquote(key string) string {
	parts := strings.Split(key, ".")
	for i, part := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(part), `"'`)
	}
	return strings.Join(parts, ".")
}

// removeDefaults drops single-line keys equal to their default (unless a
// comment documents them) and tables left completely empty, keeping every
// other line untouched.
func removeDefaults(data []byte) ([]byte, []Finding, error) {
	c, err := config.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	known := defaults(c)
	lines := strings.Split(string(data), "\n")
	keep := make([]bool, len(lines))
	var findings []Finding
	table := ""
	for i, line := range lines {
		keep[i] = true
		if match := tableHeader.FindStringSubmatch(line); match != nil {
			table = unquote(match[1])
			continue
		}
		match := keyValue.FindStringSubmatch(line)
		if match == nil || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		path := unquote(match[1])
		if table != "" {
			path = table + "." + path
		}
		want, ok := known[path]
		if !ok {
			continue
		}
		var parsed map[string]any
		if err := toml.Unmarshal([]byte("v = "+match[2]), &parsed); err != nil {
			continue // multi-line values and the like are left alone
		}
		if !reflect.DeepEqual(parsed["v"], want) {
			continue
		}
		// A commented key documents a deliberate choice: keep it.
		if strings.Contains(match[2], "#") || (i > 0 && strings.HasPrefix(strings.TrimSpace(lines[i-1]), "#")) {
			findings = append(findings, Finding{Line: i + 1, Key: path, Message: "equals the default; kept because it is commented"})
			continue
		}
		{
			keep[i] = false
			findings = append(findings, Finding{Line: i + 1, Key: path, Message: "equals the default; removed", Fixed: true})
		}
	}
	dropEmptyTables(lines, keep)
	var out []string
	for i, line := range lines {
		if keep[i] {
			out = append(out, line)
		}
	}
	text := collapseBlankLines(strings.Join(out, "\n"))
	return []byte(text), findings, nil
}

// dropEmptyTables removes headers whose section lost every key and has no
// comments or subtables content of its own.
func dropEmptyTables(lines []string, keep []bool) {
	for i, line := range lines {
		if !keep[i] || tableHeader.FindStringSubmatch(line) == nil {
			continue
		}
		hadRemoved, hasContent := false, false
		for j := i + 1; j < len(lines); j++ {
			if tableHeader.FindStringSubmatch(lines[j]) != nil {
				break
			}
			trimmed := strings.TrimSpace(lines[j])
			if !keep[j] {
				hadRemoved = true
				continue
			}
			if trimmed != "" {
				hasContent = true
				break
			}
		}
		if hadRemoved && !hasContent {
			keep[i] = false
		}
	}
}

func collapseBlankLines(text string) string {
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return text
}

// advice reports what migrate cannot change automatically.
func advice(data []byte) []Finding {
	var findings []Finding
	c, err := config.Parse(data)
	if err != nil {
		return nil
	}
	if c.Android.Track != "" && c.Android.Track != "internal" {
		findings = append(findings, Finding{Key: "android.track", Message: "kept for schema 1 compatibility; prefer environments.<name>.play.track"})
	}
	c.ApplyDefaults()
	var invalid *config.ValidationError
	if err := c.Validate(); errors.As(err, &invalid) {
		for _, problem := range invalid.Problems {
			findings = append(findings, Finding{Message: "to do: " + problem})
		}
	}
	return findings
}
