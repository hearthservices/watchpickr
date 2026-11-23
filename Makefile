BINARY  := watchpickr
LDFLAGS := -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: run demo test lint build clean fmt image

demo:
	go run ./cmd/watchpickr -demo

image:
	docker build -t $(BINARY):dev .

run:
	go run ./cmd/watchpickr

test:
	go test -race ./...

lint:
	gofmt -l .
	go vet ./...

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY) ./cmd/watchpickr

clean:
	rm -rf dist data

fmt:
	gofmt -w .
