package sshtunnel

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/alvin/oracle-mcp-server/internal/config"
	"golang.org/x/crypto/ssh"
)

// connectBastion performs exactly one SSH login, to the configured bastion.
// The database target is used only by Tunnel.forward as a TCP destination.
func connectBastion(ctx context.Context, cfg config.SSHConfig) (*ssh.Client, string, error) {
	settings, err := clientConfig(cfg)
	if err != nil {
		return nil, "SSH_CONFIG_ERROR", err
	}
	address := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, "SSH_CONNECT_FAILED", fmt.Errorf("cannot reach bastion %s: %w", address, err)
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	conn, channels, requests, err := ssh.NewClientConn(raw, address, settings)
	stop()
	if err != nil {
		raw.Close()
		return nil, "SSH_CONNECT_FAILED", fmt.Errorf("bastion %s handshake/authentication/host-key verification failed: %w", address, err)
	}
	return ssh.NewClient(conn, channels, requests), "", nil
}
