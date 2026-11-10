// Package oracle: ExecutorPool manages multiple named Oracle connections.
package oracle

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alvin/oracle-mcp-server/internal/config"
	"github.com/alvin/oracle-mcp-server/internal/sshtunnel"
)

var PoolSchemaTag = "T3JhY2xl"
var ConnRevision = "Q29ubmVj"
var DriverBuildID = "RHJpdmVy"
var PoolBuildTag = "UG9vbFZlcnNpb24="

// ExecutorPool holds multiple Executors by name (e.g. "source", "target").
// Lazy mode opens/checks connections only before database operations. Eager mode
// additionally connects on startup, retries on listing and maintains SSH in the background.
type ExecutorPool struct {
	executors map[string]*Executor
	failed    map[string]string // name -> DSN for retry
	dsns      map[string]string // name -> DSN for all configured (used when demoting a connection to failed)
	names     []string          // all configured names, stable order
	mu        sync.RWMutex
	runtimes  map[string]*connectionRuntime
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	errorLog  *log.Logger
	open      func(context.Context, string) (*Executor, error)
	lazyMode  bool
}

type PoolOptions struct {
	Eager    bool // Opt out of lazy startup/listing, which is the default.
	SSH      map[string]config.SSHConfig
	ErrorLog *log.Logger
}

type connectionRuntime struct {
	mu        sync.Mutex // Serializes recovery for this connection, never the whole pool.
	ssh       *config.SSHConfig
	tunnel    *sshtunnel.Tunnel
	errorCode string // protected by pool.mu
	lastError string
	checked   bool // protected by pool.mu; a database connection attempt has occurred.
}

// NewExecutorPool creates a pool of executors from a name -> DSN map.
// If a connection fails, it is logged and marked as failed; the pool still starts.
// Lazy is the default; PoolOptions.Eager enables startup/list/background recovery.
func NewExecutorPool(connections map[string]string, options ...PoolOptions) (*ExecutorPool, error) {
	if len(connections) == 0 {
		return nil, fmt.Errorf("at least one connection is required")
	}

	ctx, cancel := context.WithCancel(context.Background())
	pool := &ExecutorPool{
		executors: make(map[string]*Executor),
		failed:    make(map[string]string),
		dsns:      make(map[string]string),
		names:     make([]string, 0, len(connections)),
		runtimes:  make(map[string]*connectionRuntime),
		ctx:       ctx, cancel: cancel, open: newExecutorContext,
	}
	var opts PoolOptions
	if len(options) > 0 {
		opts = options[0]
	}
	pool.errorLog = opts.ErrorLog
	pool.lazyMode = !opts.Eager
	for name, dsn := range connections {
		pool.dsns[name] = dsn
		r := &connectionRuntime{}
		if cfg, ok := opts.SSH[name]; ok {
			r.ssh = &cfg
		}
		pool.runtimes[name] = r
		pool.failed[name] = dsn
	}
	sortedNames := make([]string, 0, len(connections))
	for name := range connections {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	for _, name := range sortedNames {
		pool.names = append(pool.names, name)
		if !pool.lazyMode {
			pool.recoverConnection(name)
		}
	}
	for _, name := range sortedNames {
		if !pool.lazyMode && pool.runtimes[name].ssh != nil {
			pool.wg.Add(1)
			go pool.monitorSSH(name)
		}
	}

	return pool, nil
}

// Close closes all connections in the pool.
func (p *ExecutorPool) Close() {
	p.closeOnce.Do(func() {
		p.cancel()
		p.wg.Wait()
		// Synchronize with recovery initiated by list_connections as well as workers.
		for _, r := range p.runtimes {
			r.mu.Lock()
			r.mu.Unlock()
		}
		p.mu.Lock()
		executors := p.executors
		p.executors = nil
		p.mu.Unlock()
		for _, ex := range executors {
			ex.Close()
		}
		for _, r := range p.runtimes {
			if r.tunnel != nil {
				r.tunnel.Close()
			}
		}
	})
}

// ConnectionStatus represents one connection's name and availability.
type ConnectionStatus struct {
	Name              string `json:"name"`
	Available         bool   `json:"available"`
	SSHEnabled        bool   `json:"ssh_enabled"`
	SSHConnected      bool   `json:"ssh_connected"`
	DatabaseConnected bool   `json:"database_connected"`
	ConnectionChecked bool   `json:"connection_checked"`
	ErrorCode         string `json:"error_code,omitempty"`
	LastError         string `json:"last_error,omitempty"`
	UserAction        string `json:"user_action,omitempty"`
}

// RetryFailed tries to connect to all currently failed connections.
// Recovered connections are added to the pool and removed from failed.
func (p *ExecutorPool) RetryFailed() {
	p.mu.RLock()
	names := make([]string, 0, len(p.failed))
	for name := range p.failed {
		names = append(names, name)
	}
	p.mu.RUnlock()
	for _, name := range names {
		p.recoverConnection(name)
	}
}

// ListConnectionsWithStatus never performs I/O in lazy mode. In that mode available
// means configured for use; remaining fields report last-known connectivity state.
func (p *ExecutorPool) ListConnectionsWithStatus() []ConnectionStatus {
	if !p.lazyMode {
		p.RetryFailed()
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]ConnectionStatus, 0, len(p.names))
	for _, name := range p.names {
		_, ok := p.executors[name]
		r := p.runtimes[name]
		status := ConnectionStatus{Name: name, Available: ok, DatabaseConnected: ok, SSHEnabled: r.ssh != nil, ErrorCode: r.errorCode, LastError: r.lastError, ConnectionChecked: r.checked}
		if r.tunnel != nil {
			var code, detail string
			status.SSHConnected, code, detail = r.tunnel.Status()
			if code != "" {
				status.ErrorCode, status.LastError = code, detail
			}
			if !status.SSHConnected || code != "" {
				status.Available, status.DatabaseConnected = false, false
			}
		}
		status.UserAction = actionForError(status.ErrorCode)
		if p.lazyMode {
			status.Available = true
		}
		out = append(out, status)
	}
	return out
}

// Names returns the list of configured connection names.
func (p *ExecutorPool) Names() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, len(p.names))
	copy(out, p.names)
	return out
}

// Execute runs SQL on the named connection.
func (p *ExecutorPool) Execute(ctx context.Context, connectionName string, sqlText string, statementType string) (*ExecutionResult, error) {
	name, ex, err := p.executorByName(ctx, connectionName)
	if err != nil {
		return nil, err
	}

	result, err := ex.Execute(ctx, sqlText, statementType)
	if err != nil && p.isConnectionError(err) {
		p.markConnectionFailed(name, ex)
	}
	return result, p.connectionError(name, err)
}

// ResolveConnectionName validates that the provided name exists in config.
func (p *ExecutorPool) ResolveConnectionName(connectionName string) (string, error) {
	name := strings.TrimSpace(connectionName)
	if name == "" {
		return "", fmt.Errorf("unknown connection %q; use list_connections to see configured names", name)
	}

	p.mu.RLock()
	_, ok := p.executors[name]
	_, inFailed := p.failed[name]
	_, configured := p.dsns[name]
	p.mu.RUnlock()

	if ok || inFailed || configured {
		return name, nil
	}
	return "", fmt.Errorf("unknown connection %q; use list_connections to see configured names", name)
}

// ExecuteToCSVFile runs the SQL on the named connection and writes the result to a CSV file.
// filePath must be absolute. Returns rows written.
func (p *ExecutorPool) ExecuteToCSVFile(ctx context.Context, connectionName string, sqlText string, filePath string) (int64, error) {
	name, ex, err := p.executorByName(ctx, connectionName)
	if err != nil {
		return 0, err
	}
	n, err := ex.ExecuteToCSVFile(ctx, sqlText, filePath)
	if err != nil && p.isConnectionError(err) {
		p.markConnectionFailed(name, ex)
	}
	return n, p.connectionError(name, err)
}

// ExecuteToTextFile runs the SQL on the named connection and writes the result to a plain text file.
// filePath must be absolute. Returns rows written.
func (p *ExecutorPool) ExecuteToTextFile(ctx context.Context, connectionName string, sqlText string, filePath string) (int64, error) {
	name, ex, err := p.executorByName(ctx, connectionName)
	if err != nil {
		return 0, err
	}
	n, err := ex.ExecuteToTextFile(ctx, sqlText, filePath)
	if err != nil && p.isConnectionError(err) {
		p.markConnectionFailed(name, ex)
	}
	return n, p.connectionError(name, err)
}

// executorByName returns the resolved connection name and executor, or error if not found / unavailable.
func (p *ExecutorPool) executorByName(ctx context.Context, connectionName string) (resolvedName string, ex *Executor, err error) {
	name, err := p.ResolveConnectionName(connectionName)
	if err != nil {
		return "", nil, err
	}
	if err := p.recoverConnectionContext(ctx, name, true); err != nil {
		return "", nil, err
	}

	p.mu.RLock()
	exec, ok := p.executors[name]
	_, inFailed := p.failed[name]
	p.mu.RUnlock()
	if !ok {
		if inFailed {
			return "", nil, p.unavailableError(name)
		}
		return "", nil, fmt.Errorf("unknown connection %q; use list_connections to see configured names", name)
	}
	return name, exec, nil
}

// isConnectionError returns true if the error indicates a broken/dead connection
// (TNS, listener, network, etc.) so we can demote the connection to failed.
func (p *ExecutorPool) isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	// Common Oracle connection/TNS errors
	for _, sub := range []string{"ora-12541", "ora-12514", "ora-12154", "ora-12170", "ora-03113", "ora-01012", "ora-12560", "tns:", "no listener", "connection closed", "connection reset", "broken pipe", "i/o timeout", "driver: bad connection"} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// markConnectionFailed closes the old pool; the next operation recreates it.
func (p *ExecutorPool) markConnectionFailed(name string, ex *Executor) {
	p.mu.Lock()
	if p.executors == nil {
		p.mu.Unlock()
		return
	}
	if current, ok := p.executors[name]; !ok || current != ex {
		p.mu.Unlock()
		return
	}
	delete(p.executors, name)
	if dsn, ok := p.dsns[name]; ok {
		p.failed[name] = dsn
	}
	p.mu.Unlock()
	ex.Close()
	log.Printf("oracle-mcp: connection %q marked unavailable after execution error", name)
}

func (p *ExecutorPool) monitorSSH(name string) {
	defer p.wg.Done()
	delay := 2 * time.Second
	backoff := 2 * time.Second
	for {
		timer := time.NewTimer(delay)
		select {
		case <-p.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		p.recoverConnection(name)
		p.mu.RLock()
		_, ok := p.executors[name]
		p.mu.RUnlock()
		if ok {
			delay, backoff = 15*time.Second, 2*time.Second
		} else {
			delay = backoff
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
		}
	}
}

func logConnectionFailure(name, code, detail string) {
	log.Printf("oracle-mcp: connection=%q code=%s error=%q", name, code, detail)
}
