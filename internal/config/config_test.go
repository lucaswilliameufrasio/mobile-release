package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndEnvironmentPrecedence(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "release.toml")
	os.WriteFile(p, []byte("schema_version = 1\n[project]\ntype = \"expo\"\n[android]\ntrack = \"internal\"\n[artifacts]\ndirectory = \"dist\"\n"), 0600)
	c, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("MOBILE_RELEASE_ANDROID_TRACK", "beta")
	c = Resolve(c)
	if c.AndroidTrack != "beta" {
		t.Fatalf("expected env override, got %q", c.AndroidTrack)
	}
	if e = Validate(c); e != nil {
		t.Fatal(e)
	}
}
func TestValidateRejectsUnknownProvider(t *testing.T) {
	c := Defaults()
	c.ProjectType = "native"
	if Validate(c) == nil {
		t.Fatal("expected validation error")
	}
}
