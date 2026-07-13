package flutter

import (
	"context"
	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/internal/runner"
)

type Provider struct{}

func (Provider) Prepare(c context.Context, r runner.Runner, _ config.Config) error {
	return r.Run(c, "flutter", "pub", "get")
}
func (Provider) APK(c context.Context, r runner.Runner, _ config.Config) error {
	return r.Run(c, "flutter", "build", "apk", "--release")
}
func (Provider) AAB(c context.Context, r runner.Runner, _ config.Config) error {
	return r.Run(c, "flutter", "build", "appbundle", "--release")
}
func (Provider) IOSSimulator(c context.Context, r runner.Runner, _ config.Config) error {
	return r.Run(c, "flutter", "build", "ios", "--simulator", "--debug")
}
func (Provider) IOSArchive(c context.Context, r runner.Runner, _ config.Config) error {
	return r.Run(c, "flutter", "build", "ipa", "--release")
}
