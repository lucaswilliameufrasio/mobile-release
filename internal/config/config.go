package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	SchemaVersion      int
	ProjectType        string
	AppName            string
	Environment        string
	AndroidPackage     string
	AndroidTrack       string
	AndroidAPKTask     string
	AndroidAABTask     string
	AndroidAPKOutput   string
	AndroidAABOutput   string
	IOSWorkspace       string
	IOSScheme          string
	IOSConfiguration   string
	IOSSimulatorOutput string
	OTAEnabled         bool
	OTAServerURL       string
	OTAChannel         string
	ArtifactsDir       string
}

func Defaults() Config {
	return Config{SchemaVersion: 1, Environment: "internal", AndroidTrack: "internal", IOSConfiguration: "Release", OTAChannel: "production", ArtifactsDir: "dist"}
}

func Load(path string) (Config, error) {
	c := Defaults()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	defer f.Close()
	section := ""
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(strings.SplitN(s.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[] ")
			continue
		}
		p := strings.SplitN(line, "=", 2)
		if len(p) != 2 {
			continue
		}
		key := strings.TrimSpace(p[0])
		val := strings.Trim(strings.TrimSpace(p[1]), "\"")
		full := section + "." + key
		switch full {
		case ".schema_version":
			c.SchemaVersion, _ = strconv.Atoi(val)
		case "project.type":
			c.ProjectType = val
		case "app.name":
			c.AppName = val
		case "app.environment":
			c.Environment = val
		case "android.package_name":
			c.AndroidPackage = val
		case "android.track":
			c.AndroidTrack = val
		case "android.apk_task":
			c.AndroidAPKTask = val
		case "android.aab_task":
			c.AndroidAABTask = val
		case "android.apk_output":
			c.AndroidAPKOutput = val
		case "android.aab_output":
			c.AndroidAABOutput = val
		case "ios.workspace":
			c.IOSWorkspace = val
		case "ios.scheme":
			c.IOSScheme = val
		case "ios.configuration":
			c.IOSConfiguration = val
		case "ios.simulator_output":
			c.IOSSimulatorOutput = val
		case "ota.enabled":
			c.OTAEnabled = val == "true"
		case "ota.server_url":
			c.OTAServerURL = val
		case "ota.channel":
			c.OTAChannel = val
		case "artifacts.directory":
			c.ArtifactsDir = val
		}
	}
	return c, s.Err()
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
func Resolve(c Config) Config {
	c.ProjectType = env("MOBILE_RELEASE_PROJECT_TYPE", c.ProjectType)
	c.AppName = env("MOBILE_RELEASE_APP_NAME", c.AppName)
	c.Environment = env("MOBILE_RELEASE_ENVIRONMENT", c.Environment)
	c.AndroidPackage = env("MOBILE_RELEASE_ANDROID_PACKAGE_NAME", c.AndroidPackage)
	c.AndroidTrack = env("MOBILE_RELEASE_ANDROID_TRACK", c.AndroidTrack)
	c.AndroidAPKTask = env("MOBILE_RELEASE_ANDROID_APK_TASK", c.AndroidAPKTask)
	c.AndroidAABTask = env("MOBILE_RELEASE_ANDROID_AAB_TASK", c.AndroidAABTask)
	c.AndroidAPKOutput = env("MOBILE_RELEASE_ANDROID_APK_OUTPUT", c.AndroidAPKOutput)
	c.AndroidAABOutput = env("MOBILE_RELEASE_ANDROID_AAB_OUTPUT", c.AndroidAABOutput)
	c.IOSWorkspace = env("MOBILE_RELEASE_IOS_WORKSPACE", c.IOSWorkspace)
	c.IOSScheme = env("MOBILE_RELEASE_IOS_SCHEME", c.IOSScheme)
	c.IOSConfiguration = env("MOBILE_RELEASE_IOS_CONFIGURATION", c.IOSConfiguration)
	c.IOSSimulatorOutput = env("MOBILE_RELEASE_IOS_SIMULATOR_OUTPUT", c.IOSSimulatorOutput)
	c.OTAServerURL = env("MOBILE_RELEASE_OTA_SERVER_URL", c.OTAServerURL)
	c.OTAChannel = env("MOBILE_RELEASE_OTA_CHANNEL", c.OTAChannel)
	c.ArtifactsDir = env("MOBILE_RELEASE_ARTIFACTS_DIR", c.ArtifactsDir)
	if v := os.Getenv("MOBILE_RELEASE_OTA_ENABLED"); v != "" {
		c.OTAEnabled = v == "true"
	}
	return c
}
func Validate(c Config) error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version %d", c.SchemaVersion)
	}
	switch c.ProjectType {
	case "expo", "flutter", "kmp":
	default:
		return fmt.Errorf("project.type must be expo, flutter, or kmp")
	}
	if c.ArtifactsDir == "" || filepath.IsAbs(c.ArtifactsDir) {
		return fmt.Errorf("artifacts.directory must be a relative path")
	}
	return nil
}
