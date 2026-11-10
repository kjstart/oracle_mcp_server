// Package sshtunnel forwards loopback TCP connections over an independently managed SSH session.
package sshtunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/alvin/oracle-mcp-server/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type Tunnel struct {
	cfg       config.SSHConfig
	listener  net.Listener
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	client    *ssh.Client
	closed    bool
	active    map[net.Conn]struct{}
	lastError string
	lastCode  string
	report    func(string, string)
	wg        sync.WaitGroup
}

func New(cfg config.SSHConfig, report func(string, string)) (*Tunnel, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("create local SSH listener: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &Tunnel{cfg: cfg, listener: l, ctx: ctx, cancel: cancel, active: make(map[net.Conn]struct{}), report: report}
	t.wg.Add(1)
	go t.accept()
	return t, nil
}

func (t *Tunnel) Address() string { return t.listener.Addr().String() }

// Check verifies the existing bastion session with a round trip before a database
// operation. A local client pointer alone does not prove that a tunnel is alive.
func (t *Tunnel) Check(parent context.Context) error {
	t.mu.Lock()
	client := t.client
	t.mu.Unlock()
	if client == nil {
		return net.ErrClosed
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); done <- err }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil && parent.Err() == nil {
		t.mu.Lock()
		current := t.client == client
		if current {
			t.client = nil
		}
		t.mu.Unlock()
		client.Close()
		if current {
			t.failure("SSH_DISCONNECTED", "Bastion liveness check failed; reconnect required before database operation")
		}
	}
	return err
}

func (t *Tunnel) Status() (connected bool, code, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.client != nil && !t.closed, t.lastCode, t.lastError
}

func (t *Tunnel) failure(code, detail string) {
	t.mu.Lock()
	changed := t.lastCode != code || t.lastError != detail
	t.lastCode, t.lastError = code, detail
	t.mu.Unlock()
	if changed && t.report != nil {
		t.report(code, detail)
	}
}

// Connect is called serially by the connection owner. TCP dial and SSH handshake are bounded by ctx.
func (t *Tunnel) Connect(ctx context.Context) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return net.ErrClosed
	}
	if t.client != nil {
		t.mu.Unlock()
		return nil
	}
	t.mu.Unlock()
	client, code, err := connectBastion(ctx, t.cfg)
	if err != nil {
		t.failure(code, err.Error())
		return err
	}
	t.mu.Lock()
	if t.closed || ctx.Err() != nil {
		t.mu.Unlock()
		client.Close()
		return net.ErrClosed
	}
	t.client = client
	t.lastCode, t.lastError = "", ""
	t.wg.Add(1)
	t.mu.Unlock()
	go t.watch(client)
	return nil
}

func clientConfig(cfg config.SSHConfig) (*ssh.ClientConfig, error) {
	hostKey, err := knownhosts.New(cfg.KnownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read known_hosts file: %w", err)
	}
	var auth ssh.AuthMethod
	if cfg.PrivateKeyFile != "" {
		key, err := os.ReadFile(cfg.PrivateKeyFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read SSH private key: %w", err)
		}
		var signer ssh.Signer
		pass := cfg.PrivateKeyPassphrase
		if cfg.PrivateKeyPassphraseEnv != "" {
			var ok bool
			pass, ok = os.LookupEnv(cfg.PrivateKeyPassphraseEnv)
			if !ok {
				return nil, fmt.Errorf("SSH key passphrase environment variable %s is not set", cfg.PrivateKeyPassphraseEnv)
			}
		}
		if pass != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(pass))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("cannot parse SSH private key (check format/passphrase)")
		}
		auth = ssh.PublicKeys(signer)
	} else {
		password := cfg.Password
		if cfg.PasswordEnv != "" {
			var ok bool
			password, ok = os.LookupEnv(cfg.PasswordEnv)
			if !ok || password == "" {
				return nil, fmt.Errorf("SSH password environment variable %s is not set or empty", cfg.PasswordEnv)
			}
		}
		auth = ssh.Password(password)
	}
	return &ssh.ClientConfig{User: cfg.Username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: hostKey}, nil
}

func (t *Tunnel) watch(client *ssh.Client) {
	defer t.wg.Done()
	done := make(chan struct{})
	go func() { client.Wait(); close(done) }()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer client.Close()
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-done:
			t.mu.Lock()
			current := t.client == client
			if current {
				t.client = nil
			}
			closed := t.closed
			t.mu.Unlock()
			if !closed && current {
				t.failure("SSH_DISCONNECTED", "SSH connection lost; check jump host/network; reconnect required before the next database operation")
			}
			return
		case <-ticker.C:
			// Any SSH reply proves liveness; some servers reply 'unsupported' to keepalive.
			replied := make(chan error, 1)
			go func() { _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); replied <- err }()
			timer := time.NewTimer(10 * time.Second)
			select {
			case <-replied:
			case <-timer.C:
				client.Close()
			case <-t.ctx.Done():
				client.Close()
			}
			timer.Stop()
		}
	}
}

func (t *Tunnel) accept() {
	defer t.wg.Done()
	for {
		local, err := t.listener.Accept()
		if err != nil {
			return
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			local.Close()
			return
		}
		t.active[local] = struct{}{}
		t.wg.Add(1)
		t.mu.Unlock()
		go t.forward(local)
	}
}

func (t *Tunnel) forward(local net.Conn) {
	defer t.wg.Done()
	defer func() { local.Close(); t.mu.Lock(); delete(t.active, local); t.mu.Unlock() }()
	t.mu.Lock()
	client := t.client
	t.mu.Unlock()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(t.ctx, 15*time.Second)
	remote, err := client.DialContext(ctx, "tcp", net.JoinHostPort(t.cfg.RemoteHost, strconv.Itoa(t.cfg.RemotePort)))
	cancel()
	if err != nil {
		t.failure("SSH_FORWARD_FAILED", "SSH host cannot forward to database target (check target address, port and forwarding permission): "+err.Error())
		return
	}
	defer remote.Close()
	t.mu.Lock()
	if t.client == client && t.lastCode == "SSH_FORWARD_FAILED" {
		t.lastCode, t.lastError = "", ""
	}
	t.mu.Unlock()
	done := make(chan struct{})
	go func() {
		_, err := io.Copy(remote, local)
		if writer, ok := remote.(interface{ CloseWrite() error }); ok && err == nil {
			writer.CloseWrite()
		} else {
			remote.Close()
		}
		close(done)
	}()
	io.Copy(local, remote)
	local.Close()
	<-done
}

func (t *Tunnel) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.cancel()
	t.listener.Close()
	if t.client != nil {
		t.client.Close()
		t.client = nil
	}
	for conn := range t.active {
		conn.Close()
	}
	t.mu.Unlock()
	t.wg.Wait()
}
