.PHONY: all build install clean run-daemon run-client

all: build

build:
	go build -o bin/tpoold ./cmd/tpoold
	go build -o bin/tpool ./cmd/tpool

install: build
	cp bin/tpoold ~/.local/bin/
	cp bin/tpool ~/.local/bin/

clean:
	rm -rf bin/

run-daemon:
	go run ./cmd/tpoold

run-client:
	go run ./cmd/tpool
