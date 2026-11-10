package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SSHConfig is intentionally stored as plaintext; only oracle.connections DSNs are encrypted.
type SSHConfig struct {
	Host                    string `yaml:"host"`
	Port                    int    `yaml:"port"`
	Username                string `yaml:"username"`
	PrivateKeyFile          string `yaml:"private_key_file"`
	PrivateKeyPassphrase    string `yaml:"private_key_passphrase"`
	PrivateKeyPassphraseEnv string `yaml:"private_key_passphrase_env"`
	Password                string `yaml:"password"`
	PasswordEnv             string `yaml:"password_env"`
	KnownHostsFile          string `yaml:"known_hosts_file"`
	RemoteHost              string `yaml:"remote_host"`
	RemotePort              int    `yaml:"remote_port"`
}

func (s SSHConfig) Validate() error {
	if strings.TrimSpace(s.RemoteHost) == "" {
		return fmt.Errorf("remote_host is required")
	}
	if strings.TrimSpace(s.Host) == "" || strings.TrimSpace(s.Username) == "" {
		return fmt.Errorf("bastion host and username are required")
	}
	if s.Port < 0 || s.Port > 65535 || s.RemotePort < 0 || s.RemotePort > 65535 {
		return fmt.Errorf("ports must be between 1 and 65535 (or omitted for defaults)")
	}
	authMethods := 0
	for _, method := range []string{s.PrivateKeyFile, s.Password, s.PasswordEnv} {
		if method != "" {
			authMethods++
		}
	}
	if authMethods != 1 {
		return fmt.Errorf("specify exactly one of private_key_file, password or password_env")
	}
	if (s.PrivateKeyPassphraseEnv != "" || s.PrivateKeyPassphrase != "") && s.PrivateKeyFile == "" {
		return fmt.Errorf("private key passphrase requires private_key_file")
	}
	if s.PrivateKeyPassphraseEnv != "" && s.PrivateKeyPassphrase != "" {
		return fmt.Errorf("specify only one of private_key_passphrase or private_key_passphrase_env")
	}
	return nil
}

// SSHConnections resolves paths relative to the configuration file, without changing it.
func (c *Config) SSHConnections() map[string]SSHConfig {
	out := make(map[string]SSHConfig, len(c.Oracle.ConnectionSSH))
	for name, s := range c.Oracle.ConnectionSSH {
		out[name] = c.resolveSSH(s)
	}
	return out
}

func (c *Config) resolveSSH(s SSHConfig) SSHConfig {
	if s.Port == 0 {
		s.Port = 22
	}
	if s.RemotePort == 0 {
		s.RemotePort = 1521
	}
	if s.KnownHostsFile == "" {
		s.KnownHostsFile = "~/.ssh/known_hosts"
	}
	s.PrivateKeyFile = c.sshPath(s.PrivateKeyFile)
	s.KnownHostsFile = c.sshPath(s.KnownHostsFile)
	return s
}

func (c *Config) sshPath(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) && c.ConfigPath != "" {
		return filepath.Join(filepath.Dir(c.ConfigPath), path)
	}
	return path
}
