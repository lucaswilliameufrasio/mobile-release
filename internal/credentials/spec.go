// Package credentials declares which credentials each release needs,
// resolves them from the configured stores and hands them to builds and
// lanes without ever printing their values.
package credentials

import (
	"fmt"
	"sort"

	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
)

// Kind tells how a credential is stored and used.
type Kind string

const (
	// Text is a short value such as a password or an id.
	Text Kind = "text"
	// File is a file (keystore, JSON key, .p8). Stores keep it base64-encoded;
	// builds receive a private temporary path.
	File Kind = "file"
)

// Spec describes one credential. Users never type variable names: setup
// shows Label and Help, and CI uses EnvName (with _PATH/_BASE64 for files).
type Spec struct {
	ID    string
	Kind  Kind
	Label string
	Help  string
	// EnvName is the CI variable; File specs accept EnvName+"_PATH" or
	// EnvName+"_BASE64".
	EnvName string
	// Extension of the temporary file for File specs.
	Extension string
	// Shared marks values usually identical in every environment (signing).
	Shared bool
}

// Credential ids.
const (
	AndroidKeystore         = "android.keystore"
	AndroidKeystorePassword = "android.keystore_password"
	AndroidKeyAlias         = "android.key_alias"
	AndroidKeyPassword      = "android.key_password"
	PlayServiceAccount      = "play.service_account"
	FirebaseServiceAccount  = "firebase.service_account"
	AppStoreKeyID           = "appstore.key_id"
	AppStoreIssuerID        = "appstore.issuer_id"
	AppStorePrivateKey      = "appstore.private_key"
	MatchGitURL             = "match.git_url"
	MatchPassword           = "match.password"
	ExpoToken               = "expo.token"
)

var specs = map[string]Spec{
	AndroidKeystore: {
		ID: AndroidKeystore, Kind: File, Extension: ".jks", Shared: true,
		Label:   "Android upload keystore (.jks/.keystore)",
		Help:    "The keystore that signs release builds. Play App Signing re-signs with Google's key.",
		EnvName: "MOBILE_RELEASE_ANDROID_KEYSTORE",
	},
	AndroidKeystorePassword: {
		ID: AndroidKeystorePassword, Kind: Text, Shared: true,
		Label:   "Android keystore password",
		EnvName: "MOBILE_RELEASE_ANDROID_KEYSTORE_PASSWORD",
	},
	AndroidKeyAlias: {
		ID: AndroidKeyAlias, Kind: Text, Shared: true,
		Label:   "Android key alias",
		Help:    "List it with: keytool -list -keystore <file>",
		EnvName: "MOBILE_RELEASE_ANDROID_KEY_ALIAS",
	},
	AndroidKeyPassword: {
		ID: AndroidKeyPassword, Kind: Text, Shared: true,
		Label:   "Android key password",
		Help:    "Often the same as the keystore password.",
		EnvName: "MOBILE_RELEASE_ANDROID_KEY_PASSWORD",
	},
	PlayServiceAccount: {
		ID: PlayServiceAccount, Kind: File, Extension: ".json", Shared: true,
		Label:   "Google Play service account key (JSON)",
		Help:    "Play Console > Users and permissions: a service account with release permissions.",
		EnvName: "MOBILE_RELEASE_GOOGLE_PLAY_JSON",
	},
	FirebaseServiceAccount: {
		ID: FirebaseServiceAccount, Kind: File, Extension: ".json",
		Label:   "Firebase service account key (JSON)",
		Help:    "Google Cloud IAM: a service account with the Firebase App Distribution Admin role.",
		EnvName: "MOBILE_RELEASE_FIREBASE_SERVICE_ACCOUNT",
	},
	AppStoreKeyID: {
		ID: AppStoreKeyID, Kind: Text, Shared: true,
		Label:   "App Store Connect API key id",
		Help:    "App Store Connect > Users and Access > Integrations > App Store Connect API.",
		EnvName: "MOBILE_RELEASE_APP_STORE_KEY_ID",
	},
	AppStoreIssuerID: {
		ID: AppStoreIssuerID, Kind: Text, Shared: true,
		Label:   "App Store Connect issuer id",
		EnvName: "MOBILE_RELEASE_APP_STORE_ISSUER_ID",
	},
	AppStorePrivateKey: {
		ID: AppStorePrivateKey, Kind: File, Extension: ".p8", Shared: true,
		Label:   "App Store Connect API private key (.p8)",
		EnvName: "MOBILE_RELEASE_APP_STORE_PRIVATE_KEY",
	},
	MatchGitURL: {
		ID: MatchGitURL, Kind: Text, Shared: true,
		Label:   "fastlane match certificates repository URL",
		Help:    "SSH URL of the private repository that stores the encrypted certificates.",
		EnvName: "MOBILE_RELEASE_MATCH_GIT_URL",
	},
	MatchPassword: {
		ID: MatchPassword, Kind: Text, Shared: true,
		Label:   "fastlane match encryption password",
		EnvName: "MOBILE_RELEASE_MATCH_PASSWORD",
	},
	ExpoToken: {
		ID: ExpoToken, Kind: Text,
		Label:   "Expo access token",
		Help:    "expo.dev > Account settings > Access tokens.",
		EnvName: "MOBILE_RELEASE_EXPO_TOKEN",
	},
}

// Lookup returns the spec for id.
func Lookup(id string) (Spec, error) {
	spec, ok := specs[id]
	if !ok {
		return Spec{}, fmt.Errorf("unknown credential %q", id)
	}
	return spec, nil
}

// All returns every spec sorted by id.
func All() []Spec {
	all := make([]Spec, 0, len(specs))
	for _, spec := range specs {
		all = append(all, spec)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all
}

// Platforms selected for a run.
type Platforms struct {
	Android bool
	IOS     bool
}

// Required lists the credentials a deploy of env on the given platforms
// needs, derived from destinations, signing and build number strategy.
func Required(c config.Config, env *config.Environment, platforms Platforms) []Spec {
	ids := map[string]bool{}
	add := func(values ...string) {
		for _, id := range values {
			ids[id] = true
		}
	}
	androidBuild := platforms.Android && c.AndroidEnabled() &&
		(env.Has(config.DestinationFirebase) || env.Has(config.DestinationPlay) || env.Has(config.DestinationArtifacts))
	iosBuild := platforms.IOS && c.IOSEnabled() &&
		(env.Has(config.DestinationFirebase) || env.Has(config.DestinationTestFlight) ||
			env.Has(config.DestinationAppStore) || env.Has(config.DestinationArtifacts))

	if androidBuild {
		if c.Android.Signing == config.AndroidSigningInjected {
			add(AndroidKeystore, AndroidKeystorePassword, AndroidKeyAlias, AndroidKeyPassword)
		}
		if env.Has(config.DestinationPlay) || env.BuildNumber == config.BuildNumberStore {
			add(PlayServiceAccount)
		}
	}
	if iosBuild {
		switch c.IOS.Signing {
		case config.SigningMatch:
			add(MatchGitURL, MatchPassword)
		case config.SigningAutomatic:
			add(AppStoreKeyID, AppStoreIssuerID, AppStorePrivateKey)
		}
		if env.Has(config.DestinationTestFlight) || env.Has(config.DestinationAppStore) || env.BuildNumber == config.BuildNumberStore {
			add(AppStoreKeyID, AppStoreIssuerID, AppStorePrivateKey)
		}
	}
	if env.Has(config.DestinationFirebase) && (androidBuild || iosBuild) {
		add(FirebaseServiceAccount)
	}
	if env.Has(config.DestinationOTA) && env.OTA.Mode == "eas" {
		add(ExpoToken)
	}
	required := make([]Spec, 0, len(ids))
	for id := range ids {
		required = append(required, specs[id])
	}
	sort.Slice(required, func(i, j int) bool { return required[i].ID < required[j].ID })
	return required
}
