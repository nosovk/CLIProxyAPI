package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestManagerSessionAffinityRespectPriorityRebindsWhenHigherPriorityRecovers(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		name := "single"
		if mixed {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			provider, model := "respect-priority-"+name, "respect-priority-model"
			manager := NewManager(nil, nil, nil)
			selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
				Fallback: &FillFirstSelector{}, RebindOn: SessionAffinityRebindOnQuota, RespectPriority: true,
			})
			defer selector.Stop()
			manager.SetSelector(selector)
			manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
			high1, high2, reserve := provider+"-high1", provider+"-high2", provider+"-reserve"
			for _, id := range []string{high1, high2, reserve} {
				priority := "0"
				if id == reserve {
					priority = "-10"
				}
				_, errRegister := manager.Register(WithSkipPersist(ctx), &Auth{
					ID: id, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": priority},
				})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			opts := func() cliproxyexecutor.Options {
				return cliproxyexecutor.Options{Headers: http.Header{"Session-Id": {"respect-priority"}}}
			}
			pick := func(want string, tried ...string) {
				t.Helper()
				excluded := make(map[string]struct{})
				for _, id := range tried {
					excluded[id] = struct{}{}
				}
				options := opts()
				var picked *Auth
				var errPick error
				if mixed {
					picked, _, _, errPick = manager.pickNextMixed(ctx, []string{provider}, model, options, excluded)
				} else {
					picked, _, errPick = manager.pickNext(ctx, provider, model, options, excluded)
				}
				if errPick != nil || picked == nil || picked.ID != want {
					t.Fatalf("pick = %v (%v), want %s", picked, errPick, want)
				}
				manager.MarkResult(ctx, Result{AuthID: picked.ID, Provider: provider, Model: model, Options: options, Success: true})
			}
			fail := func(id string, status int) {
				manager.MarkResult(ctx, Result{
					AuthID: id, Provider: provider, Model: model, Options: opts(),
					Error: &Error{HTTPStatus: status, Message: http.StatusText(status)},
				})
			}
			pick(high1)
			fail(high2, http.StatusTooManyRequests)
			fail(high1, http.StatusBadGateway)
			pick(reserve, high1)
			expireSessionAffinityPriorityModelCooldown(t, manager, high1, model)
			expireSessionAffinityPriorityModelCooldown(t, manager, high2, model)
			pick(high1)
			fail(high1, http.StatusTooManyRequests)
			pick(high2, high1)
			fail(high2, http.StatusTooManyRequests)
			pick(reserve, high1, high2)
			pick(reserve)
			expireSessionAffinityPriorityModelCooldown(t, manager, high2, model)
			pick(high2)
			expireSessionAffinityPriorityModelCooldown(t, manager, high1, model)
			pick(high2)
		})
	}
}

func TestSessionAffinityRespectPriorityRecovery(t *testing.T) {
	for _, lcp := range []bool{false, true} {
		name := "explicit"
		if lcp {
			name = "lcp"
		}
		t.Run(name, func(t *testing.T) {
			selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
				Fallback: &FillFirstSelector{}, TTL: time.Hour,
				RebindOn: SessionAffinityRebindOnQuota, RespectPriority: true,
			})
			defer selector.Stop()
			high1 := &Auth{ID: "high-1", Status: StatusActive}
			high2 := &Auth{ID: "high-2", Status: StatusActive}
			reserve := &Auth{ID: "reserve", Status: StatusActive, Attributes: map[string]string{"priority": "-10"}}
			cooling := func(auth *Auth) *Auth {
				copy := auth.Clone()
				copy.Unavailable = true
				copy.NextRetryAfter = time.Now().Add(time.Hour)
				copy.LastError = &Error{HTTPStatus: 429, Message: "quota"}
				return copy
			}
			payloads := []string{
				`{"messages":[{"role":"user","content":"hello"}]}`,
				`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"question"}]}`,
				`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"question"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`,
			}
			turns := []struct {
				auths []*Auth
				want  string
			}{
				{[]*Auth{cooling(high1), cooling(high2), reserve}, reserve.ID},
				{[]*Auth{cooling(high1), high2, reserve}, high2.ID},
				{[]*Auth{high1, high2, reserve}, high2.ID},
			}
			for i, turn := range turns {
				opts := cliproxyexecutor.Options{Metadata: map[string]any{}}
				if lcp {
					opts.SourceFormat = sdktranslator.FormatOpenAI
					opts.OriginalRequest = []byte(payloads[i])
					opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey] = "respect-priority"
				} else {
					opts.Metadata[cliproxyexecutor.DerivedSessionIDMetadataKey] = "respect-priority"
				}
				picked, errPick := selector.Pick(context.Background(), "openai", "gpt-5", opts, turn.auths)
				if errPick != nil || picked == nil || picked.ID != turn.want {
					t.Fatalf("turn %d picked %v (%v), want %s", i, picked, errPick, turn.want)
				}
				selector.OnResult(Result{AuthID: picked.ID, Provider: "openai", Model: "gpt-5", Options: opts, Success: true})
			}
		})
	}
}
