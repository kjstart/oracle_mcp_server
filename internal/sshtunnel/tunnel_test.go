package sshtunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alvin/oracle-mcp-server/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type testSSHServer struct {
	listener    net.Listener
	mu          sync.Mutex
	connections []net.Conn
	cfg         config.SSHConfig
	checks      atomic.Int32
}

func newTestSSHServer(t *testing.T, rejectForward bool) *testSSHServer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(l.Addr().String())
	portInt, _ := strconv.Atoi(port)
	known := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(known, []byte(knownhosts.Line([]string{knownhosts.Normalize(l.Addr().String())}, signer.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &testSSHServer{listener: l, cfg: config.SSHConfig{Host: host, Port: portInt, Username: "tester", PrivateKeyFile: keyPath, KnownHostsFile: known, RemoteHost: "127.0.0.1", RemotePort: 1521}}
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(signer.PublicKey().Marshal()) {
			return nil, fmt.Errorf("unknown key")
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(signer)
	go func() {
		for {
			raw, err := l.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.connections = append(s.connections, raw)
			s.mu.Unlock()
			go func() {
				defer raw.Close()
				conn, channels, requests, err := ssh.NewServerConn(raw, serverConfig)
				if err != nil {
					return
				}
				defer conn.Close()
				go func() {
					for request := range requests {
						if request.Type == "keepalive@openssh.com" {
							s.checks.Add(1)
						}
						if request.WantReply {
							request.Reply(false, nil)
						}
					}
				}()
				for ch := range channels {
					if ch.ChannelType() != "direct-tcpip" || rejectForward {
						ch.Reject(ssh.Prohibited, "forwarding denied")
						continue
					}
					var target struct {
						Host       string
						Port       uint32
						OriginHost string
						OriginPort uint32
					}
					if ssh.Unmarshal(ch.ExtraData(), &target) != nil {
						ch.Reject(ssh.ConnectionFailed, "bad target")
						continue
					}
					remote, err := net.DialTimeout("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))), time.Second)
					if err != nil {
						ch.Reject(ssh.ConnectionFailed, "unreachable target")
						continue
					}
					channel, reqs, err := ch.Accept()
					if err != nil {
						remote.Close()
						continue
					}
					go ssh.DiscardRequests(reqs)
					go func() { defer remote.Close(); defer channel.Close(); io.Copy(remote, channel) }()
					go func() { defer remote.Close(); defer channel.Close(); io.Copy(channel, remote) }()
				}
			}()
		}
	}()
	t.Cleanup(func() { l.Close(); s.disconnect() })
	return s
}

func (s *testSSHServer) disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, conn := range s.connections {
		conn.Close()
	}
	s.connections = nil
}

func echoServer(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func connectTestTunnel(t *testing.T, cfg config.SSHConfig) *Tunnel {
	t.Helper()
	tunnel, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tunnel.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := tunnel.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	return tunnel
}

func roundTrip(address string) error {
	c, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	message := "oracle traffic over ssh"
	if _, err := io.WriteString(c, message); err != nil {
		return err
	}
	response := make([]byte, len(message))
	if _, err := io.ReadFull(c, response); err != nil {
		return err
	}
	if string(response) != message {
		return fmt.Errorf("unexpected response %q", response)
	}
	return nil
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIndependentTunnelsConcurrentForwardAndReconnect(t *testing.T) {
	a, b := newTestSSHServer(t, false), newTestSSHServer(t, false)
	a.cfg.RemotePort, b.cfg.RemotePort = echoServer(t), echoServer(t)
	ta, tb := connectTestTunnel(t, a.cfg), connectTestTunnel(t, b.cfg)
	if ta.Address() == tb.Address() {
		t.Fatal("connections share a local port")
	}
	results := make(chan error, 10)
	for i := 0; i < 5; i++ {
		go func() { results <- roundTrip(ta.Address()) }()
		go func() { results <- roundTrip(tb.Address()) }()
	}
	for i := 0; i < 10; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	address := ta.Address()
	a.disconnect()
	waitUntil(t, func() bool { connected, code, _ := ta.Status(); return !connected && code == "SSH_DISCONNECTED" })
	if err := roundTrip(tb.Address()); err != nil {
		t.Fatalf("other connection affected: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := ta.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if ta.Address() != address {
		t.Fatal("local address changed during reconnect")
	}
	if err := roundTrip(address); err != nil {
		t.Fatal(err)
	}
	// Closing an active, idle forward must not hang or leave the listener open.
	idle, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	ta.Close()
	if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("closed listener still accepts connections")
	}
}

func TestForwardFailureHasSeparateStatus(t *testing.T) {
	s := newTestSSHServer(t, true)
	tunnel := connectTestTunnel(t, s.cfg)
	if err := roundTrip(tunnel.Address()); err == nil {
		t.Fatal("denied forwarding succeeded")
	}
	waitUntil(t, func() bool { connected, code, _ := tunnel.Status(); return connected && code == "SSH_FORWARD_FAILED" })
}

func TestRejectUnknownHostKeyAndMissingKey(t *testing.T) {
	s := newTestSSHServer(t, false)
	other := newTestSSHServer(t, false)
	s.cfg.KnownHostsFile = other.cfg.KnownHostsFile
	tunnel, err := New(s.cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tunnel.Connect(ctx); err == nil {
		t.Fatal("unknown host key accepted")
	}
	connected, code, _ := tunnel.Status()
	if connected || code != "SSH_CONNECT_FAILED" {
		t.Fatalf("status: %v %s", connected, code)
	}
	s.cfg.PrivateKeyFile = filepath.Join(t.TempDir(), "missing")
	missing, err := New(s.cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Close()
	if err := missing.Connect(ctx); err == nil {
		t.Fatal("missing key accepted")
	}
	_, code, _ = missing.Status()
	if code != "SSH_CONFIG_ERROR" {
		t.Fatalf("code %s", code)
	}
}

func TestHandshakeCancellation(t *testing.T) {
	s := newTestSSHServer(t, false)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	s.cfg.Port = l.Addr().(*net.TCPAddr).Port
	tunnel, err := New(s.cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := tunnel.Connect(ctx); err == nil {
		t.Fatal("stalled handshake succeeded")
	}
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(time.Second):
		t.Fatal("no handshake connection")
	}
}

func TestBastionForwardsRawDatabaseTCPWithoutDatabaseSSH(t *testing.T) {
	bastion := newTestSSHServer(t, false)
	database, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	payload := []byte{0, 24, 0, 0, 1, 0, 0, 0, 42}
	received := make(chan []byte, 1)
	go func() {
		conn, err := database.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		data := make([]byte, len(payload))
		io.ReadFull(conn, data)
		received <- data
		conn.Write([]byte("OK"))
	}()
	bastion.cfg.RemotePort = database.Addr().(*net.TCPAddr).Port
	tunnel := connectTestTunnel(t, bastion.cfg)
	client, err := net.DialTimeout("tcp", tunnel.Address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-received:
		if string(data) != string(payload) {
			t.Fatalf("database received unexpected bytes (possibly an SSH handshake): %q", data)
		}
	case <-time.After(time.Second):
		t.Fatal("database received no TCP payload")
	}
	bastion.mu.Lock()
	logins := len(bastion.connections)
	bastion.mu.Unlock()
	if logins != 1 {
		t.Fatalf("expected exactly one SSH login to bastion, got %d", logins)
	}
}

func TestCheckRoundTripAndReconnectDeadBastion(t *testing.T) {
	s := newTestSSHServer(t, false)
	tunnel := connectTestTunnel(t, s.cfg)
	for i := 0; i < 3; i++ {
		if err := tunnel.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if s.checks.Load() != 3 {
		t.Fatal("health check did not round-trip to bastion")
	}
	s.disconnect()
	waitUntil(t, func() bool { connected, _, _ := tunnel.Status(); return !connected })
	if err := tunnel.Check(context.Background()); err == nil {
		t.Fatal("dead SSH session reported healthy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tunnel.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tunnel.Check(ctx); err != nil {
		t.Fatal(err)
	}
}
