package handlers

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestGetContextWithCancelEnforcesRequestCredentialPool(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			model := "pool-context-" + provider
			called := false
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(&modelExecutionCaptureExecutor{
				provider: provider,
				execute: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
					called = true
					return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
				},
			})
			a := &coreauth.Auth{ID: "foreign-reserve-" + provider, Provider: provider, Status: coreauth.StatusActive}
			if _, err := manager.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(a.ID, provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
			pool := &coreauth.ResolvedCredentialPool{Name: "paperclip", Claude: []string{"subscription-only"}, Codex: []string{"subscription-only"}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(coreauth.WithCredentialPool(context.Background(), pool))
			h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			ctx, cancel := h.GetContextWithCancel(nil, c, context.Background())
			defer cancel()
			_, errMsg := h.ExecuteModel(ctx, ModelExecutionRequest{EntryProtocol: "openai", ExitProtocol: "openai", Model: model, Body: []byte(`{}`), ForcedProvider: provider})
			if called || errMsg == nil {
				t.Fatal("request pool was lost: foreign reserve executed")
			}
		})
	}
}
