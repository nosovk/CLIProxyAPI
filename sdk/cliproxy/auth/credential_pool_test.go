package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestResolveCredentialPoolForAPIKeyDefaults(t *testing.T) {
	cfg := &internalconfig.Config{
		CredentialPools: map[string]internalconfig.CredentialPool{
			"default": {
				Codex: []string{"codex-1", "codex-2", "codex-3"},
			},
			"paperclip": {
				Codex: []string{"codex-1", "codex-2", "codex-3", "codex-paperclip"},
			},
		},
		APIKeyPools: map[string]string{
			"sk-paperclip": "paperclip",
			"*":            "default",
		},
	}

	paperclip := ResolveCredentialPoolForAPIKey(cfg, "sk-paperclip")
	if paperclip == nil || paperclip.Name != "paperclip" {
		t.Fatalf("expected paperclip key to resolve to paperclip pool, got %+v", paperclip)
	}

	normal := ResolveCredentialPoolForAPIKey(cfg, "sk-some-other-key")
	if normal == nil || normal.Name != "default" {
		t.Fatalf("expected unmatched key to fall back to default pool, got %+v", normal)
	}

	unrestricted := ResolveCredentialPoolForAPIKey(&internalconfig.Config{}, "sk-anything")
	if unrestricted != nil {
		t.Fatalf("expected no pools configured to mean unrestricted, got %+v", unrestricted)
	}
}

func TestResolveCredentialPoolForAPIKeyUnknownPoolFailsClosed(t *testing.T) {
	cfg := &internalconfig.Config{
		APIKeyPools: map[string]string{
			"sk-broken": "does-not-exist",
		},
	}
	pool := ResolveCredentialPoolForAPIKey(cfg, "sk-broken")
	if pool == nil {
		t.Fatalf("expected a resolved (but empty) pool for an unknown pool name, got nil")
	}
	codexAuth := &Auth{Provider: "codex", FileName: "codex-1.json"}
	if pool.Allows(codexAuth) {
		t.Fatalf("expected an unknown pool to deny every credential, but codex-1 was allowed")
	}
}

func TestResolvedCredentialPoolClaudeStaysUnrestrictedWhenOnlyCodexConfigured(t *testing.T) {
	pool := &ResolvedCredentialPool{
		Name:  "paperclip",
		Codex: []string{"codex-1"},
		// Claude intentionally left nil: only Codex is pooled.
	}
	claudeAuth := &Auth{Provider: "claude", FileName: "any-claude-account.json"}
	if !pool.Allows(claudeAuth) {
		t.Fatalf("expected Claude to remain unrestricted when the pool only configures codex")
	}
	geminiAuth := &Auth{Provider: "gemini", FileName: "any-gemini-account.json"}
	if !pool.Allows(geminiAuth) {
		t.Fatalf("expected Gemini to remain unrestricted regardless of pool")
	}
}

func TestResolvedCredentialPoolAllowsMatchesByFileLabelOrID(t *testing.T) {
	pool := &ResolvedCredentialPool{
		Name:  "paperclip",
		Codex: []string{"codex-account-1", "Codex Paperclip Reserve"},
	}

	cases := []struct {
		name string
		auth *Auth
		want bool
	}{
		{"codex allowed by file name with extension", &Auth{Provider: "codex", FileName: "/root/.cli-proxy-api/codex-account-1.json"}, true},
		{"codex allowed by file name without extension", &Auth{Provider: "codex", FileName: "codex-account-1"}, true},
		{"codex allowed by label case-insensitive", &Auth{Provider: "codex", Label: "codex paperclip reserve"}, true},
		{"codex denied when not listed", &Auth{Provider: "codex", FileName: "codex-account-9.json"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pool.Allows(tc.auth); got != tc.want {
				t.Fatalf("Allows() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolvedCredentialPoolNilAllowsEverything(t *testing.T) {
	var pool *ResolvedCredentialPool
	if !pool.Allows(&Auth{Provider: "codex", FileName: "codex-1.json"}) {
		t.Fatalf("expected a nil pool to allow every credential")
	}
	if pool.Allows(nil) {
		t.Fatalf("expected a nil auth to never be allowed")
	}
}

func TestCredentialPoolAllowsAppliesThroughEligibility(t *testing.T) {
	pool := &ResolvedCredentialPool{
		Name:  "paperclip",
		Codex: []string{"codex-1"},
	}
	ctx := WithCredentialPool(context.Background(), pool)

	eligibility := authSelectionEligibilityForRequest(ctx, cliproxyexecutor.Options{})
	allowed := &Auth{Provider: "codex", FileName: "codex-1.json"}
	denied := &Auth{Provider: "codex", FileName: "codex-2.json"}
	claude := &Auth{Provider: "claude", FileName: "anything.json"}

	if !eligibility.allows(allowed) {
		t.Fatalf("expected codex-1 to be allowed for the paperclip pool")
	}
	if eligibility.allows(denied) {
		t.Fatalf("expected codex-2 to be denied for the paperclip pool")
	}
	if !eligibility.allows(claude) {
		t.Fatalf("expected claude to remain unrestricted since only codex is pooled")
	}
}
