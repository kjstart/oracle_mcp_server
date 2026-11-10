# Oracle MCP Server Makefile

VERSION ?= 2.0.0
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"

.PHONY: all build build-windows build-darwin test clean tidy

all: build

# Build for current platform
build:
	go build $(LDFLAGS) -o oracle-mcp .

# Build for Windows (64-bit)
build-windows:
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o oracle-mcp.exe .

# Build for macOS (Intel)
build-darwin:
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o oracle-mcp-darwin-amd64 .

# Build for macOS (Apple Silicon)
build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o oracle-mcp-darwin-arm64 .

# Build all platforms
build-all: build-windows build-darwin build-darwin-arm64

# Run tests
test:
	go test -v ./...

# Run tests with coverage
test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Download dependencies
tidy:
	go mod tidy

# Clean build artifacts
clean:
	rm -f oracle-mcp oracle-mcp.exe oracle-mcp-darwin-* coverage.out coverage.html

# Format code
fmt:
	go fmt ./...

# Run linter
lint:
	golangci-lint run

# Create distribution package for Windows
dist-windows: build-windows
	mkdir -p dist/oracle-mcp-windows
	cp oracle-mcp.exe dist/oracle-mcp-windows/
	cp config.yaml.example dist/oracle-mcp-windows/
	cp README.md dist/oracle-mcp-windows/
	cp LICENSE dist/oracle-mcp-windows/
	@echo "Note: Add Oracle Instant Client files to dist/oracle-mcp-windows/instantclient/"
