package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alvin/oracle-mcp-server/internal/config"
)

type lazyState struct {
	opens, pings, queries atomic.Int32
	failQuery             atomic.Bool
}
type lazyConnector struct{ state *lazyState }
type lazyConn struct{ state *lazyState }
type lazyDriver struct{ state *lazyState }
type lazyRows struct{ done bool }

func (c lazyConnector) Connect(context.Context) (driver.Conn, error) { return lazyConn{c.state}, nil }
func (c lazyConnector) Driver() driver.Driver                        { return lazyDriver{c.state} }
func (d lazyDriver) Open(string) (driver.Conn, error)                { return lazyConn{d.state}, nil }
func (c lazyConn) Close() error                                      { return nil }
func (c lazyConn) Prepare(string) (driver.Stmt, error)               { return nil, errors.New("unexpected prepare") }
func (c lazyConn) Begin() (driver.Tx, error)                         { return nil, errors.New("unexpected transaction") }
func (c lazyConn) Ping(context.Context) error                        { c.state.pings.Add(1); return nil }
func (c lazyConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.state.queries.Add(1)
	if c.state.failQuery.Load() {
		return nil, errors.New("ORA-03113: connection closed after submission")
	}
	return &lazyRows{}, nil
}
func (r *lazyRows) Columns() []string { return []string{"value"} }
func (r *lazyRows) Close() error      { return nil }
func (r *lazyRows) Next(values []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	values[0] = int64(1)
	return nil
}

func installLazyOpener(p *ExecutorPool, state *lazyState) {
	p.open = func(context.Context, string) (*Executor, error) {
		state.opens.Add(1)
		return &Executor{db: sql.OpenDB(lazyConnector{state})}, nil
	}
}

func TestLazyStartupAndListingOpenNothing(t *testing.T) {
	p, err := NewExecutorPool(map[string]string{"a": "app/pass@//db:1521/service", "b": "app/pass@//db:1521/service"}, PoolOptions{SSH: map[string]config.SSHConfig{"a": {Host: "missing-jump", PrivateKeyFile: "missing-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var calls atomic.Int32
	p.open = func(context.Context, string) (*Executor, error) {
		calls.Add(1)
		return nil, errors.New("must not connect during listing")
	}
	for i := 0; i < 2; i++ {
		for _, s := range p.ListConnectionsWithStatus() {
			if !s.Available || s.ConnectionChecked || s.DatabaseConnected || s.SSHConnected || s.ErrorCode != "" {
				t.Fatalf("listing unexpectedly probed connection: %+v", s)
			}
		}
	}
	if calls.Load() != 0 || len(p.executors) != 0 || p.runtimes["a"].tunnel != nil {
		t.Fatal("lazy startup/listing opened a connection")
	}
	// Waiting for background workers must finish immediately: lazy mode starts none.
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lazy mode started background recovery")
	}
}

func TestLazyDatabaseOperationsCheckAndRecoverOnlySelectedConnection(t *testing.T) {
	p, err := NewExecutorPool(map[string]string{"a": "dsn", "b": "dsn"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	state := &lazyState{}
	installLazyOpener(p, state)
	ctx := context.Background()
	if _, err := p.Execute(ctx, "a", "select 1 from dual", "SELECT"); err != nil {
		t.Fatal(err)
	}
	first := p.executors["a"]
	if p.executors["b"] != nil || p.runtimes["b"].checked {
		t.Fatal("unused connection was initialized")
	}
	if _, err := p.ExecuteToCSVFile(ctx, "a", "select 1 from dual", filepath.Join(t.TempDir(), "query.csv")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ExecuteToTextFile(ctx, "a", "select 1 from dual", filepath.Join(t.TempDir(), "query.txt")); err != nil {
		t.Fatal(err)
	}
	if state.opens.Load() != 1 || state.pings.Load() != 2 || state.queries.Load() != 3 {
		t.Fatal("operations did not share a pool or skipped preflight")
	}
	first.Close() // Simulate a dead pool before the next operation.
	if _, err := p.Execute(ctx, "a", "select 1 from dual", "SELECT"); err != nil {
		t.Fatal(err)
	}
	if state.opens.Load() != 2 || state.queries.Load() != 4 || p.executors["a"] == first {
		t.Fatal("dead connection was not replaced before executing")
	}
	state.failQuery.Store(true)
	if _, err := p.Execute(ctx, "a", "select 1 from dual", "SELECT"); err == nil {
		t.Fatal("expected operation failure")
	}
	if state.queries.Load() != 5 || state.opens.Load() != 2 {
		t.Fatal("failed SQL was replayed")
	}
	before := state.opens.Load()
	statuses := p.ListConnectionsWithStatus()
	if !statuses[0].Available || statuses[0].ErrorCode != "DATABASE_CONNECT_FAILED" || state.opens.Load() != before {
		t.Fatal("listing retried or hid diagnostic")
	}
	state.failQuery.Store(false)
	if _, err := p.Execute(ctx, "a", "select 1 from dual", "SELECT"); err != nil {
		t.Fatal(err)
	}
	if state.opens.Load() != 3 {
		t.Fatal("next operation did not recover failed pool")
	}
}

func TestConcurrentLazyFirstUseWaitsForOneRecovery(t *testing.T) {
	p, err := NewExecutorPool(map[string]string{"a": "dsn"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	state := &lazyState{}
	installLazyOpener(p, state)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() { _, err := p.Execute(context.Background(), "a", "select 1 from dual", "SELECT"); results <- err })
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if state.opens.Load() != 1 || state.queries.Load() != 8 {
		t.Fatal("concurrent requests created duplicate pools or lost operations")
	}
}

func TestLazySSHFailureOccursOnOperationNotList(t *testing.T) {
	c := config.DefaultConfig()
	c.ConfigPath = filepath.Join(t.TempDir(), "config.yaml")
	c.Oracle.ConnectionSSH = map[string]config.SSHConfig{"a": {Host: "jump", Username: "user", PrivateKeyFile: "missing", KnownHostsFile: "missing-known-hosts", RemoteHost: "db"}}
	p, err := NewExecutorPool(map[string]string{"a": "app/pass@//db:1521/service", "b": "dsn"}, PoolOptions{SSH: c.SSHConnections()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Execute(context.Background(), "a", "select 1 from dual", "SELECT"); err == nil {
		t.Fatal("expected SSH configuration failure")
	}
	statuses := p.ListConnectionsWithStatus()
	if !statuses[0].Available || !statuses[0].ConnectionChecked || statuses[0].ErrorCode != "SSH_CONFIG_ERROR" || p.runtimes["b"].checked {
		t.Fatal("incorrect lazy failure status or unused connection was probed")
	}
}

func TestLazyRecoveryWaitHonorsCancellation(t *testing.T) {
	p, err := NewExecutorPool(map[string]string{"a": "dsn"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r := p.runtimes["a"]
	r.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = p.Execute(ctx, "a", "select 1 from dual", "SELECT")
	r.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("operation waiting for recovery ignored cancellation: %v", err)
	}
}
