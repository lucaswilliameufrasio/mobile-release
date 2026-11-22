// Package provisioning compares the iOS signing state declared in
// release.toml (App IDs, capabilities and provisioning profiles) with App
// Store Connect, and applies the difference without ever leaving an app
// without a valid profile when it can be avoided.
package provisioning

import (
	"context"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/appstore"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

// API is the subset of the App Store Connect client used here.
type API interface {
	FindBundleID(ctx context.Context, identifier string) (*appstore.BundleID, error)
	CreateBundleID(ctx context.Context, identifier, name string) (appstore.BundleID, error)
	Capabilities(ctx context.Context, bundleID string) ([]appstore.Capability, error)
	EnableCapability(ctx context.Context, bundleID, capabilityType string, settings []appstore.Setting) (appstore.Capability, error)
	Profiles(ctx context.Context, bundleID string) ([]appstore.Profile, error)
	ProfileCertificateIDs(ctx context.Context, profileID string) ([]string, error)
	ProfileDeviceIDs(ctx context.Context, profileID string) ([]string, error)
	Certificates(ctx context.Context, types []string) ([]appstore.Certificate, error)
	Devices(ctx context.Context) ([]appstore.Device, error)
	CreateProfile(ctx context.Context, request appstore.CreateProfileRequest) (appstore.Profile, error)
	DeleteProfile(ctx context.Context, profileID string) error
}

// Capability is a capability to enable on an App ID.
type Capability struct {
	Type     string
	Label    string
	Settings []appstore.Setting
	// Note is shown with the capability: what the API cannot do for it.
	Note string
}

// Target is one bundle id: the app or one of its extensions.
type Target struct {
	Name         string
	BundleID     string
	Capabilities []Capability
}

// Desired is the declared state of one app.
type Desired struct {
	Name     string
	Root     string
	TeamID   string
	Targets  []Target
	Kinds    []string // development, ad-hoc, app-store
	Strategy string   // api, match, none
	// Directory receives downloaded profiles (absolute).
	Directory string
	Install   bool
}

// FromConfig builds the desired state of a project. envName limits the
// profile types to one environment (empty: every environment).
func FromConfig(root string, c config.Config, envName string) Desired {
	desired := Desired{
		Name:      c.App.Name,
		Root:      root,
		TeamID:    c.IOS.TeamID,
		Kinds:     c.ProfileTypes(envName),
		Strategy:  c.ProvisioningStrategy(),
		Directory: filepath.Join(root, c.ProfilesDirectory()),
		Install:   c.InstallProfiles(),
	}
	if c.IOS.BundleID == "" {
		return desired
	}
	desired.Targets = append(desired.Targets, Target{Name: firstNonEmpty(c.App.Name, c.IOS.BundleID), BundleID: c.IOS.BundleID, Capabilities: capabilities(c.IOS.Capabilities)})
	for _, extension := range c.IOS.Extensions {
		desired.Targets = append(desired.Targets, Target{
			Name:         firstNonEmpty(extension.Name, nameFromIdentifier(c.App.Name, extension.BundleID)),
			BundleID:     extension.BundleID,
			Capabilities: capabilities(extension.Capabilities),
		})
	}
	return desired
}

func capabilities(declared config.Capabilities) []Capability {
	var list []Capability
	if len(declared.AssociatedDomains) > 0 {
		list = append(list, Capability{
			Type: "ASSOCIATED_DOMAINS", Label: "Associated Domains",
			Note: "domains go in the entitlements file (com.apple.developer.associated-domains): " + strings.Join(declared.AssociatedDomains, ", "),
		})
	}
	if len(declared.AppGroups) > 0 {
		list = append(list, Capability{
			Type: "APP_GROUPS", Label: "App Groups",
			Note: "assign " + strings.Join(declared.AppGroups, ", ") + " to the App ID in the Apple Developer portal (the API cannot)",
		})
	}
	if declared.PushNotifications {
		list = append(list, Capability{Type: "PUSH_NOTIFICATIONS", Label: "Push Notifications"})
	}
	if declared.SignInWithApple {
		list = append(list, Capability{
			Type: "APPLE_ID_AUTH", Label: "Sign in with Apple",
			// Required by the API: the App ID is a primary App ID.
			Settings: []appstore.Setting{{Key: "APPLE_ID_AUTH_APP_CONSENT", Options: []appstore.Option{{Key: "PRIMARY_APP_CONSENT"}}}},
		})
	}
	for _, other := range declared.Other {
		list = append(list, Capability{Type: other, Label: other})
	}
	return list
}

// nameFromIdentifier names an extension App ID ("TixNow clip") when
// release.toml gives no name. App ID names only accept letters, digits and
// spaces.
func nameFromIdentifier(app, identifier string) string {
	last := identifier[strings.LastIndex(identifier, ".")+1:]
	return sanitizeName(strings.TrimSpace(app + " " + last))
}

func sanitizeName(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ' ':
			builder.WriteRune(r)
		default:
			builder.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

// profileType maps a kind to the App Store Connect profile type.
func profileType(kind string) string {
	switch kind {
	case config.ProfileDevelopment:
		return appstore.ProfileIOSDevelopment
	case config.ProfileAdHoc:
		return appstore.ProfileIOSAdHoc
	}
	return appstore.ProfileIOSAppStore
}

// certificateTypes are the certificates a kind of profile is signed with.
func certificateTypes(kind string) []string {
	if kind == config.ProfileDevelopment {
		return []string{"DEVELOPMENT", "IOS_DEVELOPMENT"}
	}
	return []string{"DISTRIBUTION", "IOS_DISTRIBUTION"}
}

func certificateGroup(kind string) string {
	if kind == config.ProfileDevelopment {
		return "development"
	}
	return "distribution"
}

func needsDevices(kind string) bool {
	return kind == config.ProfileDevelopment || kind == config.ProfileAdHoc
}

// MatchType is the fastlane match type of a kind.
func MatchType(kind string) string {
	switch kind {
	case config.ProfileDevelopment:
		return "development"
	case config.ProfileAdHoc:
		return "adhoc"
	}
	return "appstore"
}

// defaultProfileName names new profiles "<bundle id> AdHoc".
func defaultProfileName(bundleID, kind string) string {
	label := map[string]string{config.ProfileDevelopment: "Development", config.ProfileAdHoc: "AdHoc", config.ProfileAppStore: "AppStore"}[kind]
	return bundleID + " " + label
}

// FileName is the file a profile is written to in Desired.Directory.
func FileName(bundleID, kind string) string {
	return bundleID + "_" + kind + ".mobileprovision"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

var plistName = regexp.MustCompile(`<key>Name</key>\s*<string>([^<]*)</string>`)

// ProfileName reads the name from .mobileprovision content (the signed
// payload is a plain XML plist).
func ProfileName(content []byte) string {
	match := plistName.FindSubmatch(content)
	if match == nil {
		return ""
	}
	return html.UnescapeString(string(match[1]))
}

// previousName is the name of the last profile of this kind written or
// backed up locally, so a profile deleted by an interrupted apply comes back
// with the name the Xcode project references.
func previousName(desired Desired, bundleID, kind string) string {
	if data, err := os.ReadFile(filepath.Join(desired.Directory, FileName(bundleID, kind))); err == nil {
		if name := ProfileName(data); name != "" {
			return name
		}
	}
	pattern := filepath.Join(desired.Directory, ".backup", strings.TrimSuffix(FileName(bundleID, kind), ".mobileprovision")+"_*.mobileprovision")
	matches, _ := filepath.Glob(pattern)
	var newest string
	var newestTime time.Time
	for _, match := range matches {
		info, err := os.Stat(match)
		if err == nil && (newest == "" || info.ModTime().After(newestTime)) {
			newest, newestTime = match, info.ModTime()
		}
	}
	if newest == "" {
		return ""
	}
	data, err := os.ReadFile(newest)
	if err != nil {
		return ""
	}
	return ProfileName(data)
}
