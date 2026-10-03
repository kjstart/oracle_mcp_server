package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessConnectionsEncryptsPlaintext(t *testing.T) {
	t.Parallel()

	plain, encrypted, changed, err := processConnections(map[string]string{
		"play": "play/pass@//host:1521/demo",
	})
	if err != nil {
		t.Fatalf("processConnections returned error: %v", err)
	}
	if !changed {
		t.Fatalf("processConnections changed = false, want true")
	}
	if plain["play"] != "play/pass@//host:1521/demo" {
		t.Fatalf("plain connection = %q", plain["play"])
	}
	if !isEncryptedConnectionValue(encrypted["play"]) {
		t.Fatalf("encrypted connection = %q, want encrypted prefix", encrypted["play"])
	}
}

func TestProcessConnectionsDecryptsEncrypted(t *testing.T) {
	t.Parallel()

	ciphertext, err := encryptConnectionValue("play/pass@//host:1521/demo")
	if err != nil {
		t.Fatalf("encryptConnectionValue returned error: %v", err)
	}

	plain, encrypted, changed, err := processConnections(map[string]string{
		"play": ciphertext,
	})
	if err != nil {
		t.Fatalf("processConnections returned error: %v", err)
	}
	if changed {
		t.Fatalf("processConnections changed = true, want false")
	}
	if plain["play"] != "play/pass@//host:1521/demo" {
		t.Fatalf("plain connection = %q", plain["play"])
	}
	if encrypted["play"] != ciphertext {
		t.Fatalf("encrypted connection changed unexpectedly")
	}
}

func TestLoadFromFileEncryptsConnectionsOnDiskAndReturnsPlaintext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `oracle:
  connections:
    play: "play/pass@//host:1521/demo"
security:
  danger_keyword_match: "whole_text"
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile returned error: %v", err)
	}
	if cfg.Oracle.Connections["play"] != "play/pass@//host:1521/demo" {
		t.Fatalf("loaded connection = %q", cfg.Oracle.Connections["play"])
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !strings.Contains(string(updated), connectionPrefix) {
		t.Fatalf("updated config does not contain encrypted connection: %s", string(updated))
	}
	if strings.Contains(string(updated), "play/pass@//host:1521/demo") {
		t.Fatalf("updated config still contains plaintext connection: %s", string(updated))
	}
}

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	return path
}

func TestSecurityProfilesPerConnection(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, `oracle:
  connections:
    prod: "u/p@//h:1521/prod"
    dev: "u/p@//h:1521/dev"
    other: "u/p@//h:1521/other"
  connection_security:
    prod: strict
    dev: relaxed
security:
  danger_keywords: [Drop]
security_profiles:
  strict:
    danger_keyword_match: Tokens
    danger_keywords: [Drop, Update]
  relaxed:
    require_confirm_for_ddl: false
`)
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile returned error: %v", err)
	}

	sec, name := cfg.SecurityFor("prod")
	if name != "strict" || sec.DangerKeywordMatch != "tokens" || len(sec.DangerKeywords) != 2 || sec.DangerKeywords[1] != "update" || !sec.RequireConfirmForDDL {
		t.Fatalf("prod security = %q %+v", name, sec)
	}
	sec, name = cfg.SecurityFor("dev")
	if name != "relaxed" || sec.RequireConfirmForDDL || sec.DangerKeywordMatch != "whole_text" || len(sec.DangerKeywords) != len(defaultSecurityConfig().DangerKeywords) {
		t.Fatalf("dev security = %q %+v (omitted fields should take built-in defaults)", name, sec)
	}
	sec, name = cfg.SecurityFor("other")
	if name != DefaultSecurityProfileName || len(sec.DangerKeywords) != 1 || sec.DangerKeywords[0] != "drop" {
		t.Fatalf("other security = %q %+v", name, sec)
	}
}

func TestSecurityProfileValidation(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"unknown profile": `oracle:
  connections:
    a: "u/p@//h:1521/a"
  connection_security:
    a: nope
`,
		"unknown connection": `oracle:
  connections:
    a: "u/p@//h:1521/a"
  connection_security:
    b: default
`,
		"reserved name": `oracle:
  connections:
    a: "u/p@//h:1521/a"
security_profiles:
  default: {}
`,
		"bad match mode": `oracle:
  connections:
    a: "u/p@//h:1521/a"
security_profiles:
  x:
    danger_keyword_match: fuzzy
`,
	}
	for name, content := range cases {
		content := content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := LoadFromFile(writeTestConfig(t, content)); err == nil {
				t.Fatalf("LoadFromFile succeeded, want error")
			}
		})
	}
}

func TestEncryptionOnlyTouchesConnectionsBlock(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, `oracle:
  connections:
    prod: "u/p@//h:1521/prod"
  connection_security:
    prod: strict
security_profiles:
  strict:
    danger_keywords: [prod]
`)
	if _, err := LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile returned error: %v", err)
	}
	updated, _ := os.ReadFile(path)
	text := string(updated)
	if !strings.Contains(text, "    prod: strict") {
		t.Fatalf("connection_security entry was rewritten: %s", text)
	}
	if !strings.Contains(text, connectionPrefix) || strings.Contains(text, "u/p@//h:1521/prod") {
		t.Fatalf("connection not encrypted: %s", text)
	}
	if _, err := LoadFromFile(path); err != nil {
		t.Fatalf("reload after encryption failed: %v", err)
	}
}
