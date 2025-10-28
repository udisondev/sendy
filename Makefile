.PHONY: build clean test bench bench-all run run-router run-chat

# Build the sendy binary
build:
	@mkdir -p bin
	go build -o bin/sendy ./cmd/sendy

# Build with optimizations (smaller binary)
build-release:
	@mkdir -p bin
	go build -ldflags="-s -w" -o bin/sendy ./cmd/sendy

# Clean build artifacts
clean:
	rm -rf bin/

# Run tests
test:
	go test ./...

# Run tests with verbose output
test-verbose:
	go test -v ./...

# Run benchmarks (router only, fast)
bench:
	@go test -bench=. -benchtime=3s -benchmem -run=^$$ ./router 2>&1 | grep -v "^[0-9]"

# Run all benchmarks including p2p (slower, requires WebRTC setup)
bench-all:
	@echo "Running router benchmarks..."
	@go test -bench=. -benchtime=3s -benchmem -run=^$$ ./router 2>&1 | grep -v "^[0-9]"
	@echo ""
	@echo "Running p2p benchmarks (this may take a while)..."
	@go test -bench=. -benchtime=3s -benchmem -run=^$$ ./p2p 2>&1 | grep -v "^[0-9]"

# Run chat (default)
run:
	./bin/sendy

# Run router
run-router:
	./bin/sendy router

# Run chat (alias)
run-chat:
	./bin/sendy

# Help
help:
	@echo "Available targets:"
	@echo "  build         - Build sendy binary"
	@echo "  build-release - Build optimized binary"
	@echo "  clean         - Remove build artifacts"
	@echo "  test          - Run tests"
	@echo "  test-verbose  - Run tests with verbose output"
	@echo "  bench         - Run benchmarks (router only, fast)"
	@echo "  bench-all     - Run all benchmarks (includes slow p2p)"
	@echo "  run           - Start chat client (default)"
	@echo "  run-router    - Start router server"
	@echo "  run-chat      - Start chat client (alias)"
