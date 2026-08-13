package secrets

import (
	"encoding/base64"
	"os"
	"testing"
)

func TestMaterializeBase64(t *testing.T) {
	t.Setenv("TEST_SECRET_BASE64", base64.StdEncoding.EncodeToString([]byte("secret")))
	p, e := Materialize(t.TempDir(), "TEST_SECRET", ".txt")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "secret" {
		t.Fatal("unexpected secret")
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %o", info.Mode().Perm())
	}
}
func TestPathWins(t *testing.T) {
	t.Setenv("TEST_SECRET_PATH", "/tmp/existing")
	t.Setenv("TEST_SECRET_BASE64", "bad")
	p, e := Materialize(t.TempDir(), "TEST_SECRET", ".txt")
	if e != nil || p != "/tmp/existing" {
		t.Fatalf("%s %v", p, e)
	}
}
func TestApplySetsPathFromBase64(t *testing.T) {
	prefix := "MOBILE_RELEASE_TEST_K"
	t.Setenv(prefix+"_BASE64", base64.StdEncoding.EncodeToString([]byte("x")))
	if e := Apply(t.TempDir(), prefix, ".jks"); e != nil {
		t.Fatal(e)
	}
	if os.Getenv(prefix+"_PATH") == "" {
		t.Fatal("PATH not set")
	}
}
func TestApplySkipsWhenUnset(t *testing.T) {
	t.Setenv("MOBILE_RELEASE_TEST_SKIP_PATH", "")
	t.Setenv("MOBILE_RELEASE_TEST_SKIP_BASE64", "")
	if e := Apply(t.TempDir(), "MOBILE_RELEASE_TEST_SKIP", ".p8"); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("MOBILE_RELEASE_TEST_SKIP_PATH") != "" {
		t.Fatal("PATH should stay unset")
	}
}
func TestApplyAll(t *testing.T) {
	for _, p := range []string{
		"MOBILE_RELEASE_ANDROID_KEYSTORE_PATH",
		"MOBILE_RELEASE_ANDROID_KEYSTORE_BASE64",
		"MOBILE_RELEASE_GOOGLE_PLAY_JSON_PATH",
		"MOBILE_RELEASE_APP_STORE_PRIVATE_KEY_PATH",
		"MOBILE_RELEASE_APP_STORE_PRIVATE_KEY_BASE64",
	} {
		t.Setenv(p, "")
	}
	t.Setenv("MOBILE_RELEASE_GOOGLE_PLAY_JSON_BASE64", base64.StdEncoding.EncodeToString([]byte("{}")))
	if e := ApplyAll(t.TempDir()); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("MOBILE_RELEASE_GOOGLE_PLAY_JSON_PATH") == "" {
		t.Fatal("google play json PATH not set")
	}
	if os.Getenv("MOBILE_RELEASE_ANDROID_KEYSTORE_PATH") != "" {
		t.Fatal("keystore PATH should stay unset")
	}
	if os.Getenv("MOBILE_RELEASE_APP_STORE_PRIVATE_KEY_PATH") != "" {
		t.Fatal("app store key PATH should stay unset")
	}
}
