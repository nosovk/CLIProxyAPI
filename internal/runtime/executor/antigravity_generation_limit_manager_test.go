package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGenerationLimitStopsManagerRetry(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(400) }))
	defer srv.Close()
	cfg := &config.Config{}
	cfg.Antigravity.MaxGenerationBodyBytes = 1
	m := auth.NewManager(nil, &auth.RoundRobinSelector{}, nil)
	m.RegisterExecutor(NewAntigravityExecutor(cfg))
	m.SetRetryConfig(2, time.Second, 2)
	const model = "gemini-4-hydrogen-byte-limit-test"
	for _, id := range []string{"byte-limit-one", "byte-limit-two"} {
		a := &auth.Auth{ID: id, Provider: "antigravity", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "test", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "project"}, Attributes: map[string]string{"base_url": srv.URL}}
		if _, err := m.Register(ctx, a); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, a.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	for _, stream := range []bool{false, true} {
		var selected []string
		opts := ex.Options{Stream: stream, SourceFormat: tr.FormatGemini, ResponseFormat: tr.FormatGemini, Metadata: map[string]any{ex.SelectedAuthCallbackMetadataKey: func(id string) { selected = append(selected, id) }}}
		req := ex.Request{Model: model, Payload: issue7513RequestPayload(tr.FormatGemini, model, stream)}
		var err error
		if stream {
			_, err = m.ExecuteStream(ctx, []string{"antigravity"}, req, opts)
		} else {
			_, err = m.Execute(ctx, []string{"antigravity"}, req, opts)
		}
		var status ex.StatusError
		if !errors.As(err, &status) || status.StatusCode() != 413 || len(selected) != 1 || calls.Load() != 0 {
			t.Fatalf("stream=%v err=%v selected=%v upstream calls=%d", stream, err, selected, calls.Load())
		}
	}
}
