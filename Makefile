APP_NAME := landrop
VERSION := 1.4.0
BUILD_DIR := dist
LDFLAGS := -s -w -X main.AppVersion=$(VERSION)

.PHONY: all build build-all clean run

all: build

build:
	@mkdir -p $(BUILD_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) .
	@echo "Build complete: $(BUILD_DIR)/$(APP_NAME)"

run:
	go run .

build-all: clean
	@mkdir -p $(BUILD_DIR)
	@echo "Building for Windows (amd64)..."
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-windows-amd64.exe .
	@echo "Building for macOS Intel (amd64)..."
	GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-darwin-amd64 .
	@echo "Building for macOS Apple Silicon (arm64)..."
	GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-darwin-arm64 .
	@echo "Building for Linux (amd64)..."
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-linux-amd64 .
	@echo "Building for Linux (arm64/Raspberry Pi)..."
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-linux-arm64 .
	@echo "All binaries successfully compiled to $(BUILD_DIR)/"

clean:
	rm -rf $(BUILD_DIR)
