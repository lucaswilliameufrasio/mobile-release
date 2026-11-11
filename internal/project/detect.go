// Package project detects the project type and reads app identifiers so
// `mobile-release init` can propose a release.toml.
package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
)

// Info is what could be read from the project; empty fields were not found.
type Info struct {
	Type           string
	Preset         string
	Name           string
	AndroidPackage string
	IOSBundleID    string
	IOSWorkspace   string
	IOSProject     string
	IOSScheme      string
	HasAndroid     bool
	HasIOS         bool
	EnvFiles       []string
}

func exists(root string, parts ...string) bool {
	_, err := os.Stat(filepath.Join(append([]string{root}, parts...)...))
	return err == nil
}

func read(root string, parts ...string) string {
	data, err := os.ReadFile(filepath.Join(append([]string{root}, parts...)...))
	if err != nil {
		return ""
	}
	return string(data)
}

// Detect returns the project type, or "" when unknown.
func Detect(root string) string {
	return Inspect(root).Type
}

// Inspect reads the project at root.
func Inspect(root string) Info {
	var info Info
	switch {
	case exists(root, "pubspec.yaml"):
		info = inspectFlutter(root)
	case isExpo(root):
		info = inspectExpo(root)
	case exists(root, "jirau.toml"):
		info = inspectJirau(root)
	case isKMP(root):
		info = inspectKMP(root)
	case exists(root, "android") || exists(root, "ios"):
		info = inspectNative(root)
	default:
		return Info{}
	}
	info.EnvFiles = envFiles(root)
	return info
}

func isExpo(root string) bool {
	if exists(root, "app.config.ts") || exists(root, "app.config.js") {
		return true
	}
	if !exists(root, "app.json") {
		return false
	}
	return strings.Contains(read(root, "package.json"), `"expo"`) || strings.Contains(read(root, "app.json"), `"expo"`)
}

func isKMP(root string) bool {
	for _, name := range []string{"settings.gradle.kts", "settings.gradle"} {
		if exists(root, name) && !exists(root, "android") {
			return true
		}
	}
	return false
}

var (
	pubspecName      = regexp.MustCompile(`(?m)^name:\s*([A-Za-z0-9_]+)`)
	applicationID    = regexp.MustCompile(`applicationId\s*=?\s*"([^"]+)"`)
	namespacePattern = regexp.MustCompile(`namespace\s*=?\s*"([^"]+)"`)
	bundleIDPattern  = regexp.MustCompile(`PRODUCT_BUNDLE_IDENTIFIER = "?([A-Za-z0-9.\-]+)"?;`)
)

func inspectFlutter(root string) Info {
	info := Info{
		Type:         config.TypeFlutter,
		HasAndroid:   exists(root, "android"),
		HasIOS:       exists(root, "ios"),
		IOSWorkspace: "ios/Runner.xcworkspace",
		IOSScheme:    "Runner",
	}
	if match := pubspecName.FindStringSubmatch(read(root, "pubspec.yaml")); match != nil {
		info.Name = match[1]
	}
	info.AndroidPackage = gradleApplicationID(root, "android/app")
	info.IOSBundleID = pbxprojBundleID(read(root, "ios", "Runner.xcodeproj", "project.pbxproj"))
	return info
}

func inspectExpo(root string) Info {
	info := Info{Type: config.TypeExpo, HasAndroid: true, HasIOS: true}
	var app struct {
		Expo struct {
			Name    string `json:"name"`
			Android struct {
				Package string `json:"package"`
			} `json:"android"`
			IOS struct {
				BundleIdentifier string `json:"bundleIdentifier"`
			} `json:"ios"`
		} `json:"expo"`
	}
	if json.Unmarshal([]byte(read(root, "app.json")), &app) == nil {
		info.Name = app.Expo.Name
		info.AndroidPackage = app.Expo.Android.Package
		info.IOSBundleID = app.Expo.IOS.BundleIdentifier
	}
	return info
}

func inspectJirau(root string) Info {
	info := inspectNative(root)
	info.Preset = config.PresetJirau
	var manifest struct {
		App struct {
			Name      string   `toml:"name"`
			PackageID string   `toml:"package_id"`
			Platforms []string `toml:"platforms"`
		} `toml:"app"`
	}
	if toml.Unmarshal([]byte(read(root, "jirau.toml")), &manifest) == nil {
		info.Name = manifest.App.Name
		if info.AndroidPackage == "" {
			info.AndroidPackage = manifest.App.PackageID
		}
		if info.IOSBundleID == "" {
			info.IOSBundleID = manifest.App.PackageID
		}
		if len(manifest.App.Platforms) > 0 {
			info.HasAndroid = contains(manifest.App.Platforms, "android")
			info.HasIOS = contains(manifest.App.Platforms, "ios")
		}
	}
	// XcodeGen: the project and scheme are named after project.yml's name.
	if match := regexp.MustCompile(`(?m)^name:\s*(\S+)`).FindStringSubmatch(read(root, "ios", "project.yml")); match != nil {
		info.IOSProject = "ios/" + match[1] + ".xcodeproj"
		info.IOSWorkspace = ""
		info.IOSScheme = match[1]
	}
	return info
}

func inspectKMP(root string) Info {
	info := Info{Type: config.TypeKMP, HasAndroid: true}
	for _, module := range []string{"composeApp", "androidApp", "app"} {
		if id := gradleApplicationID(root, module); id != "" {
			info.AndroidPackage = id
			break
		}
	}
	if project := firstMatch(root, "iosApp/*.xcodeproj"); project != "" {
		info.HasIOS = true
		info.IOSProject = project
		info.IOSScheme = strings.TrimSuffix(filepath.Base(project), ".xcodeproj")
		info.IOSBundleID = pbxprojBundleID(read(root, project, "project.pbxproj"))
	}
	return info
}

func inspectNative(root string) Info {
	info := Info{Type: config.TypeNative, HasAndroid: exists(root, "android"), HasIOS: exists(root, "ios")}
	info.Name = filepath.Base(root)
	info.AndroidPackage = gradleApplicationID(root, "android/app")
	if workspace := firstMatch(root, "ios/*.xcworkspace"); workspace != "" {
		info.IOSWorkspace = workspace
		info.IOSScheme = strings.TrimSuffix(filepath.Base(workspace), ".xcworkspace")
	} else if project := firstMatch(root, "ios/*.xcodeproj"); project != "" {
		info.IOSProject = project
		info.IOSScheme = strings.TrimSuffix(filepath.Base(project), ".xcodeproj")
		info.IOSBundleID = pbxprojBundleID(read(root, project, "project.pbxproj"))
	}
	return info
}

func gradleApplicationID(root, module string) string {
	for _, name := range []string{"build.gradle.kts", "build.gradle"} {
		text := read(root, module, name)
		if match := applicationID.FindStringSubmatch(text); match != nil {
			return match[1]
		}
		if match := namespacePattern.FindStringSubmatch(text); match != nil {
			return match[1]
		}
	}
	return ""
}

// pbxprojBundleID returns the most common app bundle id, ignoring test and
// extension targets that share its prefix.
func pbxprojBundleID(pbxproj string) string {
	counts := map[string]int{}
	for _, match := range bundleIDPattern.FindAllStringSubmatch(pbxproj, -1) {
		id := match[1]
		if strings.Contains(strings.ToLower(id), "test") || strings.Contains(id, "$(") {
			continue
		}
		counts[id]++
	}
	best := ""
	for id, count := range counts {
		if best == "" || count > counts[best] || (count == counts[best] && len(id) < len(best)) {
			best = id
		}
	}
	return best
}

func firstMatch(root, pattern string) string {
	matches, _ := filepath.Glob(filepath.Join(root, pattern))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	relative, err := filepath.Rel(root, matches[0])
	if err != nil {
		return ""
	}
	return filepath.ToSlash(relative)
}

func envFiles(root string) []string {
	matches, _ := filepath.Glob(filepath.Join(root, ".env*"))
	var files []string
	for _, match := range matches {
		name := filepath.Base(match)
		if strings.HasSuffix(name, ".example") || strings.HasSuffix(name, ".sample") {
			continue
		}
		if info, err := os.Stat(match); err == nil && !info.IsDir() {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
