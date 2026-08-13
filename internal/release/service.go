package release

import (
	"context"
	"fmt"
	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/internal/providers"
	"github.com/lucaswilliameufrasio/mobile-release/internal/providers/expo"
	"github.com/lucaswilliameufrasio/mobile-release/internal/providers/flutter"
	"github.com/lucaswilliameufrasio/mobile-release/internal/providers/kmp"
	"github.com/lucaswilliameufrasio/mobile-release/internal/runner"
	"os"
	"path/filepath"
)

func provider(t string) (providers.Provider, error) {
	switch t {
	case "expo":
		return expo.Provider{}, nil
	case "flutter":
		return flutter.Provider{}, nil
	case "kmp":
		return kmp.Provider{}, nil
	}
	return nil, fmt.Errorf("unsupported provider %q", t)
}

func resolveAAB(c config.Config) (string, error) {
	patterns := []string{c.AndroidAABOutput}
	if c.AndroidAABOutput == "" {
		switch c.ProjectType {
		case "expo":
			patterns = []string{"android/app/build/outputs/bundle/release/app-release.aab"}
		case "flutter":
			patterns = []string{"build/app/outputs/bundle/release/app-release.aab"}
		}
	}
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if matches, e := filepath.Glob(p); e == nil && len(matches) > 0 {
			return matches[0], nil
		}
	}
	return "", fmt.Errorf("AAB not found (searched %v)", patterns)
}

type Service struct {
	R runner.Runner
	C config.Config
}

func (s Service) QA(ctx context.Context) error {
	p, e := provider(s.C.ProjectType)
	if e != nil {
		return e
	}
	if e = p.Prepare(ctx, s.R, s.C); e != nil {
		return e
	}
	if e = p.APK(ctx, s.R, s.C); e != nil {
		return e
	}
	return p.IOSSimulator(ctx, s.R, s.C)
}
func (s Service) Internal(ctx context.Context, platform string) error {
	p, e := provider(s.C.ProjectType)
	if e != nil {
		return e
	}
	if e = p.Prepare(ctx, s.R, s.C); e != nil {
		return e
	}
	if platform == "android" || platform == "all" {
		if e = p.AAB(ctx, s.R, s.C); e != nil {
			return e
		}
		aab, e := resolveAAB(s.C)
		if e != nil {
			return e
		}
		if e = os.Setenv("MOBILE_RELEASE_ANDROID_AAB_PATH", aab); e != nil {
			return e
		}
		if e = s.R.Run(ctx, "bundle", "exec", "fastlane", "android", "internal"); e != nil {
			return e
		}
	}
	if platform == "ios" || platform == "all" {
		if e = p.IOSArchive(ctx, s.R, s.C); e != nil {
			return e
		}
		if e = s.R.Run(ctx, "bundle", "exec", "fastlane", "ios", "testflight"); e != nil {
			return e
		}
	}
	return nil
}
