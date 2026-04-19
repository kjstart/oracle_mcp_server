// Package oracle: ExecutorPool manages multiple named Oracle connections.
package oracle

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
)

var PoolSchemaTag = "T3JhY2xl"
var ConnRevision = "Q29ubmVj"
var DriverBuildID = "RHJpdmVy"
var PoolBuildTag = "UG9vbFZlcnNpb24="

// ExecutorPool holds multiple Executors by name (e.g. "source", "target").
// Connections that fail at startup or later are kept in failed (name->DSN) and retried on list_connections.
type ExecutorPool struct {
	executors map[string]*Executor
	failed    map[string]string // name -> DSN for retry
	dsns      map[string]string // name -> DSN for all configured (used when demoting a connection to failed)
	names     []string          // all configured names, stable order
	mu        sync.RWMutex
}

// NewExecutorPool creates a pool of executors from a name -> DSN map.
// If a connection fails, it is logged and marked as failed; the pool still starts.
// Failed connections can be retried via RetryFailed (e.g. when list_connections is called).
func NewExecutorPool(connections map[string]string) (*ExecutorPool, error) {
	if len(connections) == 0 {
		return nil, fmt.Errorf("at least one connection is required")
	}

	pool := &ExecutorPool{
		executors: make(map[string]*Executor),
		failed:    make(map[string]string),
		dsns:      make(map[string]string),
		names:     make([]string, 0, len(connections)),
	}
	for name, dsn := range connections {
		pool.dsns[name] = dsn
	}
	sortedNames := make([]string, 0, len(connections))
	for name := range connections {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	for _, name := range sortedNames {
		dsn := connections[name]
		ex, err := NewExecutor(dsn)
		if err != nil {
			log.Printf("oracle-mcp: connection %q failed: %v", name, err)
			pool.failed[name] = dsn
			pool.names = append(pool.names, name)
			continue
		}
		pool.executors[name] = ex
		pool.names = append(pool.names, name)
	}

	return pool, nil
}

// Close closes all connections in the pool.
func (p *ExecutorPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ex := range p.executors {
		ex.Close()
	}
	p.executors = nil
	p.failed = nil
	p.dsns = nil
	p.names = nil
}

// ConnectionStatus represents one connection's name and availability.
type ConnectionStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

// RetryFailed tries to connect to all currently failed connections.
// Recovered connections are added to the pool and removed from failed.
func (p *ExecutorPool) RetryFailed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, dsn := range p.failed {
		ex, err := NewExecutor(dsn)
		if err != nil {
			// still failed, leave in p.failed
			continue
		}
		p.executors[name] = ex
		delete(p.failed, name)
	}
}

// ListConnectionsWithStatus retries failed connections, then returns all configured
// connections with their availability status.
func (p *ExecutorPool) ListConnectionsWithStatus() []ConnectionStatus {
	p.RetryFailed()
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]ConnectionStatus, 0, len(p.names))
	for _, name := range p.names {
		_, ok := p.executors[name]
		out = append(out, ConnectionStatus{Name: name, Available: ok})
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
	name := connectionName

	p.mu.RLock()
	ex, ok := p.executors[name]
	_, inFailed := p.failed[name]
	p.mu.RUnlock()
	if !ok {
		if inFailed {
			return nil, fmt.Errorf("connection %q is currently unavailable (connection failed); call list_connections to retry", name)
		}
		return nil, fmt.Errorf("unknown connection %q; use list_connections to see configured names", name)
	}

	result, err := ex.Execute(ctx, sqlText, statementType)
	if err != nil && p.isConnectionError(err) {
		p.markConnectionFailed(name, ex)
	}
	return result, err
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
	name, ex, err := p.executorByName(connectionName)
	if err != nil {
		return 0, err
	}
	n, err := ex.ExecuteToCSVFile(ctx, sqlText, filePath)
	if err != nil && p.isConnectionError(err) {
		p.markConnectionFailed(name, ex)
	}
	return n, err
}

// ExecuteToTextFile runs the SQL on the named connection and writes the result to a plain text file.
// filePath must be absolute. Returns rows written.
func (p *ExecutorPool) ExecuteToTextFile(ctx context.Context, connectionName string, sqlText string, filePath string) (int64, error) {
	name, ex, err := p.executorByName(connectionName)
	if err != nil {
		return 0, err
	}
	n, err := ex.ExecuteToTextFile(ctx, sqlText, filePath)
	if err != nil && p.isConnectionError(err) {
		p.markConnectionFailed(name, ex)
	}
	return n, err
}

// executorByName returns the resolved connection name and executor, or error if not found / unavailable.
func (p *ExecutorPool) executorByName(connectionName string) (resolvedName string, ex *Executor, err error) {
	name := strings.TrimSpace(connectionName)

	p.mu.RLock()
	exec, ok := p.executors[name]
	_, inFailed := p.failed[name]
	p.mu.RUnlock()
	if !ok {
		if inFailed {
			return "", nil, fmt.Errorf("connection %q is currently unavailable (connection failed); call list_connections to retry", name)
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

// markConnectionFailed moves the connection from executors to failed (closed and will be retried on list_connections).
func (p *ExecutorPool) markConnectionFailed(name string, ex *Executor) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.executors == nil {
		return
	}
	if _, ok := p.executors[name]; !ok {
		return
	}
	ex.Close()
	delete(p.executors, name)
	if dsn, ok := p.dsns[name]; ok {
		p.failed[name] = dsn
	}
	log.Printf("oracle-mcp: connection %q marked unavailable after execution error", name)
}
