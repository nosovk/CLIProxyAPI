package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCredentialPoolAntigravityIsolation(t *testing.T) {
	cfg := &internalconfig.Config{
		CredentialPools: map[string]internalconfig.CredentialPool{
			"default":   {Antigravity: []string{"antigravity-nosovk.json"}},
			"paperclip": {Antigravity: []string{"antigravity-nosovk.json", "antigravity-olli.json"}},
			"mia":       {Antigravity: []string{"antigravity-nosovk.json", "antigravity-olli.json"}},
			"vip-crm":   {Antigravity: []string{}},
			"legacy":    {Codex: []string{"codex-1"}},
		},
		APIKeyPools: map[string]string{
			"*": "default", "paperclip-key": "paperclip", "mia-key": "mia",
			"vip-key": "vip-crm", "legacy-key": "legacy", "broken-key": "missing",
		},
	}
	for _, tc := range []struct {
		key          string
		nosovk, olli bool
	}{
		{"default-key", true, false}, {"paperclip-key", true, true},
		{"mia-key", true, true}, {"vip-key", false, false},
		{"legacy-key", true, true}, {"broken-key", false, false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			ctx := WithCredentialPool(context.Background(), ResolveCredentialPoolForAPIKey(cfg, tc.key))
			eligibility := authSelectionEligibilityForRequest(ctx, cliproxyexecutor.Options{})
			for id, want := range map[string]bool{"antigravity-nosovk.json": tc.nosovk, "antigravity-olli.json": tc.olli} {
				if got := eligibility.allows(&Auth{ID: id, Provider: "antigravity"}); got != want {
					t.Fatalf("allows(%s) = %v, want %v", id, got, want)
				}
			}
			if !eligibility.allows(&Auth{ID: "gemini-1", Provider: "gemini"}) {
				t.Fatal("native Gemini must remain unaffected by Antigravity restrictions")
			}
		})
	}
}

func TestCredentialPoolAntigravityCreditsCannotBypassRestrictions(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			const model = "claude-pool-credits-test"
			executor := &antigravityCreditsFallbackExecutor{}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true}})
			manager.RegisterExecutor(executor)
			a := &Auth{ID: "foreign-credits-" + name, Provider: "antigravity", Status: StatusActive}
			if _, err := manager.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(a.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
			ctx := WithCredentialPool(context.Background(), &ResolvedCredentialPool{Antigravity: []string{}})
			var err error
			if stream {
				_, err = manager.ExecuteStream(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			} else {
				_, err = manager.Execute(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			}
			if err == nil || len(executor.executeCreditsRequested) != 0 || len(executor.streamCreditsRequested) != 0 {
				t.Fatalf("denied credential reached credits fallback: err=%v, execute=%v, stream=%v", err, executor.executeCreditsRequested, executor.streamCreditsRequested)
			}
		})
	}
}
