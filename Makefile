BINARY_NAME=ferry
PORTAL_NAME=portal
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS=-ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}"

.PHONY: clean prod cross help

help:
	@echo "Available targets:"
	@echo "  clean  - Remove build artifacts"
	@echo "  prod   - Build ferry and portal for current platform"
	@echo "  cross  - Cross-compile ferry and portal for Windows, Darwin, and Linux"

clean:
	@echo "Cleaning build artifacts..."
	rm -f $(BINARY_NAME)
	rm -f $(BINARY_NAME).exe
	rm -f $(BINARY_NAME)-*
	rm -rf build/
	@echo "Clean complete"

prod:
	@echo "Building ferry and portal for production..."
	mkdir -p build
	go build $(LDFLAGS) -o build/$(BINARY_NAME) ./cmd/cli
	go build $(LDFLAGS) -o build/$(PORTAL_NAME) ./cmd/portal
	@echo "Build complete: build/$(BINARY_NAME), build/$(PORTAL_NAME)"

cross: clean
	@echo "Cross-compiling ferry and portal for multiple platforms..."
	mkdir -p build

	@echo "Building ferry for Linux (amd64)..."
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-linux-amd64 ./cmd/cli

	@echo "Building portal for Linux (amd64)..."
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o build/$(PORTAL_NAME)-linux-amd64 ./cmd/portal

	@echo "Building ferry for Linux (arm64)..."
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-linux-arm64 ./cmd/cli

	@echo "Building portal for Linux (arm64)..."
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o build/$(PORTAL_NAME)-linux-arm64 ./cmd/portal

	@echo "Building ferry for Darwin (amd64)..."
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-darwin-amd64 ./cmd/cli

	@echo "Building portal for Darwin (amd64)..."
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o build/$(PORTAL_NAME)-darwin-amd64 ./cmd/portal

	@echo "Building ferry for Darwin (arm64)..."
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-darwin-arm64 ./cmd/cli

	@echo "Building portal for Darwin (arm64)..."
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o build/$(PORTAL_NAME)-darwin-arm64 ./cmd/portal

	@echo "Building ferry for Windows (amd64)..."
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-windows-amd64.exe ./cmd/cli

	@echo "Building portal for Windows (amd64)..."
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o build/$(PORTAL_NAME)-windows-amd64.exe ./cmd/portal

	@echo "Building ferry for Windows (arm64)..."
	GOOS=windows GOARCH=arm64 go build $(LDFLAGS) -o build/$(BINARY_NAME)-windows-arm64.exe ./cmd/cli

	@echo "Building portal for Windows (arm64)..."
	GOOS=windows GOARCH=arm64 go build $(LDFLAGS) -o build/$(PORTAL_NAME)-windows-arm64.exe ./cmd/portal

	@echo "Cross-compilation complete. Binaries available in build/ directory:"
	@ls -la build/
