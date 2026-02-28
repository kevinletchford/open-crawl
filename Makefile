.PHONY: run-server build-all run-tui clean

# Run the benchmark server directly (background or separate pane)
run-server:
	go run ./cmd/benchserver/main.go

# Build all Go, Rust, and TS binaries
build-all:
	rm -rf bin/
	mkdir -p bin/
	go build -o bin/server ./cmd/server/main.go
	go build -o bin/crawler-go ./cmd/crawler-go/main.go
	go build -o bin/tui ./cmd/tui
	cd cmd/crawler-rust && cargo build --release
	cp cmd/crawler-rust/target/release/crawler-rust bin/crawler-rust
	cd cmd/crawler-ts && npm install && npm run build --if-present

# Run the TUI (Ensure run-server is running first)
run-tui: build-all
	./bin/tui

# Clean build artifacts
clean:
	rm -rf bin/
	cd cmd/crawler-rust && cargo clean
	cd cmd/crawler-ts && rm -rf node_modules dist
