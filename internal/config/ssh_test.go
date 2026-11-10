package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHConfigStaysPlaintextAndResolvesPerConnection(t *testing.T) {
	path := writeTestConfig(t, `oracle:
  connections:
    a: 'u/p@//host:1521/a'
    b: 'u/p@//host:1521/b'
  connection_ssh:
    a:
      host: jump-a
      username: user-a
      password: 'keep-plain-a'
      remote_host: db-a
    b:
      host: jump-b
      port: 2222
      username: user-b
      private_key_file: keys/b
      private_key_passphrase: 'keep-plain-b'
      known_hosts_file: keys/known_hosts
      remote_host: db-b
      remote_port: 1522
`)
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ssh := cfg.SSHConnections()
	if ssh["a"].Host != "jump-a" || ssh["a"].Port != 22 || ssh["a"].RemotePort != 1521 || ssh["a"].Password != "keep-plain-a" {
		t.Fatal("wrong defaults or plaintext password")
	}
	if ssh["b"].Host != "jump-b" || ssh["b"].Port != 2222 || ssh["b"].PrivateKeyFile != filepath.Join(filepath.Dir(path), "keys/b") {
		t.Fatal("SSH connections not independent or relative path not resolved")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, literal := range []string{"password: 'keep-plain-a'", "private_key_passphrase: 'keep-plain-b'", "private_key_file: keys/b"} {
		if !strings.Contains(string(content), literal) {
			t.Fatalf("SSH config rewritten: missing %s", literal)
		}
	}
	if strings.Contains(string(content), "u/p@//host") {
		t.Fatal("database DSNs were not encrypted")
	}
}

func TestSSHConfigValidation(t *testing.T) {
	valid := SSHConfig{Host: "jump", Username: "user", RemoteHost: "db", PrivateKeyFile: "key"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*SSHConfig){
		"missing host":         func(s *SSHConfig) { s.Host = "" },
		"missing target":       func(s *SSHConfig) { s.RemoteHost = "" },
		"invalid port":         func(s *SSHConfig) { s.Port = 65536 },
		"no auth":              func(s *SSHConfig) { s.PrivateKeyFile = "" },
		"multiple auth":        func(s *SSHConfig) { s.Password = "password" },
		"multiple passphrases": func(s *SSHConfig) { s.PrivateKeyPassphrase = "plain"; s.PrivateKeyPassphraseEnv = "ENV" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := valid
			mutate(&s)
			if s.Validate() == nil {
				t.Fatal("invalid SSH config accepted")
			}
		})
	}
	c := DefaultConfig()
	c.Oracle.Connections = map[string]string{"a": "dsn"}
	c.Oracle.ConnectionSSH = map[string]SSHConfig{"typo": valid}
	if c.Validate() == nil {
		t.Fatal("unknown SSH connection accepted")
	}
}

func TestLazyModeDefaultsOnAndCanBeDisabled(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{{"", true}, {"  lazy_mode: true\n", true}, {"  lazy_mode: false\n", false}} {
		path := writeTestConfig(t, "oracle:\n"+test.value+"  connections:\n    a: 'app/pass@//db:1521/service'\n")
		cfg, err := LoadFromFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Oracle.LazyMode != test.want {
			t.Fatalf("lazy_mode=%v, expected %v", cfg.Oracle.LazyMode, test.want)
		}
	}
}
