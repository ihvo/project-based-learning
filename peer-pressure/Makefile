BINARY  := peer-pressure
PKG     := github.com/ihvo/peer-pressure
CMD     := ./cmd/peer-pressure

# Build flags
LDFLAGS := -s -w

.PHONY: all build clean test lint vet fmt run

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

run: build
	./$(BINARY) $(ARGS)

test:
	go test ./... -count=1 -timeout=30s

test-verbose:
	go test ./... -v -count=1 -timeout=30s

vet:
	go vet ./...

fmt:
	gofmt -w .

lint: vet fmt

clean:
	rm -f $(BINARY)
	go clean -testcache
