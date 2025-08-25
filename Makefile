# Makefile

APP_NAME = go-growatt
SRC_DIR = .
BUILD_DIR = ./build

GO_ARM = 6 # Raspberry Pi v1
# GO_ARM = 7 # Raspberry Pi v2 & v3

.PHONY: all clean build-pi build-linux

all: build-pi build-linux

build-pi:
	@mkdir -p $(BUILD_DIR)
	env GOOS=linux GOARCH=arm GOARM=$(GO_ARM) go build -o $(BUILD_DIR)/$(APP_NAME)-pi $(SRC_DIR)/main.go

build-linux:
	@mkdir -p $(BUILD_DIR)
	env GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/$(APP_NAME)-linux $(SRC_DIR)/main.go

clean:
	@rm -rf $(BUILD_DIR)
