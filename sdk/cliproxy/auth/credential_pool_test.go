package auth

import (
	"context"
	"testing"
	"time"

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

func TestCredentialPoolPaperclipFallbackScenario(t *testing.T) {
	// Scenario:
	// Accounts 1-4 are shared (priority 0).
	// Account 5 is paperclip special fallback (priority -1).
	// Default pool has accounts 1-4.
	// Paperclip pool has accounts 1-5.
	cfg := &internalconfig.Config{
		CredentialPools: map[string]internalconfig.CredentialPool{
			"default": {
				Codex: []string{"codex-1", "codex-2", "codex-3", "codex-4"},
			},
			"paperclip": {
				Codex: []string{"codex-1", "codex-2", "codex-3", "codex-4", "codex-5-special"},
			},
		},
		APIKeyPools: map[string]string{
			"sk-paperclip-key": "paperclip",
			"*":                "default",
		},
	}

	auths := []*Auth{
		{ID: "c1", Provider: "codex", FileName: "codex-1.json", Status: StatusActive, Attributes: map[string]string{"priority": "0"}},
		{ID: "c2", Provider: "codex", FileName: "codex-2.json", Status: StatusActive, Attributes: map[string]string{"priority": "0"}},
		{ID: "c3", Provider: "codex", FileName: "codex-3.json", Status: StatusActive, Attributes: map[string]string{"priority": "0"}},
		{ID: "c4", Provider: "codex", FileName: "codex-4.json", Status: StatusActive, Attributes: map[string]string{"priority": "0"}},
		{ID: "c5", Provider: "codex", FileName: "codex-5-special.json", Status: StatusActive, Attributes: map[string]string{"priority": "-1"}},
	}

	// 1. Regular client (using default pool via "*")
	defaultPool := ResolveCredentialPoolForAPIKey(cfg, "sk-regular-user")
	if defaultPool == nil || defaultPool.Name != "default" {
		t.Fatalf("expected default pool for regular user")
	}
	defaultCtx := WithCredentialPool(context.Background(), defaultPool)
	defaultEligibility := authSelectionEligibilityForRequest(defaultCtx, cliproxyexecutor.Options{})

	for _, a := range auths[:4] {
		if !defaultEligibility.allows(a) {
			t.Fatalf("regular user should have access to shared account %s", a.ID)
		}
	}
	if defaultEligibility.allows(auths[4]) {
		t.Fatalf("regular user must NOT have access to special account 5")
	}

	// 2. Paperclip client (using paperclip pool)
	paperclipPool := ResolveCredentialPoolForAPIKey(cfg, "sk-paperclip-key")
	if paperclipPool == nil || paperclipPool.Name != "paperclip" {
		t.Fatalf("expected paperclip pool for paperclip key")
	}
	paperclipCtx := WithCredentialPool(context.Background(), paperclipPool)
	paperclipEligibility := authSelectionEligibilityForRequest(paperclipCtx, cliproxyexecutor.Options{})

	for _, a := range auths {
		if !paperclipEligibility.allows(a) {
			t.Fatalf("paperclip should have access to account %s", a.ID)
		}
	}

	// 3. Priority routing verification for Paperclip:
	// Filter candidates through paperclip eligibility
	var paperclipCandidates []*Auth
	for _, a := range auths {
		if paperclipEligibility.allows(a) {
			paperclipCandidates = append(paperclipCandidates, a)
		}
	}

	// When all accounts are available, getAvailableAuths (which selects highest priority tier) must pick only from priority 0 (accounts 1-4)
	avail, err := getAvailableAuths(paperclipCandidates, "codex", "gpt-4", time.Now())
	if err != nil {
		t.Fatalf("getAvailableAuths error: %v", err)
	}
	if len(avail) != 4 {
		t.Fatalf("expected 4 shared accounts at priority 0, got %d", len(avail))
	}
	for _, a := range avail {
		if a.ID == "c5" {
			t.Fatalf("account 5 (priority -1) should not be picked while priority 0 accounts are available")
		}
	}

	// When shared accounts (1-4) are on cooldown / disabled:
	for _, a := range auths[:4] {
		a.Status = StatusDisabled
	}
	availFallback, errFallback := getAvailableAuths(paperclipCandidates, "codex", "gpt-4", time.Now())
	if errFallback != nil {
		t.Fatalf("getAvailableAuths fallback error: %v", errFallback)
	}
	if len(availFallback) != 1 || availFallback[0].ID != "c5" {
		t.Fatalf("expected fallback to account 5 (priority -1) when 1-4 are unavailable, got %+v", availFallback)
	}
}
