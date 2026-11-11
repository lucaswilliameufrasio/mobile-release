package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInspectFlutter(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"pubspec.yaml":                         "name: shop\nversion: 1.0.0+1\n",
		"android/app/build.gradle.kts":         "android {\n  defaultConfig {\n    applicationId = \"com.example.shop\"\n  }\n}\n",
		"ios/Runner.xcodeproj/project.pbxproj": "PRODUCT_BUNDLE_IDENTIFIER = com.example.shop;\nPRODUCT_BUNDLE_IDENTIFIER = com.example.shop;\nPRODUCT_BUNDLE_IDENTIFIER = com.example.shop.RunnerTests;\n",
		".env":                                 "A=1\n",
		".env.prd":                             "A=2\n",
		".env.example":                         "A=\n",
	})
	info := Inspect(root)
	if info.Type != "flutter" || info.Name != "shop" || info.AndroidPackage != "com.example.shop" || info.IOSBundleID != "com.example.shop" {
		t.Fatalf("info = %+v", info)
	}
	if strings.Join(info.EnvFiles, ",") != ".env,.env.prd" {
		t.Fatalf("env files = %v", info.EnvFiles)
	}
}

func TestInspectExpo(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"package.json": `{"dependencies":{"expo":"~55.0.0"}}`,
		"app.json":     `{"expo":{"name":"Notes","android":{"package":"com.example.notes"},"ios":{"bundleIdentifier":"com.example.notes"}}}`,
	})
	info := Inspect(root)
	if info.Type != "expo" || info.Name != "Notes" || info.AndroidPackage != "com.example.notes" || info.IOSBundleID != "com.example.notes" {
		t.Fatalf("info = %+v", info)
	}
}

func TestInspectJirau(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"jirau.toml":                   "[app]\nname = \"vault\"\npackage_id = \"com.example.vault\"\nplatforms = [\"android\", \"ios\"]\n",
		"android/app/build.gradle.kts": "android { namespace = \"com.example.vault\" }\n",
		"ios/project.yml":              "name: SecureVault\noptions:\n  bundleIdPrefix: com.example.vault\n",
		"Cargo.toml":                   "[workspace]\n",
	})
	info := Inspect(root)
	if info.Type != "native" || info.Preset != "jirau" || info.IOSProject != "ios/SecureVault.xcodeproj" || info.IOSScheme != "SecureVault" {
		t.Fatalf("info = %+v", info)
	}
	if info.AndroidPackage != "com.example.vault" || info.IOSBundleID != "com.example.vault" || !info.HasAndroid || !info.HasIOS {
		t.Fatalf("ids = %+v", info)
	}
}

func TestInspectKMPAndNative(t *testing.T) {
	kmp := t.TempDir()
	write(t, kmp, map[string]string{
		"settings.gradle.kts":                     "include(\":composeApp\")\n",
		"composeApp/build.gradle.kts":             "android { defaultConfig { applicationId = \"com.example.kmp\" } }\n",
		"iosApp/iosApp.xcodeproj/project.pbxproj": "PRODUCT_BUNDLE_IDENTIFIER = com.example.kmp.iosApp;\n",
	})
	info := Inspect(kmp)
	if info.Type != "kmp" || info.AndroidPackage != "com.example.kmp" || info.IOSProject != "iosApp/iosApp.xcodeproj" || info.IOSScheme != "iosApp" {
		t.Fatalf("kmp = %+v", info)
	}

	native := t.TempDir()
	write(t, native, map[string]string{
		"android/app/build.gradle":                        "android { defaultConfig { applicationId \"com.example.native\" } }\n",
		"android/settings.gradle":                         "include ':app'\n",
		"ios/Native.xcworkspace/contents.xcworkspacedata": "<Workspace/>",
	})
	info = Inspect(native)
	if info.Type != "native" || info.Preset != "" || info.AndroidPackage != "com.example.native" || info.IOSWorkspace != "ios/Native.xcworkspace" || info.IOSScheme != "Native" {
		t.Fatalf("native = %+v", info)
	}

	if got := Detect(t.TempDir()); got != "" {
		t.Fatalf("empty dir detected as %q", got)
	}
}
