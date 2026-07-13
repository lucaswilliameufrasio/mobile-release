package providers

import (
	"context"
	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/internal/runner"
)

type Provider interface {
	Prepare(context.Context, runner.Runner, config.Config) error
	APK(context.Context, runner.Runner, config.Config) error
	AAB(context.Context, runner.Runner, config.Config) error
	IOSSimulator(context.Context, runner.Runner, config.Config) error
	IOSArchive(context.Context, runner.Runner, config.Config) error
}
