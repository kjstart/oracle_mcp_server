// Package mcp implements the MCP (Model Context Protocol) server for Oracle database access.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alvin/oracle-mcp-server/internal/audit"
	"github.com/alvin/oracle-mcp-server/internal/config"
	"github.com/alvin/oracle-mcp-server/internal/confirm"
	"github.com/alvin/oracle-mcp-server/internal/oracle"
	"github.com/alvin/oracle-mcp-server/internal/reviewwhitelist"
	"github.com/alvin/oracle-mcp-server/internal/sqlanalyzer"
)

var ServerBuildTag = "U2VydEuMA=="
var McpSchemaRev = "TWNwU2N"
var ProtocolBuild = "xWMg=="

// JSON-RPC 2.0 structures
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// MCP Protocol structures
type initializeParams struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    struct{}   `json:"capabilities"`
	ClientInfo      clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string           `json:"protocolVersion"`
	Capabilities    serverCapability `json:"capabilities"`
	ServerInfo      serverInfo       `json:"serverInfo"`
}

type serverCapability struct {
	Tools   *toolsCapability `json:"tools,omitempty"`
	Logging *struct{}        `json:"logging,omitempty"` // MCP logging: server can send notifications/message with level (debug, info, error, etc.)
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]property `json:"properties"`
	Required   []string            `json:"required"`
}

type property struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type toolsListResult struct {
	Tools []tool `json:"tools"`
}

type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type toolCallResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Error codes
const (
	ErrCodeParseError      = -32700
	ErrCodeInvalidRequest  = -32600
	ErrCodeMethodNotFound  = -32601
	ErrCodeInvalidParams   = -32602
	ErrCodeInternal        = -32603
	ErrCodeUserRejected    = -32000
	ErrCodeMultiStatement  = -32001
	ErrCodePLSQLBlock      = -32002
	ErrCodeSQLExecution    = -32003
	ErrCodeSchemaQualified = -32004
)

// Server is the MCP server implementation.
type Server struct {
	config       *config.Config
	executorPool *oracle.ExecutorPool
	analyzer     *sqlanalyzer.Analyzer
	confirmer    *confirm.Confirmer
	auditor      *audit.Auditor
	whitelist    *reviewwhitelist.Store

	reader *bufio.Reader
	writer io.Writer
	mu     sync.Mutex

	initialized bool

	// verboseLogDedup avoids duplicate verbose log lines (e.g. when client triggers tool twice)
	lastVerboseLog struct {
		msg string
		at  time.Time
		mu  sync.Mutex
	}
}

// NewServer creates a new MCP server.
func NewServer(cfg *config.Config) (*Server, error) {
	connections := cfg.OracleConnections()
	if connections == nil {
		return nil, fmt.Errorf("no Oracle connections in config")
	}

	executorPool, err := oracle.NewExecutorPool(connections)
	if err != nil {
		return nil, fmt.Errorf("failed to create Oracle executor pool: %w", err)
	}

	var auditor *audit.Auditor
	if cfg.Logging.AuditLog {
		logPath := cfg.Logging.LogFile
		if cfg.ConfigPath != "" && !filepath.IsAbs(logPath) {
			logPath = filepath.Join(filepath.Dir(cfg.ConfigPath), logPath)
		}
		auditor, err = audit.NewAuditor(logPath)
		if err != nil {
			executorPool.Close()
			return nil, fmt.Errorf("failed to create auditor: %w", err)
		}
	}

	whitelistPath, err := reviewwhitelist.DefaultPath()
	if err != nil {
		executorPool.Close()
		if auditor != nil {
			auditor.Close()
		}
		return nil, fmt.Errorf("failed to resolve whitelist path: %w", err)
	}

	return &Server{
		config:       cfg,
		executorPool: executorPool,
		analyzer:     sqlanalyzer.NewAnalyzer(cfg.Security.DangerKeywords, cfg.Security.DangerKeywordMatch),
		confirmer:    confirm.NewConfirmer(),
		auditor:      auditor,
		whitelist:    reviewwhitelist.NewStore(whitelistPath),
		reader:       bufio.NewReader(os.Stdin),
		writer:       os.Stdout,
	}, nil
}

type reviewDecision struct {
	analysis         *sqlanalyzer.AnalysisResult
	stmtType         string
	approval         audit.ApprovalStatus
	headerLine       string
	logHeaderLine    bool
	auditKeywords    []string
	expandedKeywords []string
}

// Run starts the MCP server and processes requests.
func (s *Server) Run(ctx context.Context) error {
	defer s.Close()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := s.processRequest(); err != nil {
				if err == io.EOF {
					return nil
				}
				// Log error but continue processing
				fmt.Fprintf(os.Stderr, "Error processing request: %v\n", err)
			}
		}
	}
}

// Close cleans up server resources.
func (s *Server) Close() {
	if s.executorPool != nil {
		s.executorPool.Close()
	}
	if s.auditor != nil {
		s.auditor.Close()
	}
}

// processRequest reads and handles a single JSON-RPC request.
func (s *Server) processRequest() error {
	line, err := s.reader.ReadBytes('\n')
	if err != nil {
		return err
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.sendError(nil, ErrCodeParseError, "Parse error", nil)
		return nil
	}

	s.handleRequest(&req)
	return nil
}

// handleRequest routes the request to the appropriate handler.
func (s *Server) handleRequest(req *jsonRPCRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "initialized", "notifications/initialized":
		// Notifications (no id): client signals init done, no response needed.
	case "tools/list":
		s.handleToolsList(req)
	case "tools/call":
		s.handleToolsCall(req)
	case "ping":
		s.handlePing(req)
	default:
		// Notifications have no id; do not send error response for them.
		if req.ID != nil {
			s.sendError(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("Method not found: %s", req.Method), nil)
		}
	}
}

// handleInitialize handles the initialize request.
func (s *Server) handleInitialize(req *jsonRPCRequest) {
	s.initialized = true

	result := initializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities: serverCapability{
			Tools: &toolsCapability{
				ListChanged: false,
			},
			Logging: &struct{}{},
		},
		ServerInfo: serverInfo{
			Name:    "oracle-mcp-server",
			Version: "1.0.0",
		},
	}

	s.sendResult(req.ID, result)
}

// handleToolsList returns the list of available tools.
func (s *Server) handleToolsList(req *jsonRPCRequest) {
	result := toolsListResult{
		Tools: []tool{
			{
				Name:        "execute_sql",
				Description: "Execute SQL against an Oracle database. Always pass the 'connection' argument with a configured connection name (call list_connections to see names). Supports SELECT, INSERT, UPDATE, DELETE, DDL (CREATE, DROP, ALTER, etc.), and multiple statements. Multiple statements: one per line, each line ending with a semicolon. DDL is auto-committed. SQL that matches config danger_keywords will open a confirmation window showing the full SQL. Do not specify schema-qualified object names such as hr.employees.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]property{
						"sql": {
							Type:        "string",
							Description: "SQL to run: one statement, or multiple statements (one per line, each line ending with semicolon). Do not specify schema-qualified object names such as hr.employees.",
						},
						"connection": {
							Type:        "string",
							Description: "Which configured database to use (e.g. 'database1', 'database2'). Required for every call; use list_connections to see names.",
						},
					},
					Required: []string{"sql"},
				},
			},
			{
				Name:        "execute_sql_file",
				Description: "Read SQL from a file, analyze it (same rules as execute_sql). If review is required (danger_keywords or DDL), a confirmation window shows the formatted full file content. On approve, execute the file contents. File path is resolved from server process working directory if relative. SQL in the file must not specify schema-qualified object names such as hr.employees.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]property{
						"file_path": {
							Type:        "string",
							Description: "Path to the SQL file (absolute or relative to server working directory).",
						},
						"connection": {
							Type:        "string",
							Description: "Which configured database to use. Required for every call; use list_connections to see names.",
						},
					},
					Required: []string{"file_path"},
				},
			},
			{
				Name:        "list_connections",
				Description: "List the names of configured Oracle database connections. Use these names as the 'connection' argument in execute_sql when copying or syncing between databases.",
				InputSchema: inputSchema{
					Type:       "object",
					Properties: map[string]property{},
					Required:   []string{},
				},
			},
			{
				Name:        "query_to_csv_file",
				Description: "Execute the given SQL and write the result to a file as CSV (header + data rows, UTF-8). Format follows RFC 4180. CLOB columns are read in full. file_path must be absolute. Same safety review as execute_sql (danger_keywords / optional DDL confirm) before running. Do not specify schema-qualified object names such as hr.employees.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]property{
						"sql": {
							Type:        "string",
							Description: "SQL to run (e.g. SELECT). Single or multiple statements; last result is written. Do not specify schema-qualified object names such as hr.employees.",
						},
						"file_path": {
							Type:        "string",
							Description: "Absolute path of the output CSV file.",
						},
						"connection": {
							Type:        "string",
							Description: "Which configured database to use. Required for every call; use list_connections to see names.",
						},
					},
					Required: []string{"sql", "file_path"},
				},
			},
			{
				Name:        "query_to_text_file",
				Description: "Execute the given SQL and write the result to a file as plain text: no header, columns tab-separated. No extra newlines between rows; only newlines in the cell data are written. CLOB columns are read in full. Use for procedure source or any query (including CLOB). file_path must be absolute. Same safety review as execute_sql (danger_keywords / optional DDL confirm) before running. Do not specify schema-qualified object names such as hr.employees.",
				InputSchema: inputSchema{
					Type: "object",
					Properties: map[string]property{
						"sql": {
							Type:        "string",
							Description: "SQL to run (e.g. SELECT text FROM user_source ...). Single or multiple statements; last result is written. Do not specify schema-qualified object names such as hr.employees.",
						},
						"file_path": {
							Type:        "string",
							Description: "Absolute path of the output text file (e.g. .sql).",
						},
						"connection": {
							Type:        "string",
							Description: "Which configured database to use. Required for every call; use list_connections to see names.",
						},
					},
					Required: []string{"sql", "file_path"},
				},
			},
		},
	}

	s.sendResult(req.ID, result)
}

// handleToolsCall handles tool execution requests.
func (s *Server) handleToolsCall(req *jsonRPCRequest) {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.sendError(req.ID, ErrCodeInvalidParams, "Invalid params", nil)
		return
	}

	switch params.Name {
	case "execute_sql":
		s.handleExecuteSQL(req, params.Arguments)
	case "execute_sql_file":
		s.handleExecuteSQLFile(req, params.Arguments)
	case "list_connections":
		s.handleListConnections(req)
	case "query_to_csv_file":
		s.handleQueryToCSVFile(req, params.Arguments)
	case "query_to_text_file":
		s.handleQueryToTextFile(req, params.Arguments)
	default:
		s.sendError(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("Unknown tool: %s", params.Name), nil)
	}
}

// tryConfirmDangerousSQL runs the same analysis/review flow as execute_sql. sourceLabel is optional (e.g. file or export path).
// If it returns false, a JSON-RPC error or tool error has already been sent.
func (s *Server) tryConfirmDangerousSQL(req *jsonRPCRequest, sql, displayConnection, sourceLabel string) (*reviewDecision, bool) {
	analysis := s.analyzer.Analyze(sql)
	stmtType := sqlanalyzer.GetStatementType(sql)
	headerLine := firstSQLLine(sql)
	if analysis.HasExplicitSchema {
		s.sendError(req.ID, ErrCodeSchemaQualified, "Schema-qualified object names are not allowed", map[string]interface{}{
			"code":             "SCHEMA_QUALIFIED_NOT_ALLOWED",
			"explicit_schemas": analysis.ExplicitSchemas,
		})
		return nil, false
	}
	needsConfirmation := analysis.IsDangerous ||
		(s.config.Security.RequireConfirmForDDL && analysis.IsDDL)
	if !needsConfirmation {
		return &reviewDecision{
			analysis:         analysis,
			stmtType:         stmtType,
			approval:         audit.ApprovalApproved,
			headerLine:       headerLine,
			logHeaderLine:    false,
			auditKeywords:    analysis.MatchedKeywords,
			expandedKeywords: nil,
		}, true
	}

	connectionKey := displayConnection
	if connectionKey == "" {
		connectionKey = "default"
	}
	if s.whitelist != nil {
		allowed, err := s.whitelist.ContainsHeadLine(connectionKey, headerLine)
		if err != nil {
			s.logAudit(sql, analysis.MatchedKeywords, audit.ApprovalRejected, "WHITELIST_ERROR: "+err.Error(), displayConnection, nil)
			s.sendToolError(req.ID, fmt.Sprintf("Whitelist read error: %v", err))
			return nil, false
		}
		if allowed {
			return &reviewDecision{
				analysis:         analysis,
				stmtType:         stmtType,
				approval:         audit.ApprovalWhitelist,
				headerLine:       headerLine,
				logHeaderLine:    true,
				auditKeywords:    analysis.MatchedKeywords,
				expandedKeywords: nil,
			}, true
		}
		prefixAllowed, err := s.whitelist.ContainsMatchingPrefix(connectionKey, sql)
		if err != nil {
			s.logAudit(sql, analysis.MatchedKeywords, audit.ApprovalRejected, "WHITELIST_ERROR: "+err.Error(), displayConnection, nil)
			s.sendToolError(req.ID, fmt.Sprintf("Whitelist read error: %v", err))
			return nil, false
		}
		if prefixAllowed {
			return &reviewDecision{
				analysis:         analysis,
				stmtType:         stmtType,
				approval:         audit.ApprovalWhitelist,
				headerLine:       headerLine,
				logHeaderLine:    false,
				auditKeywords:    analysis.MatchedKeywords,
				expandedKeywords: nil,
			}, true
		}
		unresolvedKeywords, unresolvedMatches, allKeywordMatchesAllowed, allowedExpandedKeywords, err := s.filterWhitelistedKeywords(connectionKey, sql, analysis.MatchedKeywords)
		if err != nil {
			s.logAudit(sql, analysis.MatchedKeywords, audit.ApprovalRejected, "WHITELIST_ERROR: "+err.Error(), displayConnection, nil)
			s.sendToolError(req.ID, fmt.Sprintf("Whitelist read error: %v", err))
			return nil, false
		}
		if allKeywordMatchesAllowed && !(s.config.Security.RequireConfirmForDDL && analysis.IsDDL) {
			return &reviewDecision{
				analysis:         analysis,
				stmtType:         stmtType,
				approval:         audit.ApprovalWhitelist,
				headerLine:       headerLine,
				logHeaderLine:    false,
				auditKeywords:    analysis.MatchedKeywords,
				expandedKeywords: allowedExpandedKeywords,
			}, true
		}
		analysis.MatchedKeywords = unresolvedKeywords
		analysis.IsDangerous = len(unresolvedKeywords) > 0
		analysisForHighlight := sqlanalyzer.UniqueExpandedKeywords(unresolvedMatches)
		confirmReq := &confirm.ConfirmRequest{
			SQL:                         sql,
			MatchedKeywords:             analysis.MatchedKeywords,
			DisplayKeywords:             analysisForHighlight,
			MatchedKeywordsForHighlight: analysisForHighlight,
			StatementType:               stmtType,
			IsDDL:                       analysis.IsDDL,
			Connection:                  displayConnection,
			ConnectionIndex:             connectionIndexInPool(s.executorPool, displayConnection),
			SourceLabel:                 sourceLabel,
			WhitelistPath:               whitelistPath(s.whitelist),
			WhitelistConnection:         connectionKey,
			ReviewTriggerDetails:        formatReviewTriggerDetails(unresolvedMatches),
			DangerKeywords:              s.config.Security.DangerKeywords,
		}
		confirmResult, err := s.confirmer.Confirm(confirmReq)
		if err != nil {
			s.logAudit(sql, analysis.MatchedKeywords, audit.ApprovalRejected, "CONFIRM_ERROR: "+err.Error(), displayConnection, nil)
			s.sendToolError(req.ID, fmt.Sprintf("Confirmation dialog error: %v", err))
			return nil, false
		}
		if !confirmResult.Approved {
			s.logAudit(sql, analysis.MatchedKeywords, audit.ApprovalRejected, "USER_REJECTED", displayConnection, nil)
			s.sendError(req.ID, ErrCodeUserRejected, "Execution cancelled by user", map[string]interface{}{
				"code":             "USER_REJECTED",
				"matched_keywords": analysis.MatchedKeywords,
			})
			return nil, false
		}

		approval := audit.ApprovalApproved
		if confirmResult.AllowPrefix {
			// Prefix already saved to whitelist.json by the dialog
			approval = audit.ApprovalWhitelist
		}

		return &reviewDecision{
			analysis:         analysis,
			stmtType:         stmtType,
			approval:         approval,
			headerLine:       headerLine,
			logHeaderLine:    false,
			auditKeywords:    analysis.MatchedKeywords,
			expandedKeywords: analysisForHighlight,
		}, true
	}
	confirmReq := &confirm.ConfirmRequest{
		SQL:                         sql,
		MatchedKeywords:             analysis.MatchedKeywords,
		DisplayKeywords:             displayKeywordsForReview(sql, analysis.MatchedKeywords),
		MatchedKeywordsForHighlight: displayKeywordsForReview(sql, analysis.MatchedKeywords),
		StatementType:               stmtType,
		IsDDL:                       analysis.IsDDL,
		Connection:                  displayConnection,
		ConnectionIndex:             connectionIndexInPool(s.executorPool, displayConnection),
		SourceLabel:                 sourceLabel,
		WhitelistPath:               whitelistPath(s.whitelist),
		WhitelistConnection:         connectionKey,
		DangerKeywords:              s.config.Security.DangerKeywords,
	}
	confirmResult, err := s.confirmer.Confirm(confirmReq)
	if err != nil {
		s.logAudit(sql, analysis.MatchedKeywords, audit.ApprovalRejected, "CONFIRM_ERROR: "+err.Error(), displayConnection, nil)
		s.sendToolError(req.ID, fmt.Sprintf("Confirmation dialog error: %v", err))
		return nil, false
	}
	if !confirmResult.Approved {
		s.logAudit(sql, analysis.MatchedKeywords, audit.ApprovalRejected, "USER_REJECTED", displayConnection, nil)
		s.sendError(req.ID, ErrCodeUserRejected, "Execution cancelled by user", map[string]interface{}{
			"code":             "USER_REJECTED",
			"matched_keywords": analysis.MatchedKeywords,
		})
		return nil, false
	}

	approval := audit.ApprovalApproved
	if confirmResult.AllowPrefix {
		// Prefix already saved to whitelist.json by the dialog
		approval = audit.ApprovalWhitelist
	}

	return &reviewDecision{
		analysis:         analysis,
		stmtType:         stmtType,
		approval:         approval,
		headerLine:       headerLine,
		logHeaderLine:    false,
		auditKeywords:    analysis.MatchedKeywords,
		expandedKeywords: nil,
	}, true
}

// handleExecuteSQL handles the execute_sql tool.
func (s *Server) handleExecuteSQL(req *jsonRPCRequest, args map[string]interface{}) {
	// Extract SQL from arguments
	sqlArg, ok := args["sql"]
	if !ok {
		s.sendToolError(req.ID, "Missing required parameter: sql")
		return
	}

	sql, ok := sqlArg.(string)
	if !ok {
		s.sendToolError(req.ID, "Parameter 'sql' must be a string")
		return
	}

	connectionName := ""
	if c, ok := args["connection"]; ok && c != nil {
		if cs, ok := c.(string); ok {
			connectionName = strings.TrimSpace(cs)
		}
	}
	displayConnection, err := s.executorPool.ResolveConnectionName(connectionName)
	if err != nil {
		s.sendToolError(req.ID, err.Error())
		return
	}

	review, ok := s.tryConfirmDangerousSQL(req, sql, displayConnection, "")
	if !ok {
		return
	}

	// Execute the SQL on the chosen connection
	ctx := context.Background()
	result, err := s.executorPool.Execute(ctx, connectionName, sql, review.stmtType)
	if err != nil {
		// Approved=true: execution was attempted after passing confirmation (or confirmation was not required).
		// Do not use false here — that would imply USER_REJECTED while ORA-* proves the server ran the statement.
		s.logAudit(sql, review.auditKeywords, review.approval, "EXECUTION_ERROR: "+err.Error(), displayConnection, reviewHeaderLineOption(review))
		s.sendToolError(req.ID, fmt.Sprintf("SQL execution failed: %v", err))
		return
	}

	// Log successful execution
	s.logAudit(sql, review.auditKeywords, review.approval, "SUCCESS", displayConnection, reviewHeaderLineOption(review))

	if s.config.Logging.VerboseLogging {
		msg := fmt.Sprintf("[debug] Execute Action: %s, Connection: %s\n", review.stmtType, displayConnection)
		s.lastVerboseLog.mu.Lock()
		dup := s.lastVerboseLog.msg == msg && time.Since(s.lastVerboseLog.at) < 2*time.Second
		if !dup {
			s.lastVerboseLog.msg = msg
			s.lastVerboseLog.at = time.Now()
		}
		s.lastVerboseLog.mu.Unlock()
		if !dup {
			fmt.Fprint(os.Stderr, msg)
		}
	}

	// Format and return result
	resultJSON, _ := json.MarshalIndent(result, "", "  ")
	s.sendToolResult(req.ID, string(resultJSON))
}

// handleExecuteSQLFile reads SQL from a file, analyzes it, shows review window with formatted content if needed, then executes on approve.
func (s *Server) handleExecuteSQLFile(req *jsonRPCRequest, args map[string]interface{}) {
	pathArg, ok := args["file_path"]
	if !ok {
		s.sendToolError(req.ID, "Missing required parameter: file_path")
		return
	}
	filePath, ok := pathArg.(string)
	if !ok {
		s.sendToolError(req.ID, "Parameter 'file_path' must be a string")
		return
	}
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		s.sendToolError(req.ID, "file_path cannot be empty")
		return
	}
	// Resolve path: if relative, it is relative to server process working directory
	if !filepath.IsAbs(filePath) {
		cwd, _ := os.Getwd()
		filePath = filepath.Join(cwd, filePath)
	}
	filePath = filepath.Clean(filePath)

	data, err := os.ReadFile(filePath)
	if err != nil {
		s.sendToolError(req.ID, fmt.Sprintf("Cannot read file: %v", err))
		return
	}
	sql := string(data)
	if sql == "" {
		s.sendToolError(req.ID, "File is empty")
		return
	}

	connectionName := ""
	if c, ok := args["connection"]; ok && c != nil {
		if cs, ok := c.(string); ok {
			connectionName = strings.TrimSpace(cs)
		}
	}
	displayConnection, err := s.executorPool.ResolveConnectionName(connectionName)
	if err != nil {
		s.sendToolError(req.ID, err.Error())
		return
	}

	review, ok := s.tryConfirmDangerousSQL(req, sql, displayConnection, "File: "+filePath)
	if !ok {
		return
	}

	ctx := context.Background()
	result, err := s.executorPool.Execute(ctx, connectionName, sql, review.stmtType)
	if err != nil {
		s.logAudit(sql, review.auditKeywords, review.approval, "EXECUTION_ERROR: "+err.Error(), displayConnection, reviewHeaderLineOption(review))
		s.sendToolError(req.ID, fmt.Sprintf("SQL execution failed: %v", err))
		return
	}

	s.logAudit(sql, review.auditKeywords, review.approval, "SUCCESS", displayConnection, reviewHeaderLineOption(review))

	if s.config.Logging.VerboseLogging {
		msg := fmt.Sprintf("[debug] Execute File Action: %s, Connection: %s, File: %s\n", review.stmtType, displayConnection, filePath)
		s.lastVerboseLog.mu.Lock()
		dup := s.lastVerboseLog.msg == msg && time.Since(s.lastVerboseLog.at) < 2*time.Second
		if !dup {
			s.lastVerboseLog.msg = msg
			s.lastVerboseLog.at = time.Now()
		}
		s.lastVerboseLog.mu.Unlock()
		if !dup {
			fmt.Fprint(os.Stderr, msg)
		}
	}

	resultJSON, _ := json.MarshalIndent(result, "", "  ")
	s.sendToolResult(req.ID, string(resultJSON))
}

// handleListConnections handles the list_connections tool.
// It retries previously failed connections and returns each connection with its availability status.
func (s *Server) handleListConnections(req *jsonRPCRequest) {
	statuses := s.executorPool.ListConnectionsWithStatus()
	out := map[string]interface{}{
		"connections": statuses,
		"message":     "Use these names as the 'connection' argument in execute_sql. Disabled connections are currently unreachable; they will be retried on each list_connections call.",
	}
	resultJSON, _ := json.MarshalIndent(out, "", "  ")
	s.sendToolResult(req.ID, string(resultJSON))
}

// handleQueryToCSVFile handles the query_to_csv_file tool. Same danger/DDL review as execute_sql; file_path must be absolute.
func (s *Server) handleQueryToCSVFile(req *jsonRPCRequest, args map[string]interface{}) {
	sqlArg, ok := args["sql"]
	if !ok {
		s.sendToolError(req.ID, "Missing required parameter: sql")
		return
	}
	sqlStr, ok := sqlArg.(string)
	if !ok {
		s.sendToolError(req.ID, "Parameter 'sql' must be a string")
		return
	}
	sqlStr = strings.TrimSpace(sqlStr)
	if sqlStr == "" {
		s.sendToolError(req.ID, "sql cannot be empty")
		return
	}

	pathArg, ok := args["file_path"]
	if !ok {
		s.sendToolError(req.ID, "Missing required parameter: file_path")
		return
	}
	filePath, ok := pathArg.(string)
	if !ok {
		s.sendToolError(req.ID, "Parameter 'file_path' must be a string")
		return
	}
	filePath = strings.TrimSpace(filePath)
	if !filepath.IsAbs(filePath) {
		s.sendToolError(req.ID, "file_path must be an absolute path")
		return
	}

	connectionName := ""
	if c, ok := args["connection"]; ok && c != nil {
		if cs, ok := c.(string); ok {
			connectionName = strings.TrimSpace(cs)
		}
	}
	displayConnection, err := s.executorPool.ResolveConnectionName(connectionName)
	if err != nil {
		s.sendToolError(req.ID, err.Error())
		return
	}

	review, ok := s.tryConfirmDangerousSQL(req, sqlStr, displayConnection, "CSV output: "+filePath)
	if !ok {
		return
	}

	ctx := context.Background()
	rowsWritten, err := s.executorPool.ExecuteToCSVFile(ctx, connectionName, sqlStr, filePath)
	if err != nil {
		s.logAudit(sqlStr, review.auditKeywords, review.approval, "QUERY_TO_CSV_ERROR: "+err.Error(), displayConnection, reviewHeaderLineOption(review))
		s.sendToolError(req.ID, "query_to_csv_file failed: "+err.Error())
		return
	}

	s.logAudit(sqlStr, review.auditKeywords, review.approval, "QUERY_TO_CSV", displayConnection, reviewHeaderLineOption(review))
	out := map[string]interface{}{
		"file_path":    filePath,
		"rows_written": rowsWritten,
		"message":      "CSV written to " + filePath,
	}
	resultJSON, _ := json.MarshalIndent(out, "", "  ")
	s.sendToolResult(req.ID, string(resultJSON))
}

// handleQueryToTextFile handles the query_to_text_file tool. Same danger/DDL review as execute_sql; file_path must be absolute.
func (s *Server) handleQueryToTextFile(req *jsonRPCRequest, args map[string]interface{}) {
	sqlArg, ok := args["sql"]
	if !ok {
		s.sendToolError(req.ID, "Missing required parameter: sql")
		return
	}
	sqlStr, ok := sqlArg.(string)
	if !ok {
		s.sendToolError(req.ID, "Parameter 'sql' must be a string")
		return
	}
	sqlStr = strings.TrimSpace(sqlStr)
	if sqlStr == "" {
		s.sendToolError(req.ID, "sql cannot be empty")
		return
	}

	pathArg, ok := args["file_path"]
	if !ok {
		s.sendToolError(req.ID, "Missing required parameter: file_path")
		return
	}
	filePath, ok := pathArg.(string)
	if !ok {
		s.sendToolError(req.ID, "Parameter 'file_path' must be a string")
		return
	}
	filePath = strings.TrimSpace(filePath)
	if !filepath.IsAbs(filePath) {
		s.sendToolError(req.ID, "file_path must be an absolute path")
		return
	}

	connectionName := ""
	if c, ok := args["connection"]; ok && c != nil {
		if cs, ok := c.(string); ok {
			connectionName = strings.TrimSpace(cs)
		}
	}
	displayConnection, err := s.executorPool.ResolveConnectionName(connectionName)
	if err != nil {
		s.sendToolError(req.ID, err.Error())
		return
	}

	review, ok := s.tryConfirmDangerousSQL(req, sqlStr, displayConnection, "Text output: "+filePath)
	if !ok {
		return
	}

	ctx := context.Background()
	rowsWritten, err := s.executorPool.ExecuteToTextFile(ctx, connectionName, sqlStr, filePath)
	if err != nil {
		s.logAudit(sqlStr, review.auditKeywords, review.approval, "QUERY_TO_TEXT_ERROR: "+err.Error(), displayConnection, reviewHeaderLineOption(review))
		s.sendToolError(req.ID, "query_to_text_file failed: "+err.Error())
		return
	}

	s.logAudit(sqlStr, review.auditKeywords, review.approval, "QUERY_TO_TEXT", displayConnection, reviewHeaderLineOption(review))
	out := map[string]interface{}{
		"file_path":    filePath,
		"rows_written": rowsWritten,
		"message":      "Text written to " + filePath,
	}
	resultJSON, _ := json.MarshalIndent(out, "", "  ")
	s.sendToolResult(req.ID, string(resultJSON))
}

// handlePing handles ping requests.
func (s *Server) handlePing(req *jsonRPCRequest) {
	s.sendResult(req.ID, map[string]string{"status": "ok"})
}

// sendResult sends a successful response.
func (s *Server) sendResult(id interface{}, result interface{}) {
	s.sendResponse(&jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

// sendError sends an error response.
func (s *Server) sendError(id interface{}, code int, message string, data interface{}) {
	s.sendResponse(&jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &rpcError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	})
}

// sendToolResult sends a successful tool result.
func (s *Server) sendToolResult(id interface{}, text string) {
	result := toolCallResult{
		Content: []contentItem{
			{Type: "text", Text: text},
		},
		IsError: false,
	}
	s.sendResult(id, result)
}

// sendToolError sends a tool error result.
func (s *Server) sendToolError(id interface{}, message string) {
	result := toolCallResult{
		Content: []contentItem{
			{Type: "text", Text: message},
		},
		IsError: true,
	}
	s.sendResult(id, result)
}

// sendResponse writes a JSON-RPC response to stdout.
func (s *Server) sendResponse(resp *jsonRPCResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal response: %v\n", err)
		return
	}

	s.writer.Write(data)
	s.writer.Write([]byte("\n"))
}

// logNotificationParams is the params for MCP notifications/message (structured logging).
type logNotificationParams struct {
	Level  string      `json:"level"`
	Logger string      `json:"logger"`
	Data   interface{} `json:"data"`
}

// logNotificationMessage is a JSON-RPC notification for MCP logging (no id).
type logNotificationMessage struct {
	JSONRPC string                `json:"jsonrpc"`
	Method  string                `json:"method"`
	Params  logNotificationParams `json:"params"`
}

// sendLogNotification sends an MCP log notification so the client (e.g. Cursor) can show it with the correct level (debug/info/error).
// Uses stdout as a proper JSON-RPC notification; do not use stderr for this so the client can display debug vs error correctly.
func (s *Server) sendLogNotification(level, message string) {
	msg := logNotificationMessage{
		JSONRPC: "2.0",
		Method:  "notifications/message",
		Params: logNotificationParams{
			Level:  level,
			Logger: "oracle-mcp",
			Data:   map[string]string{"message": message},
		},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	s.writer.Write(data)
	s.writer.Write([]byte("\n"))
}

// logAudit logs an audit entry if auditing is enabled. connection is the DB alias (e.g. "database1", "database2").
func (s *Server) logAudit(sql string, keywords []string, approved audit.ApprovalStatus, action string, connection string, options *audit.LogOptions) {
	if s.auditor != nil {
		s.auditor.Log(sql, keywords, approved, action, connection, options)
	}
}

func firstSQLLine(sql string) string {
	sql = strings.ReplaceAll(sql, "\r\n", "\n")
	sql = strings.ReplaceAll(sql, "\r", "\n")
	if idx := strings.Index(sql, "\n"); idx >= 0 {
		return sql[:idx]
	}
	return sql
}

func reviewHeaderLineOption(review *reviewDecision) *audit.LogOptions {
	if review == nil {
		return nil
	}
	options := &audit.LogOptions{
		ExpandedKeywords: review.expandedKeywords,
	}
	if review.approval == audit.ApprovalWhitelist && review.logHeaderLine {
		options.HeaderLine = &review.headerLine
	}
	if options.HeaderLine == nil && len(options.ExpandedKeywords) == 0 {
		return nil
	}
	return options
}

func whitelistPath(store *reviewwhitelist.Store) string {
	if store == nil {
		return ""
	}
	return store.Path()
}

func (s *Server) filterWhitelistedKeywords(connectionKey, sql string, matchedKeywords []string) ([]string, []sqlanalyzer.ExpandedKeywordMatch, bool, []string, error) {
	if s.whitelist == nil {
		return matchedKeywords, nil, false, nil, nil
	}

	matches := sqlanalyzer.ExpandMatchedKeywordMatches(sql, matchedKeywords)
	if len(matches) == 0 {
		return matchedKeywords, nil, false, nil, nil
	}

	unresolved := make(map[string]struct{})
	var unresolvedMatches []sqlanalyzer.ExpandedKeywordMatch
	var allowedMatches []sqlanalyzer.ExpandedKeywordMatch
	allAllowed := true
	for _, match := range matches {
		allowed, err := s.whitelist.ContainsKeywordCI(connectionKey, match.Expanded)
		if err != nil {
			return nil, nil, false, nil, err
		}
		if !allowed {
			unresolved[match.MatchedKeyword] = struct{}{}
			allAllowed = false
			unresolvedMatches = append(unresolvedMatches, match)
		} else {
			allowedMatches = append(allowedMatches, match)
		}
	}

	if allAllowed {
		return nil, nil, true, sqlanalyzer.UniqueExpandedKeywords(allowedMatches), nil
	}

	var unresolvedKeywords []string
	for _, keyword := range matchedKeywords {
		if _, ok := unresolved[keyword]; ok {
			unresolvedKeywords = append(unresolvedKeywords, keyword)
		}
	}
	return unresolvedKeywords, unresolvedMatches, false, sqlanalyzer.UniqueExpandedKeywords(allowedMatches), nil
}

func formatReviewTriggerDetails(matches []sqlanalyzer.ExpandedKeywordMatch) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, match := range matches {
		label := match.MatchedKeyword + " -> " + match.Expanded
		key := strings.ToLower(label)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, label)
	}
	return out
}

func displayKeywordsForReview(sql string, matchedKeywords []string) []string {
	displayKeywords := sqlanalyzer.UniqueExpandedKeywords(sqlanalyzer.ExpandMatchedKeywordMatches(sql, matchedKeywords))
	if len(displayKeywords) > 0 {
		return displayKeywords
	}
	return matchedKeywords
}

// connectionIndexInPool returns the 0-based index of the named connection for review UI header color (Java parity).
func connectionIndexInPool(pool *oracle.ExecutorPool, displayName string) int {
	if pool == nil || displayName == "" {
		return 0
	}
	for i, n := range pool.Names() {
		if n == displayName {
			return i
		}
	}
	return 0
}
