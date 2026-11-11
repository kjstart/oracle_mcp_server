// oracle-mcp-server is an MCP server for executing SQL against Oracle databases.
// It supports Human-in-the-loop confirmation for dangerous operations.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alvin/oracle-mcp-server/internal/config"
	"github.com/alvin/oracle-mcp-server/internal/mcp"
)

// Version information (set via build flags)
var (
	Version   = "2.0.2"
	BuildTime = "unknown"
)

func main() {
	// Handle signals for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		cancel()
	}()

	// Run the server
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	mcp.Version = Version
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Create and start MCP server
	server, err := mcp.NewServer(cfg)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	// Run the server (blocks until context is cancelled or stdin is closed)
	return server.Run(ctx)
}
