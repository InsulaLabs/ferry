BINARY_NAME=ferry
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS=-ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}"

.PHONY: clean prod cross help

help:
	@echo "Available targets:"
	@echo "  clean  - Remove build artifacts"
	@echo "  prod   - Build for current platform"
	@echo "  cross  - Cross-compile for Windows, Darwin, and Linux"

clean:
	@echo "Cleaning build artifacts..."
	rm -f $(BINARY_NAME)
	rm -f $(BINARY_NAME).exe
	rm -f $(BINARY_NAME)-*
	rm -rf build/
	@echo "Clean complete"

prod:
	@echo "Building ferry for production..."
	mkdir -p build
	go build $(LDFLAGS) -o build/$(BINARY_NAME) .
	@echo "Build complete: build/$(BINARY_NAME)"

cross: clean
	@echo "Cross-compiling ferry for multiple platforms..."
	mkdir -p build
	
	@echo "Building for Linux (amd64)..."
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-linux-amd64 .
	
	@echo "Building for Linux (arm64)..."
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-linux-arm64 .
	
	@echo "Building for Darwin (amd64)..."
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-darwin-amd64 .
	
	@echo "Building for Darwin (arm64)..."
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-darwin-arm64 .
	
	@echo "Building for Windows (amd64)..."
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-windows-amd64.exe .
	
	@echo "Building for Windows (arm64)..."
	GOOS=windows GOARCH=arm64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-windows-arm64.exe .
	
	@echo "Cross-compilation complete. Binaries available in build/ directory:"
	@ls -la build/
