.PHONY: run build

# Run the server directly
run:
	go run ./cmd/server/main.go

# Build the server binary
build:
	go build -o bin/server ./cmd/server/main.go

# Clean build artifacts
clean:
	rm -rf bin/
