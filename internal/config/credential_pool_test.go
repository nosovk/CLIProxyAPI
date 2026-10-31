package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCredentialPoolSerializationPreservesProviderRestrictions(t *testing.T) {
	for name, pool := range map[string]CredentialPool{
		"omitted":   {},
		"deny-all":  {Claude: []string{}, Codex: []string{}, Antigravity: []string{}},
		"allowlist": {Antigravity: []string{"antigravity-olli.json"}},
	} {
		t.Run(name, func(t *testing.T) {
			for format, codec := range map[string]struct {
				marshal   func(any) ([]byte, error)
				unmarshal func([]byte, any) error
			}{
				"yaml": {yaml.Marshal, yaml.Unmarshal},
				"json": {json.Marshal, json.Unmarshal},
			} {
				t.Run(format, func(t *testing.T) {
					data, err := codec.marshal(pool)
					if err != nil {
						t.Fatal(err)
					}
					var got CredentialPool
					if err := codec.unmarshal(data, &got); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, pool) {
						t.Fatalf("restrictions changed: got %#v, want %#v; serialized: %s", got, pool, data)
					}
				})
			}
		})
	}
}

func TestCredentialPoolSaveAddsEmptyAntigravityRestriction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("config-version: 8\naccess:\n  credential-pools:\n    vip-crm:\n      codex: [codex-1]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	pool := cfg.CredentialPools["vip-crm"]
	pool.Antigravity = []string{}
	cfg.CredentialPools["vip-crm"] = pool
	if err := SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.CredentialPools["vip-crm"].Antigravity == nil {
		t.Fatal("save dropped the newly configured Antigravity deny-all restriction")
	}
}

func TestCredentialPoolSavePolicyTransitions(t *testing.T) {
	for name, initial := range map[string]string{
		"new-section":        "port: 8317\n",
		"new-pool":           "credential-pools:\n  other:\n    codex: [other]\n",
		"null-to-empty":      "credential-pools:\n  restricted:\n    antigravity: null\n",
		"allowlist-to-empty": "credential-pools:\n  restricted:\n    antigravity: [old]\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.CredentialPools == nil {
				cfg.CredentialPools = make(map[string]CredentialPool)
			}
			want := CredentialPool{Claude: []string{}, Codex: []string{}, Antigravity: []string{}}
			cfg.CredentialPools["restricted"] = want
			for range 2 {
				if err := SaveConfigPreserveComments(path, cfg); err != nil {
					t.Fatal(err)
				}
				cfg, err = LoadConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := cfg.CredentialPools["restricted"]; !reflect.DeepEqual(got, want) {
					t.Fatalf("deny-all changed: got %#v, want %#v", got, want)
				}
			}
			cfg.CredentialPools["restricted"] = CredentialPool{}
			if err := SaveConfigPreserveComments(path, cfg); err != nil {
				t.Fatal(err)
			}
			cfg, err = LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.CredentialPools["restricted"]; !reflect.DeepEqual(got, CredentialPool{}) {
				t.Fatalf("removed restrictions persisted: %#v", got)
			}
		})
	}
}
