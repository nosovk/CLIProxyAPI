package config

import "encoding/json"

// providerLists keeps explicit empty lists distinct from omitted providers.
// Losing an empty list during a save would silently turn deny-all into unrestricted.
func (p CredentialPool) providerLists() map[string][]string {
	providers := make(map[string][]string)
	if p.Claude != nil {
		providers["claude"] = p.Claude
	}
	if p.Codex != nil {
		providers["codex"] = p.Codex
	}
	if p.Antigravity != nil {
		providers["antigravity"] = p.Antigravity
	}
	return providers
}

func (p CredentialPool) MarshalYAML() (any, error) {
	return p.providerLists(), nil
}

func (p CredentialPool) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.providerLists())
}
