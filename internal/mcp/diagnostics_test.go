package mcp

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestStageDiagnosticsCorrelateWithoutLoggingSQL(t *testing.T) {
	var output bytes.Buffer
	s := &Server{diagnosticLog: log.New(&output, "UTC ", log.LstdFlags|log.LUTC)}
	req := &jsonRPCRequest{ID: "request-17"}
	trace := s.stageLogger(req, "test-connection")
	trace("CONFIRM_START", "sql_bytes=310000")
	ctx, finish := s.operationContext(req, "test-connection")
	if ctx == nil {
		t.Fatal("missing operation context")
	}
	finish()
	text := output.String()
	for _, expected := range []string{"pid=", "request_id=\"request-17\"", "connection=\"test-connection\"", "stage=CONFIRM_START", "stage=OPERATION_START", "stage=OPERATION_END", "elapsed_ms="} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %s", expected)
		}
	}
}
