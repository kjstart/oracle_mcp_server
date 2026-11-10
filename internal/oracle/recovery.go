package oracle

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/alvin/oracle-mcp-server/internal/sshtunnel"
)

func (p *ExecutorPool) recoverConnection(name string) {
	_ = p.recoverConnectionContext(p.ctx, name, false)
}

// Demand operations wait for the per-connection recovery lock; background checks
// skip busy connections. No SQL is executed or replayed by this method.
func (p *ExecutorPool) recoverConnectionContext(parent context.Context, name string, wait bool) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	r := p.runtimes[name]
	if wait {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for !r.mu.TryLock() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	} else if !r.mu.TryLock() {
		return nil
	}
	defer r.mu.Unlock()
	if p.ctx.Err() != nil {
		return p.ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	r.checked = true
	p.mu.Unlock()
	p.mu.RLock()
	ex := p.executors[name]
	original := p.dsns[name]
	p.mu.RUnlock()
	connected := true
	if r.tunnel != nil {
		connected = r.tunnel.Check(ctx) == nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ex != nil {
		if connected {
			pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
			err := ex.TestConnection(pingCtx)
			pingCancel()
			if err == nil {
				return nil
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		p.markConnectionFailed(name, ex)
	}
	databaseDSN := original
	if r.ssh != nil {
		if _, err := tunnelDSN(original, "127.0.0.1:1"); err != nil {
			p.recordFailure(name, "SSH_DSN_UNSUPPORTED", err.Error())
			return p.unavailableError(name)
		}
		if r.tunnel == nil {
			tunnel, err := sshtunnel.New(*r.ssh, func(code, detail string) { p.recordFailure(name, code, detail) })
			if err != nil {
				p.recordFailure(name, "SSH_LISTEN_FAILED", err.Error())
				return p.unavailableError(name)
			}
			p.mu.Lock()
			r.tunnel = tunnel
			p.mu.Unlock()
		}
		if err := r.tunnel.Connect(ctx); err != nil {
			return p.unavailableError(name)
		}
		var err error
		databaseDSN, err = tunnelDSN(original, r.tunnel.Address())
		if err != nil {
			p.recordFailure(name, "SSH_DSN_UNSUPPORTED", err.Error())
			return p.unavailableError(name)
		}
	}
	ex, err := p.open(ctx, databaseDSN)
	if err != nil {
		if r.tunnel != nil {
			if _, code, _ := r.tunnel.Status(); code != "" {
				return p.unavailableError(name)
			}
		}
		p.recordFailure(name, "DATABASE_CONNECT_FAILED", safeDatabaseError(err))
		return p.unavailableError(name)
	}
	p.mu.Lock()
	if p.ctx.Err() != nil {
		p.mu.Unlock()
		ex.Close()
		return p.ctx.Err()
	}
	p.executors[name] = ex
	delete(p.failed, name)
	r.errorCode, r.lastError = "", ""
	p.mu.Unlock()
	return nil
}

// Logs intentionally omit DSNs and credentials. Audit logging is unrelated to connectivity.
func (p *ExecutorPool) recordFailure(name, code, detail string) {
	p.mu.Lock()
	r := p.runtimes[name]
	changed := r.errorCode != code || r.lastError != detail
	r.errorCode, r.lastError = code, detail
	p.mu.Unlock()
	if changed {
		logger := p.errorLog
		if logger != nil {
			logger.Printf("connection=%q code=%s error=%q", name, code, detail)
		}
		// stderr remains useful for MCP hosts; error.log is the persistent diagnostic log.
		logConnectionFailure(name, code, detail)
	}
}

var oracleErrorCode = regexp.MustCompile(`ORA-[0-9]+`)

func safeDatabaseError(err error) string {
	detail := "Database connection failed; check database listener, service name, credentials and network"
	if code := oracleErrorCode.FindString(err.Error()); code != "" {
		detail += " (" + code + ")"
	}
	return detail
}

func actionForError(code string) string {
	switch code {
	case "SSH_CONFIG_ERROR":
		return "Ask the user to check SSH key/password environment variables and known_hosts; SSH settings are plaintext in oracle.connection_ssh."
	case "SSH_DSN_UNSUPPORTED":
		return "Ask the user to configure a single TCP Easy Connect database DSN (host:port/service)."
	case "SSH_FORWARD_FAILED":
		return "Ask the user to check the database target address/port and TCP forwarding permission on the jump host."
	case "SSH_CONNECT_FAILED", "SSH_DISCONNECTED", "SSH_LISTEN_FAILED":
		return "Ask the user to check the jump host, SSH port, network, authentication and host key; the connection will be checked/reconnected before the next database operation."
	case "DATABASE_CONNECT_FAILED":
		return "Ask the user to check the Oracle listener, service name and database credentials; inspect ssh_connected separately."
	}
	return ""
}

func (p *ExecutorPool) unavailableError(name string) error {
	p.mu.RLock()
	r := p.runtimes[name]
	code, detail := r.errorCode, r.lastError
	if r.tunnel != nil {
		if _, c, d := r.tunnel.Status(); c != "" {
			code, detail = c, d
		}
	}
	p.mu.RUnlock()
	return &ConnectivityError{Connection: name, Code: code, Detail: detail, UserAction: actionForError(code)}
}

// ConnectivityError is available to MCP callers without parsing a driver error string.
type ConnectivityError struct {
	Connection string `json:"connection"`
	Code       string `json:"error_code"`
	Detail     string `json:"last_error"`
	UserAction string `json:"user_action"`
}

func (e *ConnectivityError) Error() string {
	return fmt.Sprintf("connection %q unavailable [%s]: %s. %s Call list_connections for current status", e.Connection, e.Code, e.Detail, e.UserAction)
}

func (p *ExecutorPool) connectionError(name string, err error) error {
	if err == nil {
		return nil
	}
	p.mu.RLock()
	r := p.runtimes[name]
	var code string
	if r.tunnel != nil {
		_, code, _ = r.tunnel.Status()
	}
	p.mu.RUnlock()
	networkFailure := p.isConnectionError(err)
	if networkFailure && code == "" {
		p.recordFailure(name, "DATABASE_CONNECT_FAILED", safeDatabaseError(err))
	}
	if strings.HasPrefix(code, "SSH_") || networkFailure {
		return fmt.Errorf("%w; %w", err, p.unavailableError(name))
	}
	return err
}
