// Package mcpserver exposes mobile-release to agents over the Model Context
// Protocol.
//
// Every tool runs the mobile-release CLI itself as a subprocess, so agents get
// exactly the same preflight checks, credential stores and guards as a person
// in a terminal, and build output never reaches the MCP transport (stdout).
//
// Publishing always asks the user: deploy shows the dry-run plan through MCP
// elicitation and only publishes after an explicit acceptance (typing the
// environment name for store destinations). Clients without elicitation, a
// decline or a cancel never publish.
package mcpserver

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/config"
)

// Executor runs the CLI with args in dir and returns its combined output.
// The error is non-nil when the command fails; output is still returned.
type Executor func(ctx context.Context, dir string, args []string) (string, error)

// SelfExecutor runs this binary with an empty stdin, so no subprocess can
// read the MCP transport or wait for a terminal prompt.
func SelfExecutor(ctx context.Context, dir string, args []string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, self, args...)
	command.Dir = dir
	command.Stdin = nil
	command.Env = append(os.Environ(), "CI=", "NO_COLOR=1")
	output, err := command.CombinedOutput()
	return string(output), err
}

// Server is the mobile-release MCP server.
type Server struct {
	Version string
	Exec    Executor
	// MaxOutput bounds the text returned per tool call (the tail is kept).
	MaxOutput int

	keyOnce sync.Once
	key     []byte
}

func (s *Server) executor() Executor {
	if s.Exec != nil {
		return s.Exec
	}
	return SelfExecutor
}

func (s *Server) limit(text string) string {
	max := s.MaxOutput
	if max <= 0 {
		max = 60_000
	}
	if len(text) <= max {
		return text
	}
	return "…(earlier output truncated)…\n" + text[len(text)-max:]
}

// Input types. Dir defaults to the server's working directory.

type ProjectInput struct {
	Dir string `json:"dir,omitempty" jsonschema:"project root containing release.toml (default: the server's working directory)"`
}

type DoctorInput struct {
	Dir         string `json:"dir,omitempty" jsonschema:"project root containing release.toml"`
	Environment string `json:"environment,omitempty" jsonschema:"only check this environment (also loads exec credential stores for it)"`
}

type ReleaseInput struct {
	Dir         string `json:"dir,omitempty" jsonschema:"project root containing release.toml"`
	Environment string `json:"environment" jsonschema:"environment from release.toml"`
	Platform    string `json:"platform,omitempty" jsonschema:"all, android or ios (default all)"`
	Notes       string `json:"notes,omitempty" jsonschema:"release notes (default: environment and commit)"`
}

type QAInput struct {
	Dir         string `json:"dir,omitempty" jsonschema:"project root containing release.toml"`
	Environment string `json:"environment,omitempty" jsonschema:"environment whose env_file and defines are used"`
	Platform    string `json:"platform,omitempty" jsonschema:"all, android or ios (default all)"`
}

// EnvironmentInfo describes one environment without any credential value.
type EnvironmentInfo struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Destinations []string `json:"destinations"`
	BuildNumber  string   `json:"build_number"`
	EnvFile      string   `json:"env_file,omitempty"`
	AsksToType   bool     `json:"publishing_requires_typing_the_name"`
}

type EnvironmentsOutput struct {
	ProjectType  string            `json:"project_type"`
	AppID        string            `json:"app_id"`
	Android      bool              `json:"android"`
	IOS          bool              `json:"ios"`
	Environments []EnvironmentInfo `json:"environments"`
}

type TextOutput struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

type AdoptInput struct {
	Dir      string `json:"dir,omitempty" jsonschema:"project root (default: the server's working directory)"`
	Workflow bool   `json:"workflow,omitempty" jsonschema:"also add .github/workflows/release.yml"`
}

type UpgradeInput struct {
	Dir string `json:"dir,omitempty" jsonschema:"project root (default: the server's working directory)"`
	To  string `json:"to,omitempty" jsonschema:"version to move to (default: the latest release of the installed major version)"`
}

type AdoptOutput struct {
	Applied bool   `json:"applied"`
	Reason  string `json:"reason,omitempty"`
	Plan    string `json:"plan,omitempty"`
	Output  string `json:"output,omitempty"`
	Command string `json:"command,omitempty"`
}

type DeployOutput struct {
	Published bool   `json:"published"`
	Reason    string `json:"reason,omitempty"`
	Plan      string `json:"plan,omitempty"`
	Output    string `json:"output,omitempty"`
	// Command lets the user publish from a terminal instead.
	Command string `json:"command,omitempty"`
}

func boolPtr(value bool) *bool { return &value }

// New builds the MCP server with its tools.
func (s *Server) New() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mobile-release", Version: s.Version}, &mcp.ServerOptions{
		Instructions: "mobile-release builds and publishes mobile apps described by release.toml. " +
			"Use environments and doctor to inspect a project, plan to preview a release (dry run), qa for local test builds and deploy to publish. " +
			"deploy always asks the user to confirm before publishing; never try to bypass that confirmation. " +
			"Credential values are never returned: when credentials are missing, tell the user to run `mobile-release setup` in a terminal.",
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "environments",
		Description: "List the release environments of a project (destinations, build number strategy, env file). Read-only; no credentials.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, s.environments)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "doctor",
		Description: "Check tools, release.toml and credentials (credential names and their store only, never values). Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, s.doctor)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "plan",
		Description: "Dry run of a release: the exact build and upload commands (variable names only), credentials and their sources. Builds and publishes nothing.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, s.plan)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "qa",
		Description: "Build an APK and an iOS simulator app locally without release signing. Publishes nothing. Can take several minutes.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)},
	}, s.qa)
	mcp.AddTool(server, &mcp.Tool{
		Name: "adopt",
		Description: "Set a project up for mobile-release: release.toml (detected), a managed Makefile block pinning the CLI version, " +
			".gitignore and optionally a release workflow. Never overwrites files. Always asks the user before writing.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, s.adopt)
	mcp.AddTool(server, &mcp.Tool{
		Name: "upgrade",
		Description: "Move the project's mobile-release pins (managed Makefile block, release workflow) to the latest release and show what changed. " +
			"Does not reinstall the CLI; it returns the install command. Always asks the user before writing.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)},
	}, s.upgrade)
	mcp.AddTool(server, &mcp.Tool{
		Name: "deploy",
		Description: "Build and publish an environment (Firebase App Distribution, Google Play, TestFlight, App Store, OTA). " +
			"Always asks the user to confirm the plan first; nothing is published without that answer. Can take several minutes.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(true)},
	}, s.deploy)
	return server
}

func resolveDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(abs, "release.toml")); err != nil {
		return "", fmt.Errorf("%s has no release.toml: run `mobile-release init` there first", abs)
	}
	return abs, nil
}

func loadConfig(dir string) (config.Config, error) {
	c, err := config.Load(filepath.Join(dir, "release.toml"))
	if err != nil {
		return c, err
	}
	c.ApplyEnv(os.LookupEnv)
	c.ApplyDefaults()
	return c, c.Validate()
}

func normalizePlatform(platform string) (string, error) {
	switch platform {
	case "":
		return "all", nil
	case "all", "android", "ios":
		return platform, nil
	}
	return "", fmt.Errorf("platform must be all, android or ios")
}

func (s *Server) environments(_ context.Context, _ *mcp.CallToolRequest, in ProjectInput) (*mcp.CallToolResult, EnvironmentsOutput, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, EnvironmentsOutput{}, err
	}
	c, err := loadConfig(dir)
	if err != nil {
		return nil, EnvironmentsOutput{}, err
	}
	out := EnvironmentsOutput{ProjectType: c.Project.Type, AppID: c.App.ID, Android: c.AndroidEnabled(), IOS: c.IOSEnabled()}
	for _, name := range c.EnvironmentNames() {
		env := c.Environments[name]
		out.Environments = append(out.Environments, EnvironmentInfo{
			Name: name, Description: env.Description, Destinations: env.Destinations,
			BuildNumber: env.BuildNumber, EnvFile: env.EnvFile, AsksToType: env.NeedsConfirmation(),
		})
	}
	return nil, out, nil
}

func (s *Server) run(ctx context.Context, dir string, args []string) TextOutput {
	output, err := s.executor()(ctx, dir, args)
	return TextOutput{OK: err == nil, Output: s.limit(output)}
}

func (s *Server) doctor(ctx context.Context, _ *mcp.CallToolRequest, in DoctorInput) (*mcp.CallToolResult, TextOutput, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, TextOutput{}, err
	}
	args := []string{"doctor", "-yes"}
	if in.Environment != "" {
		args = append(args, "-env", in.Environment)
	}
	return nil, s.run(ctx, dir, args), nil
}

func releaseArgs(command string, in ReleaseInput, platform string) []string {
	args := []string{command, "-yes", "-env", in.Environment, "-platform", platform}
	if in.Notes != "" {
		args = append(args, "-notes", in.Notes)
	}
	return args
}

func (s *Server) plan(ctx context.Context, _ *mcp.CallToolRequest, in ReleaseInput) (*mcp.CallToolResult, TextOutput, error) {
	dir, platform, _, err := s.checkRelease(in)
	if err != nil {
		return nil, TextOutput{}, err
	}
	return nil, s.run(ctx, dir, append(releaseArgs("deploy", in, platform), "-dry-run")), nil
}

func (s *Server) qa(ctx context.Context, _ *mcp.CallToolRequest, in QAInput) (*mcp.CallToolResult, TextOutput, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return nil, TextOutput{}, err
	}
	platform, err := normalizePlatform(in.Platform)
	if err != nil {
		return nil, TextOutput{}, err
	}
	args := []string{"qa", "-yes", "-platform", platform}
	if in.Environment != "" {
		args = append(args, "-env", in.Environment)
	}
	return nil, s.run(ctx, dir, args), nil
}

func (s *Server) checkRelease(in ReleaseInput) (string, string, *config.Environment, error) {
	dir, err := resolveDir(in.Dir)
	if err != nil {
		return "", "", nil, err
	}
	platform, err := normalizePlatform(in.Platform)
	if err != nil {
		return "", "", nil, err
	}
	c, err := loadConfig(dir)
	if err != nil {
		return "", "", nil, err
	}
	if in.Environment == "" {
		return "", "", nil, fmt.Errorf("environment is required (available: %s)", strings.Join(c.EnvironmentNames(), ", "))
	}
	env, err := c.Environment(in.Environment)
	if err != nil {
		return "", "", nil, err
	}
	return dir, platform, env, nil
}

func terminalCommand(dir string, in ReleaseInput, platform string) string {
	return fmt.Sprintf("cd %q && mobile-release deploy -env %s -platform %s", dir, in.Environment, platform)
}

// confirmID is the input request that asks the user before publishing.
const confirmID = "confirm_publish"

func (s *Server) deploy(ctx context.Context, request *mcp.CallToolRequest, in ReleaseInput) (*mcp.CallToolResult, DeployOutput, error) {
	dir, platform, env, err := s.checkRelease(in)
	if err != nil {
		return nil, DeployOutput{}, err
	}
	command := terminalCommand(dir, in, platform)

	// The plan runs on every round, so what is published is checked again
	// right before publishing.
	plan := s.run(ctx, dir, append(releaseArgs("deploy", in, platform), "-dry-run"))
	if !plan.OK {
		return nil, DeployOutput{Reason: "the release plan failed; fix it before publishing", Plan: plan.Output, Command: command}, nil
	}
	g := s.gate(request, confirmation(env, platform, plan.Output), "deploy", dir, in.Environment, platform, in.Notes)
	switch {
	case g.ask != nil:
		return g.ask, DeployOutput{}, nil
	case g.refusal != "":
		return nil, DeployOutput{Reason: g.refusal + ", so nothing was published", Plan: plan.Output, Command: command}, nil
	}
	if accepted, reason := evaluate(env, g.answer); !accepted {
		return nil, DeployOutput{Reason: reason, Plan: plan.Output, Command: command}, nil
	}

	result := s.run(ctx, dir, releaseArgs("deploy", in, platform))
	out := DeployOutput{Published: result.OK, Plan: plan.Output, Output: result.Output}
	if !result.OK {
		out.Reason = "the release failed; see output"
	}
	return nil, out, nil
}

func (s *Server) adopt(ctx context.Context, request *mcp.CallToolRequest, in AdoptInput) (*mcp.CallToolResult, AdoptOutput, error) {
	args := []string{"adopt", "-yes", fmt.Sprintf("-workflow=%t", in.Workflow)}
	upToDate := func(plan string) bool { return strings.Contains(plan, "Nothing to change.") }
	return s.changeFiles(ctx, request, in.Dir, args, upToDate, "the project is already set up", "adopt", fmt.Sprint(in.Workflow))
}

func (s *Server) upgrade(ctx context.Context, request *mcp.CallToolRequest, in UpgradeInput) (*mcp.CallToolResult, AdoptOutput, error) {
	args := []string{"upgrade", "-yes"}
	if in.To != "" {
		args = append(args, "-to", in.To)
	}
	// Only file changes need an answer; a pending CLI install is reported.
	upToDate := func(plan string) bool {
		for _, line := range strings.Split(plan, "\n") {
			if strings.HasPrefix(line, "  ~ ") || strings.HasPrefix(line, "  + ") {
				return false
			}
		}
		return true
	}
	return s.changeFiles(ctx, request, in.Dir, args, upToDate, "the project already pins this version (see plan for the CLI install command)", "upgrade", in.To)
}

// changeFiles runs a file-changing CLI command: plan (-dry-run), ask the
// user, then apply. upToDate is the plan text meaning nothing to do.
func (s *Server) changeFiles(ctx context.Context, request *mcp.CallToolRequest, dir string, args []string, upToDate func(string) bool, upToDateReason string, parts ...string) (*mcp.CallToolResult, AdoptOutput, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, AdoptOutput{}, err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, AdoptOutput{}, fmt.Errorf("%s is not a directory", dir)
	}
	command := fmt.Sprintf("cd %q && mobile-release %s", dir, args[0])

	plan := s.run(ctx, dir, append(append([]string{}, args...), "-dry-run"))
	if !plan.OK {
		return nil, AdoptOutput{Reason: "the plan failed; see plan", Plan: plan.Output, Command: command}, nil
	}
	if upToDate(plan.Output) {
		return nil, AdoptOutput{Reason: upToDateReason, Plan: plan.Output}, nil
	}
	question := &mcp.ElicitParams{
		Message: "mobile-release wants to change files in " + dir + ".\n\nPlan:\n" + tail(plan.Output, 6000),
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"apply": map[string]any{
					"type":        "boolean",
					"title":       "Apply these changes?",
					"description": "Nothing is written unless this is checked.",
				},
			},
			"required": []string{"apply"},
		},
	}
	g := s.gate(request, question, append(parts, dir)...)
	switch {
	case g.ask != nil:
		return g.ask, AdoptOutput{}, nil
	case g.refusal != "":
		return nil, AdoptOutput{Reason: g.refusal + ", so nothing was written", Plan: plan.Output, Command: command}, nil
	}
	if g.answer == nil || g.answer.Action != "accept" {
		return nil, AdoptOutput{Reason: "the user did not accept; nothing was written", Plan: plan.Output, Command: command}, nil
	}
	if apply, _ := g.answer.Content["apply"].(bool); !apply {
		return nil, AdoptOutput{Reason: "the user did not confirm; nothing was written", Plan: plan.Output, Command: command}, nil
	}
	result := s.run(ctx, dir, args)
	out := AdoptOutput{Applied: result.OK, Plan: plan.Output, Output: result.Output}
	if !result.OK {
		out.Reason = args[0] + " failed; see output"
	}
	return nil, out, nil
}

func clientCanAsk(request *mcp.CallToolRequest) bool {
	if request == nil || request.Session == nil {
		return false
	}
	params := request.Session.InitializeParams()
	return params != nil && params.Capabilities != nil && params.Capabilities.Elicitation != nil
}

// gateResult is the outcome of asking the user before acting.
type gateResult struct {
	// ask, when set, is returned to the client to ask the user (first round;
	// the SDK falls back to elicitation/create for older clients).
	ask *mcp.CallToolResult
	// refusal, when set, explains why the action must not happen.
	refusal string
	answer  *mcp.ElicitResult
}

// gate asks question once per request and returns the user's answer on the
// retry. The request state is signed over parts, so an answer only applies
// to the request that asked for it.
func (s *Server) gate(request *mcp.CallToolRequest, question *mcp.ElicitParams, parts ...string) gateResult {
	if !clientCanAsk(request) {
		return gateResult{refusal: "this MCP client cannot ask the user to confirm; run the command in a terminal"}
	}
	state := s.sign(parts...)
	var responses mcp.InputResponseMap
	echoed := ""
	if request.Params != nil {
		responses = request.Params.InputResponses
		echoed = request.Params.RequestState
	}
	if len(responses) == 0 {
		return gateResult{ask: &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{confirmID: question},
			RequestState:  state,
		}}
	}
	if !hmac.Equal([]byte(echoed), []byte(state)) {
		return gateResult{refusal: "the confirmation does not belong to this request"}
	}
	answer, _ := responses[confirmID].(*mcp.ElicitResult)
	return gateResult{answer: answer}
}

// sign is an HMAC with a per-process key; the client echoes it back.
func (s *Server) sign(parts ...string) string {
	s.keyOnce.Do(func() {
		s.key = make([]byte, 32)
		if _, err := rand.Read(s.key); err != nil {
			panic(err)
		}
	})
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(mac.Sum(nil))
}

func confirmation(env *config.Environment, platform, plan string) *mcp.ElicitParams {
	destinations := strings.Join(env.Destinations, ", ")
	properties := map[string]any{
		"publish": map[string]any{
			"type":        "boolean",
			"title":       fmt.Sprintf("Publish %s to %s?", env.Name, destinations),
			"description": "Nothing is published unless this is checked.",
		},
	}
	required := []string{"publish"}
	if env.NeedsConfirmation() {
		properties["environment"] = map[string]any{
			"type":        "string",
			"title":       fmt.Sprintf("Type %q to confirm", env.Name),
			"description": "Store releases require typing the environment name.",
		}
		required = append(required, "environment")
	}
	return &mcp.ElicitParams{
		Message: fmt.Sprintf("mobile-release wants to publish %s (%s) to %s.\n\nPlan (dry run):\n%s",
			env.Name, platform, destinations, tail(plan, 6000)),
		RequestedSchema: map[string]any{"type": "object", "properties": properties, "required": required},
	}
}

// evaluate accepts only an explicit "accept" with publish checked and, for
// store destinations, the environment name typed exactly.
func evaluate(env *config.Environment, answer *mcp.ElicitResult) (bool, string) {
	if answer == nil {
		return false, "no answer from the user; nothing was published"
	}
	switch answer.Action {
	case "accept":
	case "decline":
		return false, "the user declined; nothing was published"
	default:
		return false, "the user cancelled; nothing was published"
	}
	publish, _ := answer.Content["publish"].(bool)
	if !publish {
		return false, "the user did not confirm publishing; nothing was published"
	}
	if env.NeedsConfirmation() {
		typed, _ := answer.Content["environment"].(string)
		if strings.TrimSpace(typed) != env.Name {
			return false, fmt.Sprintf("the typed name did not match %q; nothing was published", env.Name)
		}
	}
	return true, ""
}

func tail(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return "…\n" + text[len(text)-max:]
}

// Run serves over stdio until the client disconnects.
func (s *Server) Run(ctx context.Context) error {
	err := s.New().Run(ctx, &mcp.StdioTransport{})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
