package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

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
