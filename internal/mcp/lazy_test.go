package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/alvin/oracle-mcp-server/internal/config"
)

func TestLazyMCPListingDoesNotCreateSSHOrDatabaseConnections(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.ConfigPath = filepath.Join(dir, "config.yaml")
	cfg.Oracle.Connections = map[string]string{"a": "app/pass@//db:1521/service", "b": "app/pass@//db:1521/service"}
	cfg.Oracle.ConnectionSSH = map[string]config.SSHConfig{"a": {Host: "nonexistent-bastion", Username: "user", PrivateKeyFile: "missing", RemoteHost: "db"}}
	server, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var output bytes.Buffer
	server.writer = &output
	for i := 0; i < 2; i++ {
		output.Reset()
		server.handleListConnections(&jsonRPCRequest{ID: i + 1})
		var envelope struct {
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		var listing struct {
			Lazy        bool `json:"lazy_mode"`
			Connections []struct {
				Available bool   `json:"available"`
				Checked   bool   `json:"connection_checked"`
				SSH       bool   `json:"ssh_connected"`
				DB        bool   `json:"database_connected"`
				Code      string `json:"error_code"`
			} `json:"connections"`
		}
		if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &listing); err != nil {
			t.Fatal(err)
		}
		if !listing.Lazy || len(listing.Connections) != 2 {
			t.Fatal("incorrect lazy listing")
		}
		for _, s := range listing.Connections {
			if !s.Available || s.Checked || s.SSH || s.DB || s.Code != "" {
				t.Fatalf("listing probed a connection: %+v", s)
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "error.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatal("startup/listing attempted SSH with missing key")
	}
}
