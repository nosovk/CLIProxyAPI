package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const forkLegacyConfig = `host: "127.0.0.1"
port: 8317
api-keys:
  - "sk-paperclip"
  - "sk-mia"
request-log-format: "json"
routing:
  strategy: nearest-reset
credential-pools:
  default:
    codex:
      - codex-account-1
  paperclip:
    claude:
      - claude-paperclip
    codex:
      - codex-account-1
      - codex-account-paperclip
api-key-pools:
  "sk-paperclip": paperclip
  "*": default
`

func assertForkSettings(t *testing.T, cfg *Config) {
	t.Helper()
	if cfg.RequestLogFormat != "json" {
		t.Fatalf("request-log-format = %q, want json", cfg.RequestLogFormat)
	}
	if cfg.Routing.Strategy != "nearest-reset" {
		t.Fatalf("routing.strategy = %q, want nearest-reset", cfg.Routing.Strategy)
	}
	if len(cfg.CredentialPools) != 2 || len(cfg.CredentialPools["paperclip"].Codex) != 2 || len(cfg.CredentialPools["paperclip"].Claude) != 1 {
		t.Fatalf("credential-pools lost: %+v", cfg.CredentialPools)
	}
	if cfg.APIKeyPools["sk-paperclip"] != "paperclip" || cfg.APIKeyPools["*"] != "default" {
		t.Fatalf("api-key-pools lost: %+v", cfg.APIKeyPools)
	}
}

// The v8 migration comments out unknown sections. Fork-only sections must be
// registered so a save through the v8 management API keeps them active.
func TestV8MigrationKeepsForkSections(t *testing.T) {
	logger := log.StandardLogger()
	hook := logtest.NewLocal(logger)
	t.Cleanup(hook.Reset)

	migrated, changed, err := NormalizeConfigLayout([]byte(forkLegacyConfig), true)
	if err != nil || !changed {
		t.Fatalf("NormalizeConfigLayout() error = %v, changed = %v", err, changed)
	}
	text := string(migrated)
	for _, section := range []string{"credential-pools", "api-key-pools", "request-log-format"} {
		if strings.Contains(text, "# "+section+":") {
			t.Fatalf("fork section %q was commented out during v8 migration:\n%s", section, text)
		}
	}
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "unrecognized configuration section") {
			t.Fatalf("unexpected migration warning: %s", entry.Message)
		}
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatalf("migrated fork config is not valid v8: %v\n%s", err, text)
	}
	if !strings.Contains(text, "access:") || !strings.Contains(text, "observability:") {
		t.Fatalf("migrated config is not in v8 layout:\n%s", text)
	}

	cfg, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatalf("ParseConfigBytes(migrated) error = %v", err)
	}
	assertForkSettings(t, cfg)

	legacy, err := ParseConfigBytes([]byte(forkLegacyConfig))
	if err != nil {
		t.Fatalf("ParseConfigBytes(legacy) error = %v", err)
	}
	assertForkSettings(t, legacy)
}

func TestV8ForkSectionsSurviveSave(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(forkLegacyConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Debug = true
	if err = SaveConfigPreserveComments(configPath, cfg, true); err != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", err)
	}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "config-version: 8") {
		t.Fatalf("save did not migrate to v8:\n%s", saved)
	}
	if err = ValidateV8Config(saved); err != nil {
		t.Fatalf("saved fork config is not valid v8: %v\n%s", err, saved)
	}
	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(saved) error = %v", err)
	}
	if !reloaded.Debug {
		t.Fatal("saved debug flag lost")
	}
	assertForkSettings(t, reloaded)
}
