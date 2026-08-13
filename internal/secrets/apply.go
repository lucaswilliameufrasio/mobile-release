package secrets

import "os"

func Apply(tempDir, prefix, ext string) error {
	if os.Getenv(prefix+"_PATH") != "" {
		return nil
	}
	if os.Getenv(prefix+"_BASE64") == "" {
		return nil
	}
	p, err := Materialize(tempDir, prefix, ext)
	if err != nil {
		return err
	}
	return os.Setenv(prefix+"_PATH", p)
}

func ApplyAll(tempDir string) error {
	for _, s := range []struct{ prefix, ext string }{
		{"MOBILE_RELEASE_ANDROID_KEYSTORE", ".jks"},
		{"MOBILE_RELEASE_GOOGLE_PLAY_JSON", ".json"},
		{"MOBILE_RELEASE_APP_STORE_PRIVATE_KEY", ".p8"},
	} {
		if err := Apply(tempDir, s.prefix, s.ext); err != nil {
			return err
		}
	}
	return nil
}
