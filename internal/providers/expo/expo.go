package expo

import (
	"context"
	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/internal/runner"
)

type Provider struct{}

func (Provider) Prepare(c context.Context, r runner.Runner, _ config.Config) error {
	if e := r.Run(c, "pnpm", "install", "--frozen-lockfile"); e != nil {
		return e
	}
	return r.Run(c, "npx", "expo", "prebuild", "--clean")
}
func (Provider) APK(c context.Context, r runner.Runner, _ config.Config) error {
	return r.Run(c, "./android/gradlew", "-p", "android", "assembleRelease")
}
func (Provider) AAB(c context.Context, r runner.Runner, _ config.Config) error {
	return r.Run(c, "./android/gradlew", "-p", "android", "bundleRelease")
}
func (Provider) IOSSimulator(c context.Context, r runner.Runner, x config.Config) error {
	return r.Run(c, "xcodebuild", "-workspace", x.IOSWorkspace, "-scheme", x.IOSScheme, "-configuration", "Debug", "-sdk", "iphonesimulator", "-derivedDataPath", "build/ios-simulator", "CODE_SIGNING_ALLOWED=NO", "build")
}
func (Provider) IOSArchive(c context.Context, r runner.Runner, _ config.Config) error {
	return r.Run(c, "bundle", "exec", "fastlane", "ios", "build")
}
