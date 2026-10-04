package config

import (
	"strings"
	"testing"
)

// Fork-only top-level fields must survive the v8 layout migration. Any field
// absent from buildV8Paths is treated as unknown and commented out without an
// error, which would silently drop credential pool restrictions in production.
func TestV8MigrationKeepsForkOnlyFields(t *testing.T) {
	legacy := []byte(`
api-keys:
  - "sk-shared"
  - "sk-restricted"
request-log: true
request-log-format: "json"
credential-pools:
  default:
    codex:
      - codex-a
      - codex-b
  restricted:
    codex:
      - codex-b
api-key-pools:
  "sk-restricted": restricted
  "*": default
`)

	var warned []string
	SetV8MigrationWarnFunc(func(section, _ string) { warned = append(warned, section) })
	t.Cleanup(func() { SetV8MigrationWarnFunc(nil) })

	migrated, changed, err := NormalizeConfigLayout(legacy, true)
	if err != nil {
		t.Fatalf("NormalizeConfigLayout: %v", err)
	}
	if !changed {
		t.Fatalf("legacy layout should have been migrated")
	}
	if len(warned) != 0 {
		t.Fatalf("migration warned about fork fields: %v", warned)
	}
	for _, key := range []string{"credential-pools", "api-key-pools", "request-log-format"} {
		if strings.Contains(string(migrated), "# "+key+":") {
			t.Fatalf("%s was commented out by migration:\n%s", key, migrated)
		}
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatalf("migrated config is not valid v8: %v\n%s", err, migrated)
	}

	cfg, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatalf("ParseConfigBytes(migrated): %v", err)
	}
	if cfg.RequestLogFormat != "json" {
		t.Fatalf("request-log-format = %q, want json", cfg.RequestLogFormat)
	}
	if got := len(cfg.CredentialPools); got != 2 {
		t.Fatalf("credential-pools = %d, want 2", got)
	}
	if got := cfg.CredentialPools["restricted"].Codex; len(got) != 1 || got[0] != "codex-b" {
		t.Fatalf("credential-pools.restricted.codex = %v, want [codex-b]", got)
	}
	if cfg.APIKeyPools["sk-restricted"] != "restricted" || cfg.APIKeyPools["*"] != "default" {
		t.Fatalf("api-key-pools = %v", cfg.APIKeyPools)
	}

	// A second pass must leave the fork fields intact (byte-identical output).
	again, _, errAgain := NormalizeConfigLayout(migrated, true)
	if errAgain != nil {
		t.Fatalf("second migration pass: %v", errAgain)
	}
	if string(again) != string(migrated) {
		t.Fatalf("second migration pass altered output:\n--- first\n%s\n--- second\n%s", migrated, again)
	}
}
