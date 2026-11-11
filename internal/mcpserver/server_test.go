package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const releaseToml = `
schema_version = 2
[project]
type = "flutter"
[app]
name = "Example"
[android]
package_name = "com.example.app"
[ios]
bundle_id = "com.example.app"
[environments.staging]
destinations = ["firebase"]
[environments.staging.firebase]
android_app_id = "1:1:android:a"
ios_app_id = "1:1:ios:b"
[environments.production]
build_number = "store"
destinations = ["play", "testflight"]
`

type recorder struct {
	calls   [][]string
	failDry bool
}

func (r *recorder) exec(_ context.Context, _ string, args []string) (string, error) {
	r.calls = append(r.calls, args)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-dry-run") {
		if r.failDry {
			return "cannot release:\n  - missing credentials", errors.New("exit status 1")
		}
		return "Release plan\n  environment: …\nDry run finished; nothing was built or published.", nil
	}
	return "released", nil
}

func (r *recorder) published() bool {
	for _, call := range r.calls {
		joined := strings.Join(call, " ")
		if strings.HasPrefix(joined, "deploy ") && !strings.Contains(joined, "-dry-run") {
			return true
		}
	}
	return false
}

func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release.toml"), []byte(releaseToml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// connect starts the server and a client; handler nil means the client does
// not support elicitation.
func connect(t *testing.T, rec *recorder, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := (&Server{Version: "test", Exec: rec.exec}).New()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, &mcp.ClientOptions{ElicitationHandler: handler})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any, out any) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%s returned an error: %+v", tool, result.Content)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func answer(action string, content map[string]any) func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	return func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: action, Content: content}, nil
	}
}

func TestEnvironmentsTool(t *testing.T) {
	session := connect(t, &recorder{}, nil)
	var out EnvironmentsOutput
	call(t, session, "environments", map[string]any{"dir": project(t)}, &out)
	if out.ProjectType != "flutter" || len(out.Environments) != 2 {
		t.Fatalf("out = %+v", out)
	}
	production := out.Environments[0]
	if production.Name != "production" || !production.AsksToType || production.BuildNumber != "store" {
		t.Fatalf("production = %+v", production)
	}
}

func TestDeployNeverPublishesWithoutAnExplicitYes(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)
		reason  string
	}{
		{"client without elicitation", "staging", nil, "cannot ask the user"},
		{"declined", "staging", answer("decline", nil), "declined"},
		{"cancelled", "staging", answer("cancel", nil), "cancelled"},
		{"publish unchecked", "staging", answer("accept", map[string]any{"publish": false}), "did not confirm"},
		{"wrong environment name", "production", answer("accept", map[string]any{"publish": true, "environment": "prod"}), "did not match"},
		{"name missing for stores", "production", answer("accept", map[string]any{"publish": true}), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			session := connect(t, rec, tc.handler)
			arguments := map[string]any{"dir": project(t), "environment": tc.env, "platform": "android"}
			if tc.reason == "" {
				// The SDK rejects answers that do not match the schema before
				// they reach the server: the call fails and nothing publishes.
				_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "deploy", Arguments: arguments})
				if err == nil || rec.published() {
					t.Fatalf("expected a rejected answer, err=%v calls=%v", err, rec.calls)
				}
				return
			}
			var out DeployOutput
			call(t, session, "deploy", arguments, &out)
			if out.Published || rec.published() {
				t.Fatalf("published without confirmation: %+v calls=%v", out, rec.calls)
			}
			if !strings.Contains(out.Reason, tc.reason) || !strings.Contains(out.Command, "mobile-release deploy -env "+tc.env) {
				t.Fatalf("out = %+v", out)
			}
			if out.Plan == "" {
				t.Fatal("the plan must be returned so the user can review it")
			}
		})
	}
}

func TestDeployPublishesAfterConfirmation(t *testing.T) {
	var asked string
	handler := func(_ context.Context, request *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = request.Params.Message
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"publish": true, "environment": "production"}}, nil
	}
	rec := &recorder{}
	session := connect(t, rec, handler)
	var out DeployOutput
	call(t, session, "deploy", map[string]any{"dir": project(t), "environment": "production", "platform": "android", "notes": "hotfix"}, &out)
	if !out.Published || !rec.published() {
		t.Fatalf("out=%+v calls=%v", out, rec.calls)
	}
	if !strings.Contains(asked, "Plan (dry run)") || !strings.Contains(asked, "play, testflight") {
		t.Fatalf("the question must show the plan, got %q", asked)
	}
	// The plan runs again in the confirmed round, right before publishing.
	want := []string{
		"deploy -yes -env production -platform android -notes hotfix -dry-run",
		"deploy -yes -env production -platform android -notes hotfix -dry-run",
		"deploy -yes -env production -platform android -notes hotfix",
	}
	if len(rec.calls) != len(want) {
		t.Fatalf("calls = %v", rec.calls)
	}
	for i, args := range rec.calls {
		if strings.Join(args, " ") != want[i] {
			t.Fatalf("call %d = %v, want %s", i, args, want[i])
		}
	}

	// Firebase-only environments only need the checkbox.
	rec = &recorder{}
	session = connect(t, rec, answer("accept", map[string]any{"publish": true}))
	call(t, session, "deploy", map[string]any{"dir": project(t), "environment": "staging"}, &out)
	if !out.Published {
		t.Fatalf("staging = %+v", out)
	}
}

func TestDeployStopsWhenThePlanFails(t *testing.T) {
	asked := false
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = true
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"publish": true}}, nil
	}
	rec := &recorder{failDry: true}
	session := connect(t, rec, handler)
	var out DeployOutput
	call(t, session, "deploy", map[string]any{"dir": project(t), "environment": "staging"}, &out)
	if asked || out.Published || rec.published() || !strings.Contains(out.Plan, "missing credentials") {
		t.Fatalf("asked=%v out=%+v", asked, out)
	}
}

func TestReadOnlyToolsUseTheCLI(t *testing.T) {
	rec := &recorder{}
	session := connect(t, rec, nil)
	dir := project(t)
	var text TextOutput
	call(t, session, "plan", map[string]any{"dir": dir, "environment": "staging", "platform": "ios"}, &text)
	call(t, session, "doctor", map[string]any{"dir": dir, "environment": "staging"}, &text)
	call(t, session, "qa", map[string]any{"dir": dir, "platform": "android"}, &text)
	got := []string{}
	for _, args := range rec.calls {
		got = append(got, strings.Join(args, " "))
	}
	want := "deploy -yes -env staging -platform ios -dry-run|doctor -yes -env staging|qa -yes -platform android"
	if strings.Join(got, "|") != want {
		t.Fatalf("calls = %v", got)
	}
}

func TestInvalidInputs(t *testing.T) {
	session := connect(t, &recorder{}, nil)
	for _, args := range []map[string]any{
		{"dir": t.TempDir(), "environment": "staging"},
		{"dir": project(t), "environment": "nope"},
		{"dir": project(t), "environment": "staging", "platform": "windows"},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "deploy", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Fatalf("expected an error for %v", args)
		}
	}
}

func TestForgedConfirmationIsRefused(t *testing.T) {
	rec := &recorder{}
	ctx := context.Background()
	server := (&Server{Version: "test", Exec: rec.exec}).New()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	// Retries are driven by hand to forge the second round.
	client := mcp.NewClient(&mcp.Implementation{Name: "forger"}, &mcp.ClientOptions{
		ElicitationHandler: answer("accept", map[string]any{"publish": true}),
		MultiRoundTrip:     &mcp.MultiRoundTripOptions{Disabled: true},
	})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	arguments := map[string]any{"dir": project(t), "environment": "staging"}
	first, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "deploy", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if !first.NeedsInput() || first.InputRequests[confirmID] == nil {
		t.Fatalf("first round must ask the user, got %+v", first)
	}
	accepted := mcp.InputResponseMap{confirmID: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"publish": true}}}
	forged, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "deploy", Arguments: arguments, InputResponses: accepted, RequestState: "forged"})
	if err != nil {
		t.Fatal(err)
	}
	var out DeployOutput
	data, _ := json.Marshal(forged.StructuredContent)
	_ = json.Unmarshal(data, &out)
	if out.Published || rec.published() || !strings.Contains(out.Reason, "does not belong") {
		t.Fatalf("forged state accepted: %+v", out)
	}
	// The genuine state from the first round is accepted.
	genuine, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "deploy", Arguments: arguments, InputResponses: accepted, RequestState: first.RequestState})
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(genuine.StructuredContent)
	_ = json.Unmarshal(data, &out)
	if !out.Published {
		t.Fatalf("genuine confirmation refused: %+v", out)
	}
}

type adoptRecorder struct {
	calls    [][]string
	upToDate bool
}

func (r *adoptRecorder) exec(_ context.Context, _ string, args []string) (string, error) {
	r.calls = append(r.calls, args)
	if strings.Contains(strings.Join(args, " "), "-dry-run") {
		if r.upToDate {
			return "Plan (mobile-release v2.1.0)\n  = Makefile: skipped (already up to date)\n\nNothing to change.", nil
		}
		return "Plan (mobile-release v2.1.0)\n  + Makefile: create\n\nDry run: nothing was written.", nil
	}
	return "Done.", nil
}

func (r *adoptRecorder) applied() bool {
	for _, call := range r.calls {
		if call[0] == "adopt" && !strings.Contains(strings.Join(call, " "), "-dry-run") {
			return true
		}
	}
	return false
}

func connectAdopt(t *testing.T, rec *adoptRecorder, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := (&Server{Version: "test", Exec: rec.exec}).New()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, &mcp.ClientOptions{ElicitationHandler: handler}).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestAdoptAlwaysAsksBeforeWriting(t *testing.T) {
	cases := []struct {
		name    string
		handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)
		applied bool
		reason  string
	}{
		{"no elicitation", nil, false, "cannot ask the user"},
		{"declined", answer("decline", nil), false, "did not accept"},
		{"unchecked", answer("accept", map[string]any{"apply": false}), false, "did not confirm"},
		{"accepted", answer("accept", map[string]any{"apply": true}), true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &adoptRecorder{}
			session := connectAdopt(t, rec, tc.handler)
			var out AdoptOutput
			call(t, session, "adopt", map[string]any{"dir": t.TempDir(), "workflow": true}, &out)
			if out.Applied != tc.applied || rec.applied() != tc.applied || !strings.Contains(out.Reason, tc.reason) {
				t.Fatalf("out=%+v calls=%v", out, rec.calls)
			}
			if !strings.Contains(strings.Join(rec.calls[0], " "), "adopt -yes -workflow=true -dry-run") {
				t.Fatalf("plan call = %v", rec.calls[0])
			}
		})
	}
}

func TestAdoptUpToDateDoesNotAsk(t *testing.T) {
	asked := false
	rec := &adoptRecorder{upToDate: true}
	session := connectAdopt(t, rec, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = true
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"apply": true}}, nil
	})
	var out AdoptOutput
	call(t, session, "adopt", map[string]any{"dir": t.TempDir()}, &out)
	if asked || out.Applied || rec.applied() || !strings.Contains(out.Reason, "already set up") {
		t.Fatalf("asked=%v out=%+v", asked, out)
	}
}
