APP_NAME  := rfelogappwasap
GOOS      ?= linux
GOARCH    ?= amd64
BUILD_DIR := ./bin
BINARY    := $(BUILD_DIR)/$(APP_NAME)

.PHONY: build clean

build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -ldflags="-s -w" -o $(BINARY) .

clean:
	rm -rf $(BUILD_DIR)

deploy:
	scp -i "SNIPEIT.pem" ./bin/rfelogappwasap ubuntu@ec2-18-227-182-12.us-east-2.compute.amazonaws.com:/home/ubuntu/apps/notify-api/
	