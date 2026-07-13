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
