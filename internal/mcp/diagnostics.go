package mcp

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/alvin/oracle-mcp-server/internal/confirm"
	"github.com/alvin/oracle-mcp-server/internal/oracle"
)

func (s *Server) stageLogger(req *jsonRPCRequest, connection string) func(string, string) {
	started := time.Now()
	return func(stage, detail string) {
		if s.diagnosticLog != nil {
			s.diagnosticLog.Printf("pid=%d request_id=%q connection=%q stage=%s elapsed_ms=%d %s", os.Getpid(), fmt.Sprint(req.ID), connection, stage, time.Since(started).Milliseconds(), detail)
		}
	}
}

func (s *Server) confirmSQL(req *jsonRPCRequest, review *confirm.ConfirmRequest) (confirm.ConfirmResult, error) {
	trace := s.stageLogger(req, review.Connection)
	review.LogStage = trace
	trace("CONFIRM_START", fmt.Sprintf("sql_bytes=%d", len(review.SQL)))
	result, err := s.confirmer.Confirm(review)
	if err != nil {
		trace("CONFIRM_ERROR", fmt.Sprintf("error=%q", err.Error()))
	} else {
		trace("CONFIRM_END", fmt.Sprintf("approved=%t allow_prefix=%t", result.Approved, result.AllowPrefix))
	}
	return result, err
}

func (s *Server) operationContext(req *jsonRPCRequest, connection string) (context.Context, func()) {
	trace := s.stageLogger(req, connection)
	trace("OPERATION_START", "")
	return oracle.WithStageLogger(context.Background(), trace), func() { trace("OPERATION_END", "") }
}
