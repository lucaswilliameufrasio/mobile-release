package secrets

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

func Materialize(tempDir, prefix, ext string) (string, error) {
	if p := os.Getenv(prefix + "_PATH"); p != "" {
		return p, nil
	}
	raw := os.Getenv(prefix + "_BASE64")
	if raw == "" {
		return "", fmt.Errorf("missing %s_PATH or %s_BASE64", prefix, prefix)
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", prefix, err)
	}
	if err = os.MkdirAll(tempDir, 0700); err != nil {
		return "", err
	}
	p := filepath.Join(tempDir, "secret"+ext)
	if err = os.WriteFile(p, b, 0600); err != nil {
		return "", err
	}
	return p, nil
}
