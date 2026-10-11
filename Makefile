.PHONY: opencrawl test check run-server build-all run-tui clean

# The product packages are pure Go, so they build and test without cgo.
PRODUCT_PKGS := ./cmd/opencrawl ./internal/...

# Build the audit CLI
opencrawl:
	CGO_ENABLED=0 go build -o bin/opencrawl ./cmd/opencrawl

# Run the product tests
test:
	CGO_ENABLED=0 go test $(PRODUCT_PKGS)

# Vet, format check and test with the race detector (the race detector needs cgo)
check:
	@test -z "$$(gofmt -l cmd/opencrawl internal)" || (gofmt -l cmd/opencrawl internal; exit 1)
	go vet $(PRODUCT_PKGS)
	go test -race $(PRODUCT_PKGS)

# Run the benchmark server directly (background or separate pane)
run-server:
	go run ./cmd/bench/benchserver

# Build the CLI and the Go, Rust and TS benchmark binaries
build-all:
	rm -rf bin/
	mkdir -p bin/
	CGO_ENABLED=0 go build -o bin/opencrawl ./cmd/opencrawl
	go build -o bin/crawler-go ./cmd/bench/crawler-go/main.go
	go build -o bin/tui ./cmd/bench/tui
	cd cmd/bench/crawler-rust && cargo build --release
	cp cmd/bench/crawler-rust/target/release/crawler-rust bin/crawler-rust
	cd cmd/bench/crawler-ts && npm install && npm run build --if-present

# Run the TUI (Ensure run-server is running first)
run-tui: build-all
	./bin/tui

# Clean build artifacts
clean:
	rm -rf bin/
	cd cmd/bench/crawler-rust && cargo clean
	cd cmd/bench/crawler-ts && rm -rf node_modules dist
