package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCredentialLoadTrackerCountsInFlightAndDecaysRateLimits(t *testing.T) {
	tracker := newCredentialLoadTracker()
	now := time.Unix(1_700_000_000, 0)
	tracker.now = func() time.Time { return now }
	auths := []*Auth{{ID: "a"}, {ID: "b"}}

	releaseA := tracker.acquire("a")
	releaseA2 := tracker.acquire("a")
	if got := tracker.scores(auths); got[0] != 2 || got[1] != 0 {
		t.Fatalf("scores = %v, want [2 0]", got)
	}
	releaseA()
	releaseA()
	if got := tracker.scores(auths); got[0] != 1 {
		t.Fatalf("score after double release = %v, want 1", got[0])
	}
	releaseA2()

	tracker.observeResult(Result{AuthID: "b", Error: &Error{HTTPStatus: http.StatusTooManyRequests}})
	tracker.observeResult(Result{AuthID: "b", Error: &Error{HTTPStatus: http.StatusInternalServerError}})
	if got := tracker.scores(auths); got[0] != 0 || got[1] != credentialRateLimitPenalty {
		t.Fatalf("scores after 429 = %v, want [0 %v]", got, credentialRateLimitPenalty)
	}
	now = now.Add(credentialRateLimitWindow / 2)
	if got := tracker.scores(auths)[1]; got != credentialRateLimitPenalty/2 {
		t.Fatalf("half-decayed penalty = %v, want %v", got, credentialRateLimitPenalty/2)
	}
	now = now.Add(credentialRateLimitWindow)
	if got := tracker.scores(auths)[1]; got != 0 {
		t.Fatalf("expired penalty = %v, want 0", got)
	}
}

func TestSessionAffinityLoadAwareKeepsBindingsAndSpreadsNewSessions(t *testing.T) {
	tracker := newCredentialLoadTracker()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:  &FillFirstSelector{},
		TTL:       time.Hour,
		LoadAware: true,
	})
	defer selector.Stop()
	attachCredentialLoad(selector, tracker)
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}, {ID: "auth-c"}}
	ctx := context.Background()
	pick := func(session string) string {
		t.Helper()
		opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {session}}}
		auth, errPick := selector.Pick(ctx, "codex", "gpt-load", opts, auths)
		if errPick != nil || auth == nil {
			t.Fatalf("Pick(%s) = %v, %v", session, auth, errPick)
		}
		return auth.ID
	}

	if got := pick("session-1"); got != "auth-a" {
		t.Fatalf("first session bound to %s, want auth-a", got)
	}
	defer tracker.acquire("auth-a")()
	defer tracker.acquire("auth-a")()

	if got := pick("session-1"); got != "auth-a" {
		t.Fatalf("established session moved to %s under load, want auth-a", got)
	}
	if got := pick("session-2"); got != "auth-b" {
		t.Fatalf("new session bound to %s, want least loaded auth-b", got)
	}

	tracker.observeResult(Result{AuthID: "auth-b", Error: &Error{HTTPStatus: http.StatusTooManyRequests}})
	if got := pick("session-3"); got != "auth-c" {
		t.Fatalf("new session bound to %s, want auth-c after auth-b was rate limited", got)
	}
	if got := pick("session-2"); got != "auth-b" {
		t.Fatalf("established session moved to %s after 429 penalty, want auth-b", got)
	}
}

func TestSessionAffinityLoadToleranceDefersToFallbackOrder(t *testing.T) {
	tracker := newCredentialLoadTracker()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:      &FillFirstSelector{},
		LoadAware:     true,
		LoadTolerance: 1,
	})
	defer selector.Stop()
	attachCredentialLoad(selector, tracker)
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	defer tracker.acquire("auth-a")()

	opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"tolerated"}}}
	if auth, _ := selector.Pick(context.Background(), "codex", "gpt-load", opts, auths); auth == nil || auth.ID != "auth-a" {
		t.Fatalf("Pick() = %v, want auth-a within tolerance", auth)
	}
	defer tracker.acquire("auth-a")()
	opts = cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"over-tolerance"}}}
	if auth, _ := selector.Pick(context.Background(), "codex", "gpt-load", opts, auths); auth == nil || auth.ID != "auth-b" {
		t.Fatalf("Pick() = %v, want auth-b beyond tolerance", auth)
	}
}

func TestSessionAffinityWithoutLoadAwareIgnoresLoad(t *testing.T) {
	tracker := newCredentialLoadTracker()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &FillFirstSelector{}})
	defer selector.Stop()
	attachCredentialLoad(selector, tracker)
	defer tracker.acquire("auth-a")()

	opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"plain"}}}
	auth, _ := selector.Pick(context.Background(), "codex", "gpt-load", opts, []*Auth{{ID: "auth-a"}, {ID: "auth-b"}})
	if auth == nil || auth.ID != "auth-a" {
		t.Fatalf("Pick() = %v, want auth-a", auth)
	}
}

type loadTrackingExecutor struct {
	manager      *Manager
	stream       chan cliproxyexecutor.StreamChunk
	executeLoad  float64
	executeError error
	executePanic any
}

func (e *loadTrackingExecutor) Identifier() string { return "codex" }

func (e *loadTrackingExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeLoad = e.manager.credentialLoad.scores([]*Auth{auth})[0]
	if e.executePanic != nil {
		panic(e.executePanic)
	}
	if e.executeError != nil {
		return cliproxyexecutor.Response{}, e.executeError
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *loadTrackingExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{Chunks: e.stream}, nil
}

func (e *loadTrackingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *loadTrackingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *loadTrackingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func setupLoadTrackingManager(t *testing.T, authID string) (*Manager, *loadTrackingExecutor, *Auth) {
	t.Helper()
	const model = "gpt-load-tracking"
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 1)
	executor := &loadTrackingExecutor{manager: manager, stream: make(chan cliproxyexecutor.StreamChunk, 2)}
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: authID, Provider: "codex", Status: StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	return manager, executor, auth
}

func TestManagerExecuteTracksCredentialLoad(t *testing.T) {
	manager, executor, auth := setupLoadTrackingManager(t, "codex-load-execute")
	if _, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-load-tracking"}, cliproxyexecutor.Options{}); errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	if executor.executeLoad != 1 {
		t.Fatalf("load during Execute = %v, want 1", executor.executeLoad)
	}
	if got := manager.credentialLoad.scores([]*Auth{auth})[0]; got != 0 {
		t.Fatalf("load after Execute = %v, want 0", got)
	}

	executor.executeError = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
	_, _ = manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-load-tracking"}, cliproxyexecutor.Options{})
	if got := manager.credentialLoad.scores([]*Auth{auth})[0]; got <= 0 {
		t.Fatalf("load after 429 = %v, want rate-limit penalty", got)
	}
}

func TestManagerExecuteReleasesCredentialLoadOnPanic(t *testing.T) {
	manager, executor, auth := setupLoadTrackingManager(t, "codex-load-panic")
	executor.executePanic = "boom"

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected Execute panic")
			}
		}()
		_, _ = manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-load-tracking"}, cliproxyexecutor.Options{})
	}()

	if executor.executeLoad != 1 {
		t.Fatalf("load during Execute = %v, want 1", executor.executeLoad)
	}
	if got := manager.credentialLoad.scores([]*Auth{auth})[0]; got != 0 {
		t.Fatalf("load after recovered Execute panic = %v, want 0", got)
	}
}

func TestManagerExecuteStreamHoldsCredentialLoadUntilStreamEnds(t *testing.T) {
	manager, executor, auth := setupLoadTrackingManager(t, "codex-load-stream")
	executor.stream <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.created\"}\n\n")}

	result, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-load-tracking"}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	<-result.Chunks
	if got := manager.credentialLoad.scores([]*Auth{auth})[0]; got != 1 {
		t.Fatalf("load while streaming = %v, want 1", got)
	}

	close(executor.stream)
	for range result.Chunks {
	}
	if got := manager.credentialLoad.scores([]*Auth{auth})[0]; got != 0 {
		t.Fatalf("load after stream end = %v, want 0", got)
	}
}
