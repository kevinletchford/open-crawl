.PHONY: run-server build-all run-tui clean build-mcp run-mcp build-browser-run run-browser-run test test-integration

# Run the benchmark server directly (background or separate pane)
run-server:
	go run ./cmd/benchserver/main.go

# Build all Go, Rust, and TS binaries
build-all:
	rm -rf bin/
	mkdir -p bin/
	go build -o bin/server ./cmd/server/main.go
	go build -o bin/crawler-go ./cmd/crawler-go/main.go
	go build -o bin/mcp-server ./cmd/mcp-server
	go build -o bin/tui ./cmd/tui
	cd cmd/crawler-rust && cargo build --release
	cp cmd/crawler-rust/target/release/crawler-rust bin/crawler-rust
	cd cmd/crawler-ts && npm install && npm run build --if-present

# Run the TUI (Ensure run-server is running first)
run-tui: build-all
	./bin/tui

# Build MCP server only
build-mcp:
	mkdir -p bin/
	go build -o bin/mcp-server ./cmd/mcp-server

# Run MCP server (stdio mode for Claude integration)
run-mcp: build-mcp
	./bin/mcp-server

# Build browser-run server
build-browser-run:
	mkdir -p bin/
	go build -o bin/browser-run ./cmd/browser-run

# Run browser-run server (default port 7600)
run-browser-run: build-browser-run
	./bin/browser-run

# Unit tests (no browser required, fast)
test:
	go test ./internal/browserrun/ -timeout 30s

# Integration tests (requires Chrome/Chromium, ~40s)
test-integration:
	go test -tags integration -timeout 120s ./internal/browserrun/ -run TestIntegration -v

# Clean build artifacts
clean:
	rm -rf bin/
	cd cmd/crawler-rust && cargo clean
	cd cmd/crawler-ts && rm -rf node_modules dist
