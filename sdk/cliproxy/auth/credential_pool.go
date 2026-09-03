package auth

import (
	"context"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type ResolvedCredentialPool struct {
	Name   string
	Claude []string
	Codex  []string
}

type credentialPoolContextKey struct{}

func WithCredentialPool(ctx context.Context, pool *ResolvedCredentialPool) context.Context {
	if pool == nil {
		return ctx
	}
	return context.WithValue(ctx, credentialPoolContextKey{}, pool)
}

func credentialPoolFromContext(ctx context.Context) *ResolvedCredentialPool {
	if ctx == nil {
		return nil
	}
	pool, _ := ctx.Value(credentialPoolContextKey{}).(*ResolvedCredentialPool)
	return pool
}

func ResolveCredentialPoolForAPIKey(cfg *internalconfig.Config, apiKey string) *ResolvedCredentialPool {
	if cfg == nil || len(cfg.APIKeyPools) == 0 {
		return nil
	}
	apiKey = strings.TrimSpace(apiKey)
	poolName := ""
	if apiKey != "" {
		if name, ok := cfg.APIKeyPools[apiKey]; ok {
			poolName = strings.TrimSpace(name)
		}
	}
	if poolName == "" {
		if name, ok := cfg.APIKeyPools["*"]; ok {
			poolName = strings.TrimSpace(name)
		}
	}
	if poolName == "" {
		return nil
	}
	entry, ok := cfg.CredentialPools[poolName]
	if !ok {
		return &ResolvedCredentialPool{Name: poolName, Claude: []string{}, Codex: []string{}}
	}
	return &ResolvedCredentialPool{Name: poolName, Claude: entry.Claude, Codex: entry.Codex}
}

// Allows reports whether auth is usable by the downstream key resolved to this pool.
// A provider whose list is nil (not mentioned in the pool at all) stays fully
// unrestricted - that's how Claude/Gemini/everything-but-Codex passes through untouched
// when a pool only configures "codex:". A provider whose list is present but empty is a
// deliberate fail-closed: deny every credential for that provider.
func (p *ResolvedCredentialPool) Allows(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if p == nil {
		return true
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	var allowed []string
	switch provider {
	case "claude":
		allowed = p.Claude
	case "codex":
		allowed = p.Codex
	default:
		return true
	}
	if allowed == nil {
		return true
	}
	if len(allowed) == 0 {
		return false
	}
	return credentialMatchesAny(auth, allowed)
}

func credentialMatchesAny(auth *Auth, entries []string) bool {
	fileBase := credentialFileBase(auth.FileName)
	label := strings.TrimSpace(auth.Label)
	id := strings.TrimSpace(auth.ID)
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if fileBase != "" && strings.EqualFold(entry, fileBase) {
			return true
		}
		if label != "" && strings.EqualFold(entry, label) {
			return true
		}
		if id != "" && strings.EqualFold(entry, id) {
			return true
		}
	}
	return false
}

func credentialFileBase(fileName string) string {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return ""
	}
	base := fileName
	if idx := strings.LastIndexAny(base, `/\`); idx >= 0 {
		base = base[idx+1:]
	}
	return strings.TrimSuffix(base, ".json")
}
