package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alvin/oracle-mcp-server/internal/config"
)

func TestSSHStatusInMCPAndSeparateErrorLog(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Oracle.LazyMode = false
	cfg.ConfigPath = filepath.Join(dir, "config.yaml")
	cfg.Oracle.Connections = map[string]string{"test": "app/secret@//db:1521/service"}
	cfg.Oracle.ConnectionSSH = map[string]config.SSHConfig{"test": {Host: "jump", Username: "user", PrivateKeyFile: "missing-key", KnownHostsFile: "missing-known-hosts", RemoteHost: "db"}}
	server, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var response bytes.Buffer
	server.writer = &response
	server.handleListConnections(&jsonRPCRequest{ID: 1})
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Result.Content) != 1 {
		t.Fatalf("unexpected response: %s", response.String())
	}
	var status struct {
		Connections []struct {
			SSHEnabled   bool   `json:"ssh_enabled"`
			SSHConnected bool   `json:"ssh_connected"`
			ErrorCode    string `json:"error_code"`
			UserAction   string `json:"user_action"`
		} `json:"connections"`
	}
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Connections) != 1 || !status.Connections[0].SSHEnabled || status.Connections[0].SSHConnected || status.Connections[0].ErrorCode != "SSH_CONFIG_ERROR" || status.Connections[0].UserAction == "" {
		t.Fatalf("missing diagnostic: %+v", status)
	}
	response.Reset()
	server.handleExecuteSQL(&jsonRPCRequest{ID: 2}, map[string]interface{}{"sql": "select 1 from dual", "connection": "test"})
	if !strings.Contains(response.String(), "SSH_CONFIG_ERROR") {
		t.Fatal("SQL error omitted SSH cause")
	}
	server.Close()
	diagnostic, err := os.ReadFile(filepath.Join(dir, "error.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(diagnostic), "SSH_CONFIG_ERROR") || strings.Contains(string(diagnostic), "secret") {
		t.Fatal("diagnostic missing or contains credentials")
	}
	logs, err := filepath.Glob(filepath.Join(dir, "audit_*.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range logs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "SSH_CONFIG_ERROR") {
			t.Fatal("SSH diagnostic was written to audit log")
		}
	}
}

func TestRunCancellationWithIdleStdin(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	server := &Server{reader: bufio.NewReader(reader), writer: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stdin prevented shutdown")
	}
}
