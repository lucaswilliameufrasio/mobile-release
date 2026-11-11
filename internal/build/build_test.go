package build

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTimestampBuildNumber(t *testing.T) {
	cases := map[string]time.Time{
		"263141530": time.Date(2026, 11, 10, 15, 30, 0, 0, time.UTC),
		"260010005": time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC),
		"993652359": time.Date(2099, 12, 31, 23, 59, 0, 0, time.UTC),
	}
	for want, at := range cases {
		if got := TimestampBuildNumber(at); got != want {
			t.Errorf("%s: got %s want %s", at, got, want)
		}
	}
	// Converted to UTC, and below the Android versionCode limit.
	local := time.Date(2026, 11, 10, 12, 30, 0, 0, time.FixedZone("BRT", -3*3600))
	if TimestampBuildNumber(local) != "263141530" {
		t.Fatal("timestamp must use UTC")
	}
	latest, err := strconv.Atoi(TimestampBuildNumber(time.Date(2099, 12, 31, 23, 59, 0, 0, time.UTC)))
	if err != nil || latest >= 2_100_000_000 {
		t.Fatalf("must stay below 2,100,000,000, got %d", latest)
	}
}

func TestExpandDefinesAndPairs(t *testing.T) {
	defines := ExpandDefines(map[string]string{"B": "{env}-{build_number}", "A": "{commit}"}, "abc", "42", "staging")
	if strings.Join(SortedPairs(defines), ",") != "A=abc,B=staging-42" {
		t.Fatalf("defines = %v", defines)
	}
}

func TestReadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\n\nexport API_URL=\"https://api.example\"\nTOKEN='a=b'\nEMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	pairs, err := ReadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pairs, ",") != "API_URL=https://api.example,TOKEN=a=b,EMPTY=" {
		t.Fatalf("pairs = %v", pairs)
	}
	if err := os.WriteFile(path, []byte("not a pair\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEnvFile(path); err == nil || !strings.Contains(err.Error(), ":1:") {
		t.Fatalf("expected line error, got %v", err)
	}
}
