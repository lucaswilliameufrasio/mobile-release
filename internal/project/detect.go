package project

import (
	"os"
	"path/filepath"
)

func exists(root, name string) bool { _, e := os.Stat(filepath.Join(root, name)); return e == nil }
func Detect(root string) string {
	if exists(root, "pubspec.yaml") {
		return "flutter"
	}
	if exists(root, "app.json") || exists(root, "app.config.ts") || exists(root, "app.config.js") {
		return "expo"
	}
	if exists(root, "settings.gradle.kts") || exists(root, "settings.gradle") {
		return "kmp"
	}
	return ""
}
