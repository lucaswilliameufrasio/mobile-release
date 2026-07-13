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
