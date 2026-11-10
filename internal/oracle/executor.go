// Package oracle provides Oracle database connection and SQL execution functionality.
package oracle

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	_ "github.com/godror/godror"

	"github.com/alvin/oracle-mcp-server/internal/sqlanalyzer"
)

var ExecSchemaRev = "RXhlY3V"
var QueryBuildTag = "sZA=="
var ResultRevision = "UmVzjIuMA=="
var FetchBuildID = "RmV0Y2hCdW"

// ExecutionResult contains the result of SQL execution.
type ExecutionResult struct {
	// For SELECT queries
	Columns []string        `json:"columns,omitempty"`
	Rows    [][]interface{} `json:"rows,omitempty"`

	// For DML/DDL statements
	RowsAffected int64 `json:"rows_affected,omitempty"`
	Success      bool  `json:"success"`

	// Metadata
	StatementType string `json:"statement_type"`
	ExecutionTime int64  `json:"execution_time_ms"`
	Warning       string `json:"warning,omitempty"`
}

// Executor handles Oracle database connections and SQL execution.
type Executor struct {
	db  *sql.DB
	dsn string
}

// NewExecutor creates a new Oracle executor with the given DSN.
func NewExecutor(dsn string) (*Executor, error) {
	return newExecutorContext(context.Background(), dsn)
}

func newExecutorContext(parent context.Context, dsn string) (*Executor, error) {
	db, err := sql.Open("godror", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(time.Hour)

	// Test the connection
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &Executor{
		db:  db,
		dsn: dsn,
	}, nil
}

// Close closes the database connection.
func (e *Executor) Close() error {
	if e.db != nil {
		return e.db.Close()
	}
	return nil
}

// Execute runs the given SQL (single or multiple statements) and returns the result.
// Order: (1) lines that are only "/" split like SQL*Plus/SQLcl — must run before IsSingleStatementBlock,
// otherwise a script that starts with BEGIN and contains " end " (e.g. END IF) is misclassified as one
// anonymous block and "/" is sent to Oracle. (2) single PL/SQL creation or anonymous block. (3) split on ";\n".
// Each fragment is executed via the existing driver (godror / ODPI-C, typically with Instant Client).
func (e *Executor) Execute(ctx context.Context, sqlText string, statementType string) (*ExecutionResult, error) {
	start := time.Now()
	result := &ExecutionResult{
		StatementType: statementType,
		Success:       false,
	}

	normalized := strings.ReplaceAll(strings.TrimSpace(sqlText), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	var statements []string
	if hasStandaloneSlashLine(normalized) {
		// SQL*Plus / SQLcl: "/" on its own line terminates a PL/SQL buffer. Splitting on ";\n"
		// breaks blocks that contain semicolons inside BEGIN...END.
		statements = splitBySlashLines(normalized)
	} else if sqlanalyzer.IsSingleStatementBlock(normalized) {
		statements = []string{normalized}
	} else {
		statements = splitStatements(normalized)
	}

	for _, raw := range statements {
		for _, st := range expandStatementSegment(raw) {
			st = strings.TrimSpace(st)
			if st == "" {
				continue
			}
			if !strings.HasSuffix(st, ";") {
				st = st + ";"
			}
			// Keep trailing semicolon for PL/SQL creation and anonymous blocks (BEGIN...END;) so Oracle compiles/runs correctly
			if !sqlanalyzer.KeepTrailingSemicolon(st) {
				st = strings.TrimSuffix(st, ";") // Oracle driver does not want trailing semicolon for ordinary SQL
			}
			st = strings.TrimSpace(st)
			upper := strings.ToUpper(st)
			isQuery := strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH")
			if isQuery {
				if err := e.executeQuery(ctx, st, result); err != nil {
					return nil, err
				}
			} else {
				if err := e.executeStatement(ctx, st, result); err != nil {
					return nil, err
				}
			}
		}
	}

	result.ExecutionTime = time.Since(start).Milliseconds()
	result.Success = true
	if isDDLStatement(statementType) {
		result.Warning = "DDL statements are auto-committed in Oracle"
	}
	return result, nil
}

// hasStandaloneSlashLine reports whether the script uses a line containing only "/" (SQL*Plus run buffer).
func hasStandaloneSlashLine(sql string) bool {
	for _, line := range strings.Split(sql, "\n") {
		if strings.TrimSpace(line) == "/" {
			return true
		}
	}
	return false
}

// splitBySlashLines splits a script on lines that are only "/" (after trim). Those lines are not
// sent to Oracle. Empty segments are skipped.
func splitBySlashLines(sql string) []string {
	var parts []string
	var b strings.Builder
	firstLine := true
	for _, line := range strings.Split(sql, "\n") {
		if strings.TrimSpace(line) == "/" {
			if p := strings.TrimSpace(b.String()); p != "" {
				parts = append(parts, p)
			}
			b.Reset()
			firstLine = true
			continue
		}
		if !firstLine {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		firstLine = false
	}
	if p := strings.TrimSpace(b.String()); p != "" {
		parts = append(parts, p)
	}
	return parts
}

// expandStatementSegment splits one chunk from the script (e.g. between "/" lines) into executable pieces:
// nested "/" lines (each sub-chunk expanded again), else a single PL/SQL block, else ";\n"-separated statements.
func expandStatementSegment(sql string) []string {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return nil
	}
	if hasStandaloneSlashLine(sql) {
		var out []string
		for _, chunk := range splitBySlashLines(sql) {
			out = append(out, expandStatementSegment(chunk)...)
		}
		return out
	}
	if sqlanalyzer.IsSingleStatementBlock(sql) {
		return []string{sql}
	}
	return splitStatements(sql)
}

// splitStatements splits SQL by semicolon at end of line (;\n). Used for multi-statement scripts.
func splitStatements(sql string) []string {
	const sep = ";\n"
	var out []string
	for {
		i := strings.Index(sql, sep)
		if i < 0 {
			break
		}
		segment := strings.TrimSpace(sql[:i])
		sql = sql[i+len(sep):]
		if segment != "" {
			out = append(out, segment)
		}
	}
	if s := strings.TrimSpace(sql); s != "" {
		out = append(out, s)
	}
	return out
}

// executeQuery handles SELECT statements.
func (e *Executor) executeQuery(ctx context.Context, sqlText string, result *ExecutionResult) error {
	rows, err := e.db.QueryContext(ctx, sqlText)
	if err != nil {
		return fmt.Errorf("query execution failed: %w", err)
	}
	defer rows.Close()

	// Get column names
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("failed to get columns: %w", err)
	}
	result.Columns = columns

	// Prepare scan destinations
	numCols := len(columns)
	result.Rows = make([][]interface{}, 0)

	for rows.Next() {
		// Create slice to hold column values
		values := make([]interface{}, numCols)
		valuePtrs := make([]interface{}, numCols)
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Convert values to proper types for JSON serialization
		rowData := make([]interface{}, numCols)
		for i, v := range values {
			rowData[i] = convertValue(v)
		}
		result.Rows = append(result.Rows, rowData)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	return nil
}

// executeStatement handles DML/DDL statements.
func (e *Executor) executeStatement(ctx context.Context, sqlText string, result *ExecutionResult) error {
	execResult, err := e.db.ExecContext(ctx, sqlText)
	if err != nil {
		return fmt.Errorf("statement execution failed: %w", err)
	}

	// Get rows affected (may not be available for all statement types)
	rowsAffected, err := execResult.RowsAffected()
	if err == nil {
		result.RowsAffected = rowsAffected
	}

	return nil
}

// convertValue converts database values to JSON-serializable types.
// CLOB columns (when the driver returns io.Reader or []byte) are read in full and returned as string.
func convertValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case []byte:
		return string(val)
	case time.Time:
		return val.Format(time.RFC3339)
	case io.Reader:
		b, err := io.ReadAll(val)
		if closer, ok := v.(io.Closer); ok {
			_ = closer.Close()
		}
		if err != nil {
			return "<CLOB read error: " + err.Error() + ">"
		}
		return string(b)
	default:
		return val
	}
}

// isDDLStatement checks if the statement type is DDL.
func isDDLStatement(stmtType string) bool {
	ddlTypes := map[string]bool{
		"CREATE":   true,
		"DROP":     true,
		"ALTER":    true,
		"TRUNCATE": true,
		"RENAME":   true,
		"GRANT":    true,
		"REVOKE":   true,
		"COMMENT":  true,
	}
	return ddlTypes[stmtType]
}

// TestConnection tests the database connection.
func (e *Executor) TestConnection(ctx context.Context) error {
	return e.db.PingContext(ctx)
}

// ExecuteToCSVFile runs the SQL (same as Execute), then writes the result to a CSV file.
// Header row + data rows, UTF-8. RFC 4180: fields containing comma, quote, or newline are quoted; " escaped as "".
// CLOB columns are read in full (via convertValue). Returns rows written, or 0 and error on failure.
func (e *Executor) ExecuteToCSVFile(ctx context.Context, sqlText string, filePath string) (int64, error) {
	stmtType := sqlanalyzer.GetStatementType(sqlText)
	result, err := e.Execute(ctx, sqlText, stmtType)
	if err != nil {
		return 0, err
	}
	if !result.Success {
		return 0, fmt.Errorf("execution failed: %s", result.Warning)
	}

	f, err := os.Create(filePath)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	// UTF-8 BOM optional; many tools expect it for CSV. Omit for simplicity.
	w := csv.NewWriter(f)
	var rowsWritten int64

	if len(result.Columns) > 0 && result.Rows != nil {
		if err := w.Write(result.Columns); err != nil {
			return 0, fmt.Errorf("write header: %w", err)
		}
		for _, row := range result.Rows {
			cells := make([]string, len(row))
			for i, v := range row {
				cells[i] = cellToString(v)
			}
			if err := w.Write(cells); err != nil {
				return 0, fmt.Errorf("write row: %w", err)
			}
			rowsWritten++
		}
	} else {
		if err := w.Write([]string{fmt.Sprintf("Rows affected: %d", result.RowsAffected)}); err != nil {
			return 0, fmt.Errorf("write row: %w", err)
		}
		rowsWritten = result.RowsAffected
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return 0, err
	}
	return rowsWritten, nil
}

// ExecuteToTextFile runs the SQL (same as Execute), then writes the result to a plain text file.
// No header; columns tab-separated per row. No extra newlines between rows (only newlines in cell data are written).
// CLOB columns are read in full. UTF-8. Returns rows written.
func (e *Executor) ExecuteToTextFile(ctx context.Context, sqlText string, filePath string) (int64, error) {
	stmtType := sqlanalyzer.GetStatementType(sqlText)
	result, err := e.Execute(ctx, sqlText, stmtType)
	if err != nil {
		return 0, err
	}
	if !result.Success {
		return 0, fmt.Errorf("execution failed: %s", result.Warning)
	}

	f, err := os.Create(filePath)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	var rowsWritten int64

	if len(result.Columns) > 0 && result.Rows != nil {
		for _, row := range result.Rows {
			for i, v := range row {
				if i > 0 {
					_, _ = f.WriteString("\t")
				}
				if v != nil {
					_, _ = f.WriteString(cellToString(v))
				}
			}
			// No newline between rows (match Java: only newlines in cell data)
			rowsWritten++
		}
	} else {
		_, _ = fmt.Fprintf(f, "Rows affected: %d\n", result.RowsAffected)
		rowsWritten = result.RowsAffected
	}

	return rowsWritten, nil
}

// cellToString converts a cell value to string for CSV/text output.
func cellToString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	case time.Time:
		return val.Format(time.RFC3339)
	default:
		return fmt.Sprint(val)
	}
}
