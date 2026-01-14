.PHONY: all build install clean run-daemon run-client

all: build

build:
	mkdir -p bin
	go build -o bin/tpoold ./cmd/tpoold
	go build -o bin/tpool ./cmd/tpool
