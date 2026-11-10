package oracle

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alvin/oracle-mcp-server/internal/config"
)

func TestSSHFailuresReturnedAndLoggedWithoutDSN(t *testing.T) {
	var diagnostic bytes.Buffer
	key := filepath.Join(t.TempDir(), "missing-key")
	p, err := NewExecutorPool(map[string]string{"a": "app/secret@//db:1521/service", "b": "app/secret@//db:1521/service"}, PoolOptions{
		Eager: true,
		SSH: map[string]config.SSHConfig{
			"a": {Host: "jump-a", Port: 22, Username: "user", PrivateKeyFile: key, KnownHostsFile: key, RemoteHost: "db-a", RemotePort: 1521},
			"b": {Host: "jump-b", Port: 22, Username: "user", PrivateKeyFile: key, KnownHostsFile: key, RemoteHost: "db-b", RemotePort: 1521},
		}, ErrorLog: log.New(&diagnostic, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	statuses := p.ListConnectionsWithStatus()
	for _, s := range statuses {
		if s.Available || !s.SSHEnabled || s.SSHConnected || s.ErrorCode != "SSH_CONFIG_ERROR" || s.UserAction == "" {
			t.Fatalf("incorrect diagnostic: %+v", s)
		}
	}
	_, err = p.Execute(context.Background(), "a", "select 1 from dual", "SELECT")
	if err == nil || !strings.Contains(err.Error(), "SSH_CONFIG_ERROR") {
		t.Fatalf("missing SSH cause: %v", err)
	}
	if !strings.Contains(diagnostic.String(), "SSH_CONFIG_ERROR") || strings.Contains(diagnostic.String(), "secret") {
		t.Fatal("diagnostic missing or contains credentials")
	}
	if p.runtimes["a"].tunnel.Address() == p.runtimes["b"].tunnel.Address() {
		t.Fatal("SSH listeners shared")
	}
}

type recoveryDriver struct{}
type recoveryConn struct{}

func (recoveryDriver) Open(string) (driver.Conn, error) { return recoveryConn{}, nil }
func (recoveryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("SQL must not be replayed")
}
func (recoveryConn) Close() error               { return nil }
func (recoveryConn) Begin() (driver.Tx, error)  { return nil, errors.New("no transaction") }
func (recoveryConn) Ping(context.Context) error { return nil }
func init()                                     { sql.Register("ssh-recovery-test", recoveryDriver{}) }

func TestBackgroundRecoveryRecreatesFailedDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &ExecutorPool{ctx: ctx, cancel: cancel, executors: make(map[string]*Executor), failed: map[string]string{"a": "dsn"}, dsns: map[string]string{"a": "dsn"}, runtimes: map[string]*connectionRuntime{"a": {}}, names: []string{"a"}}
	var attempts atomic.Int32
	p.open = func(context.Context, string) (*Executor, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("database unavailable")
		}
		db, err := sql.Open("ssh-recovery-test", "")
		if err != nil {
			return nil, err
		}
		return &Executor{db: db}, nil
	}
	p.recoverConnection("a")
	p.wg.Add(1)
	go p.monitorSSH("a")
	defer p.Close()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("automatic recovery did not recreate database")
		case <-ticker.C:
			p.mu.RLock()
			ex := p.executors["a"]
			code := p.runtimes["a"].errorCode
			p.mu.RUnlock()
			if ex != nil {
				if code != "" {
					t.Fatal("recovered connection retains stale error")
				}
				if attempts.Load() != 2 {
					t.Fatal("unexpected retries")
				}
				return
			}
		}
	}
}

func TestRecoveryDoesNotHoldGlobalLockAndCannotPublishAfterClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	p := &ExecutorPool{ctx: ctx, cancel: cancel, executors: make(map[string]*Executor), failed: map[string]string{"a": "dsn"}, dsns: map[string]string{"a": "dsn"}, runtimes: map[string]*connectionRuntime{"a": {}}, names: []string{"a"}}
	p.open = func(ctx context.Context, _ string) (*Executor, error) {
		close(started)
		<-ctx.Done()
		return nil, errors.New("cancelled")
	}
	done := make(chan struct{})
	go func() { p.recoverConnection("a"); close(done) }()
	<-started
	read := make(chan struct{})
	go func() { p.Names(); p.ListConnectionsWithStatus(); close(read) }()
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("network recovery holds global lock")
	}
	p.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery not cancelled on close")
	}
	if len(p.executors) != 0 {
		t.Fatal("recovery published after close")
	}
}
