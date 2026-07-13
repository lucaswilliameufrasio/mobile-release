package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetect(t *testing.T) {
	cases := []struct{ file, want string }{{"pubspec.yaml", "flutter"}, {"app.config.ts", "expo"}, {"settings.gradle.kts", "kmp"}}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			d := t.TempDir()
			os.WriteFile(filepath.Join(d, tc.file), []byte("x"), 0600)
			if got := Detect(d); got != tc.want {
				t.Fatalf("got %s", got)
			}
		})
	}
}
